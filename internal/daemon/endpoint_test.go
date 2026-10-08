package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dncore/wgtun/internal/paths"
	"github.com/dncore/wgtun/internal/uapi"
	"github.com/dncore/wgtun/internal/wgconf"
)

// fakeUAPI answers one request per connection and reports the request bodies
// it received.
func fakeUAPI(t *testing.T, sockPath, reply string) chan string {
	t.Helper()
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 0, 4096)
				tmp := make([]byte, 4096)
				for {
					n, err := c.Read(tmp)
					if n > 0 {
						buf = append(buf, tmp[:n]...)
						if strings.HasSuffix(string(buf), "\n\n") {
							break
						}
					}
					if err != nil {
						return
					}
				}
				got <- string(buf)
				c.Write([]byte(reply))
			}(c)
		}
	}()
	return got
}

// newEndpointFixture builds a running-looking instance whose UAPI socket is a
// fake server, and points the socket dir at a short temp path (the unix socket
// path limit is tight on darwin).
func newEndpointFixture(t *testing.T, peers []wgconf.Peer) (*inst, chan string) {
	t.Helper()
	sockDir, err := os.MkdirTemp("/tmp", "wgtun-ep")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	old := paths.WireGuardSockDir
	paths.WireGuardSockDir = sockDir
	t.Cleanup(func() { paths.WireGuardSockDir = old })

	i := &inst{name: "wg1", tun: "utun9", conf: &wgconf.Config{Peers: peers}}
	got := fakeUAPI(t, filepath.Join(sockDir, "utun9.sock"), "errno=0\n\n")
	return i, got
}

// stubLookup replaces the resolver seam with a static zone.
func stubLookup(t *testing.T, zone map[string][]string) {
	t.Helper()
	old := lookupIPAddr
	lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		ips, ok := zone[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		out := make([]net.IPAddr, 0, len(ips))
		for _, s := range ips {
			out = append(out, net.IPAddr{IP: net.ParseIP(s)})
		}
		return out, nil
	}
	t.Cleanup(func() { lookupIPAddr = old })
}

func waitReq(t *testing.T, got chan string) string {
	t.Helper()
	select {
	case req := <-got:
		return req
	case <-time.After(3 * time.Second):
		t.Fatal("no UAPI request was sent")
		return ""
	}
}

func requireNoReq(t *testing.T, got chan string) {
	t.Helper()
	select {
	case req := <-got:
		t.Fatalf("unexpected UAPI request:\n%s", req)
	default:
	}
}

func TestIsHostnameEndpoint(t *testing.T) {
	for _, tc := range []struct {
		ep   string
		want bool
	}{
		{"ddns.example:51820", true},
		{"203.0.113.7:51820", false},
		{"[2001:db8::7]:51820", false},
		{"", false},
		{"ddns.example", false}, // no port: not a usable endpoint
	} {
		if got := isHostnameEndpoint(tc.ep); got != tc.want {
			t.Errorf("isHostnameEndpoint(%q) = %v want %v", tc.ep, got, tc.want)
		}
	}
}

func TestResolveEndpointAll(t *testing.T) {
	calls := 0
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7", "2001:db8::7"}})
	old := lookupIPAddr
	lookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		calls++
		return old(ctx, host)
	}

	got, err := resolveEndpointAll("ddns.example:51820")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"198.51.100.7:51820", "[2001:db8::7]:51820"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("candidates = %v want %v", got, want)
	}

	// a literal address is authoritative and must not hit the resolver
	got, err = resolveEndpointAll("203.0.113.7:51820")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "203.0.113.7:51820" {
		t.Fatalf("literal candidates = %v", got)
	}
	if calls != 1 {
		t.Fatalf("resolver called %d times, want 1", calls)
	}
}

func TestRefreshEndpointsRetargetsChangedDNS(t *testing.T) {
	pub, _ := wgconf.GeneratePrivateKey()
	i, got := newEndpointFixture(t, []wgconf.Peer{{PublicKey: pub, Endpoint: "ddns.example:51820"}})
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7"}})
	sup, _ := newTestSupervisor(t)

	st := &uapi.DeviceStatus{Peers: []uapi.PeerStatus{{
		PublicKey:     pub,
		Endpoint:      "203.0.113.7:51820",
		LastHandshake: time.Now(),
	}}}
	sup.maybeRefreshEndpointsLocked(i, st)

	req := waitReq(t, got)
	hexKey, err := wgconf.KeyHex(pub)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"set=1\n",
		"public_key=" + hexKey + "\n",
		"endpoint=198.51.100.7:51820\n",
	} {
		if !strings.Contains(req, want) {
			t.Errorf("request missing %q\ngot:\n%s", want, req)
		}
	}
	// the point of SetPeerEndpoint: no peer replacement, no session reset
	for _, bad := range []string{"replace_peers", "replace_allowed_ips", "private_key", "listen_port", "allowed_ip="} {
		if strings.Contains(req, bad) {
			t.Errorf("request must not contain %q\ngot:\n%s", bad, req)
		}
	}
	if !i.lastEPCheck.After(time.Time{}) {
		t.Error("a check must stamp lastEPCheck")
	}
}

