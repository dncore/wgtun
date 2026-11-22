package daemon

import (
	"net/netip"
	"testing"
	"time"

	"github.com/dncore/wgtun/internal/uapi"
	"github.com/dncore/wgtun/internal/wgconf"
)

func TestPeerUnhealthy(t *testing.T) {
	now := time.Now()
	keepalive := &wgconf.Peer{PublicKey: "k", PersistentKeepalive: 25}
	noKeepalive := &wgconf.Peer{PublicKey: "k"}

	cases := []struct {
		name   string
		live   *uapi.PeerStatus
		peer   *wgconf.Peer
		start  time.Time
		expect bool
	}{
		{"no peer", nil, keepalive, now.Add(-time.Hour), true},
		{"no endpoint", &uapi.PeerStatus{Endpoint: ""}, keepalive, now.Add(-time.Hour), true},
		{"fresh handshake", &uapi.PeerStatus{Endpoint: "a:1", LastHandshake: now.Add(-time.Minute)}, keepalive, now.Add(-time.Hour), false},
		{"stale handshake", &uapi.PeerStatus{Endpoint: "a:1", LastHandshake: now.Add(-handshakeStaleAfter - time.Second)}, keepalive, now.Add(-time.Hour), true},
		{"never, within grace", &uapi.PeerStatus{Endpoint: "a:1", TxBytes: 100}, keepalive, now.Add(-time.Minute), false},
		{"never, no keepalive", &uapi.PeerStatus{Endpoint: "a:1", TxBytes: 100}, noKeepalive, now.Add(-time.Hour), false},
		{"never, keepalive, no tx", &uapi.PeerStatus{Endpoint: "a:1"}, keepalive, now.Add(-time.Hour), false},
		{"never, keepalive, after grace", &uapi.PeerStatus{Endpoint: "a:1", TxBytes: 100}, keepalive, now.Add(-time.Hour), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := &inst{startedAt: tc.start}
			if got := i.peerUnhealthy(tc.live, tc.peer); got != tc.expect {
				t.Errorf("peerUnhealthy = %v want %v", got, tc.expect)
			}
		})
	}
}

func TestNextCandidate(t *testing.T) {
	i := &inst{}
	cands := []string{"a:1", "b:1", "c:1"}
	if got := i.nextCandidate("p", "a:1", cands); got != "b:1" {
		t.Fatalf("first rotation = %q want b:1", got)
	}
	// The throttle holds the next call back.
	if got := i.nextCandidate("p", "b:1", cands); got != "" {
		t.Fatalf("rotation was not throttled: %q", got)
	}
	i.peerLastSwitch["p"] = time.Now().Add(-endpointRotateEvery - time.Second)
	if got := i.nextCandidate("p", "b:1", cands); got != "c:1" {
		t.Fatalf("second rotation = %q want c:1", got)
	}
	// A single answer has nothing to rotate to.
	if got := i.nextCandidate("p", "a:1", []string{"a:1"}); got != "" {
		t.Fatalf("single candidate rotated to %q", got)
	}
	// The current address is not in the answer set: the caller retargets,
	// it does not rotate.
	if got := i.nextCandidate("q", "x:1", cands); got != "" {
		t.Fatalf("unknown current rotated to %q", got)
	}
}

func TestNeedsEndpointPin(t *testing.T) {
	conf := &wgconf.Config{Peers: []wgconf.Peer{
		{AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}},
	}}
	if !needsEndpointPin(conf, netip.MustParseAddr("203.0.113.7")) {
		t.Error("a default route covers the endpoint and needs a pin")
	}
	narrow := &wgconf.Config{Peers: []wgconf.Peer{
		{AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}},
	}}
	if needsEndpointPin(narrow, netip.MustParseAddr("203.0.113.7")) {
		t.Error("a narrow allowed-ips set does not cover the endpoint")
	}
	narrow.Interface.Table = "off"
	if needsEndpointPin(narrow, netip.MustParseAddr("10.0.0.7")) {
		t.Error("Table=off means the caller owns routing")
	}
	narrow.Interface.Table = ""
	if !needsEndpointPin(narrow, netip.MustParseAddr("10.0.0.7")) {
		t.Error("an endpoint inside allowed-ips needs a pin")
	}
}

func TestParseRouteGet(t *testing.T) {
	out := `   route to: 203.0.113.5
destination: 203.0.113.5
    gateway: 192.0.2.1
  interface: en0
      flags: <UP,GATEWAY,HOST,DONE>
`
	gw, iface := parseRouteGet(out)
	if gw != "192.0.2.1" || iface != "en0" {
		t.Fatalf("parseRouteGet = %q/%q", gw, iface)
	}
}

func TestParseIPRouteGet(t *testing.T) {
	for _, tc := range []struct {
		out string
		gw  string
		dev string
	}{
		{"203.0.113.7 via 192.168.1.1 dev eth0 src 192.168.1.5 uid 0\n", "192.168.1.1", "eth0"},
		{"203.0.113.7 dev eth0 src 192.168.1.5\n", "", "eth0"},
		{"2001:db8::1 from :: via fe80::1 dev enp1s0 proto ra\n", "fe80::1", "enp1s0"},
	} {
		gw, dev := parseIPRouteGet(tc.out)
		if gw != tc.gw || dev != tc.dev {
			t.Errorf("parseIPRouteGet(%q) = %q/%q want %q/%q", tc.out, gw, dev, tc.gw, tc.dev)
		}
	}
}
