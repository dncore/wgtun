package daemon

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/dncore/wgtun/internal/logs"
	"github.com/dncore/wgtun/internal/uapi"
	"github.com/dncore/wgtun/internal/wgconf"
)

// fakeUAPI answers get=1 with getReply and any set=1 with errno=0, recording
// every request body it received. Requests are answered per connection.
func fakeUAPI(t *testing.T, sockPath, getReply string) chan string {
	t.Helper()
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 16)
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
				req := string(buf)
				got <- req
				if strings.HasPrefix(req, "get=1") {
					c.Write([]byte(getReply))
					return
				}
				c.Write([]byte("errno=0\n\n"))
			}(c)
		}
	}()
	return got
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

// waitSet returns the next request that is not the periodic get=1 probe: every
// reconcile pass reads the device before it decides anything, so the interesting
// request is always the one after it.
func waitSet(t *testing.T, got chan string) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case req := <-got:
			if strings.HasPrefix(req, "get=1") {
				continue
			}
			return req
		case <-deadline:
			t.Fatal("no UAPI set request was sent")
			return ""
		}
	}
}

// requireNoSet fails if the daemon wrote anything but a get=1 probe.
func requireNoSet(t *testing.T, got chan string) {
	t.Helper()
	for _, req := range collect(got) {
		if !strings.HasPrefix(req, "get=1") {
			t.Fatalf("unexpected UAPI request:\n%s", req)
		}
	}
}

func hasEvent(ev *logs.Store, level logs.Level, substr string) bool {
	for _, e := range ev.Query(logs.Filter{MinLevel: level}) {
		if strings.Contains(e.Msg, substr) {
			return true
		}
	}
	return false
}

func countEvents(ev *logs.Store, level logs.Level, substr string) int {
	n := 0
	for _, e := range ev.Query(logs.Filter{MinLevel: level}) {
		if strings.Contains(e.Msg, substr) {
			n++
		}
	}
	return n
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