func TestRefreshEndpointsKeepsLiveWhenAnyCandidateMatches(t *testing.T) {
	pub, _ := wgconf.GeneratePrivateKey()
	i, got := newEndpointFixture(t, []wgconf.Peer{{PublicKey: pub, Endpoint: "ddns.example:51820"}})
	// round-robin: order changes per lookup, the second answer is the live one
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7", "203.0.113.7"}})
	sup, _ := newTestSupervisor(t)

	st := &uapi.DeviceStatus{Peers: []uapi.PeerStatus{{
		PublicKey:     pub,
		Endpoint:      "203.0.113.7:51820",
		LastHandshake: time.Now(),
	}}}
	sup.maybeRefreshEndpointsLocked(i, st)
	requireNoReq(t, got)
}

func TestRefreshEndpointsIgnoresLiteralEndpoint(t *testing.T) {
	pub, _ := wgconf.GeneratePrivateKey()
	i, got := newEndpointFixture(t, []wgconf.Peer{{PublicKey: pub, Endpoint: "203.0.113.9:51820"}})
	stubLookup(t, map[string][]string{}) // any lookup would fail loudly
	sup, _ := newTestSupervisor(t)

	st := &uapi.DeviceStatus{Peers: []uapi.PeerStatus{{
		PublicKey:     pub,
		Endpoint:      "203.0.113.7:51820",
		LastHandshake: time.Now(),
	}}}
	sup.maybeRefreshEndpointsLocked(i, st)
	requireNoReq(t, got)
}

func TestRefreshEndpointsUnresolvedIsNotFatal(t *testing.T) {
	pub, _ := wgconf.GeneratePrivateKey()
	i, got := newEndpointFixture(t, []wgconf.Peer{{PublicKey: pub, Endpoint: "ddns.example:51820"}})
	stubLookup(t, map[string][]string{}) // NXDOMAIN
	sup, _ := newTestSupervisor(t)

	st := &uapi.DeviceStatus{Peers: []uapi.PeerStatus{{
		PublicKey:     pub,
		Endpoint:      "203.0.113.7:51820",
		LastHandshake: time.Now(),
	}}}
	sup.maybeRefreshEndpointsLocked(i, st) // must not panic or push
	requireNoReq(t, got)
}

func TestEndpointCheckDue(t *testing.T) {
	pub, _ := wgconf.GeneratePrivateKey()
	i := &inst{name: "wg1", conf: &wgconf.Config{Peers: []wgconf.Peer{
		{PublicKey: pub, Endpoint: "ddns.example:51820"},
	}}}
	status := func(hs time.Time) *uapi.DeviceStatus {
		return &uapi.DeviceStatus{Peers: []uapi.PeerStatus{{
			PublicKey: pub, Endpoint: "203.0.113.7:51820", LastHandshake: hs,
		}}}
	}
	now := time.Now()

	i.lastEPCheck = now
	if i.endpointCheckDue(status(now)) {
		t.Error("must not re-check right after a check")
	}

	// fast lane: handshake went stale and the throttle has expired
	i.lastEPCheck = now.Add(-endpointFastRecheck - time.Second)
	stale := now.Add(-handshakeStaleAfter - time.Second)
	if !i.endpointCheckDue(status(stale)) {
		t.Error("stale handshake should trigger a re-check")
	}

	// a peer that never handshaked is idle, not necessarily broken
	if i.endpointCheckDue(status(time.Time{})) {
		t.Error("a never-handshaked peer must not hold the fast lane open")
	}

	// ...and the same holds for the 1970 timestamp wireguard-go reports for
	// "never handshaked" if a caller bypasses parseGet's normalisation
	if i.endpointCheckDue(status(time.Unix(0, 0))) {
		t.Error("an epoch-zero handshake must count as never handshaked")
	}

	// periodic interval: always due
	i.lastEPCheck = now.Add(-endpointRecheck - time.Second)
	if !i.endpointCheckDue(status(time.Time{})) {
		t.Error("periodic re-check should be due")
	}
}
