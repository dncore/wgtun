package daemon

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dncore/wgtun/internal/logs"
	"github.com/dncore/wgtun/internal/paths"
	"github.com/dncore/wgtun/internal/uapi"
	"github.com/dncore/wgtun/internal/wgconf"
)

// newFillFixture builds an instance with one peer carrying both an endpoint and
// allowed IPs, plus a fake UAPI socket to observe what the daemon sends.
func newFillFixture(t *testing.T, pub, endpoint string) (*inst, chan string) {
	t.Helper()
	sockDir, err := os.MkdirTemp("/tmp", "wgtun-fill")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	old := paths.WireGuardSockDir
	paths.WireGuardSockDir = sockDir
	t.Cleanup(func() { paths.WireGuardSockDir = old })

	i := &inst{name: "wg1", tun: "utun9", conf: &wgconf.Config{Peers: []wgconf.Peer{{
		PublicKey:  pub,
		Endpoint:   endpoint,
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")},
	}}}}
	got := fakeUAPI(t, filepath.Join(sockDir, "utun9.sock"), "errno=0\n\n")
	return i, got
}

// TestFillEndpointUsesSurgicalPush is the regression guard for the 5s
// replace_peers storm: a peer that exists without an endpoint (DNS was not up
// at push time) needs its endpoint, not a full config re-push that resets every
// peer session in the instance.
func TestFillEndpointUsesSurgicalPush(t *testing.T) {
	pub, _ := wgconf.GeneratePrivateKey()
	i, got := newFillFixture(t, pub, "ddns.example:51820")
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7"}})
	sup, _ := newTestSupervisor(t)

	st := &uapi.DeviceStatus{Peers: []uapi.PeerStatus{{PublicKey: pub}}}
	sup.maybeFillEndpointsLocked(i, st)

	req := waitReq(t, got)
	if !strings.Contains(req, "endpoint=198.51.100.7:51820\n") {
		t.Errorf("endpoint not pushed:\n%s", req)
	}
	for _, bad := range []string{"replace_peers", "replace_allowed_ips", "allowed_ip=", "private_key="} {
		if strings.Contains(req, bad) {
			t.Errorf("fill must not send %q (it would reset every peer):\n%s", bad, req)
		}
	}
	if !hasEvent(sup.ev, logs.Info, "resolved and pushed") {
		t.Error("a successful fill should be logged")
	}
}

// A peer the device never accepted needs its whole block (allowed IPs are
// missing too) — still without replace_peers.
func TestFillEndpointUpsertsMissingPeer(t *testing.T) {
	pub, _ := wgconf.GeneratePrivateKey()
	i, got := newFillFixture(t, pub, "ddns.example:51820")
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7"}})
	sup, _ := newTestSupervisor(t)

	sup.maybeFillEndpointsLocked(i, &uapi.DeviceStatus{}) // device has no peers

	req := waitReq(t, got)
	for _, want := range []string{
		"public_key=", "endpoint=198.51.100.7:51820\n",
		"replace_allowed_ips=true\n", "allowed_ip=198.51.100.0/24\n",
	} {
		if !strings.Contains(req, want) {
			t.Errorf("missing %q:\n%s", want, req)
		}
	}
	if strings.Contains(req, "replace_peers") || strings.Contains(req, "private_key=") {
		t.Errorf("upsert must not touch other peers or the device key:\n%s", req)
	}
}

// A peer the device keeps dropping (wireguard-go ignores a peer whose key
// equals the device's own) must not be re-pushed on every tick forever.
func TestFillEndpointGivesUpOnRefusedPeer(t *testing.T) {
	pub, _ := wgconf.GeneratePrivateKey()
	i, got := newFillFixture(t, pub, "ddns.example:51820")
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7"}})
	sup, _ := newTestSupervisor(t)

	st := &uapi.DeviceStatus{} // the peer never appears, however often we push
	for n := 0; n < peerFillGiveUp; n++ {
		sup.maybeFillEndpointsLocked(i, st)
		waitReq(t, got)
	}
	if !hasEvent(sup.ev, logs.Error, "still absent from the device") {
		t.Error("giving up must be reported once, at error level")
	}

	// past the cap nothing is sent any more
	for n := 0; n < 3; n++ {
		sup.maybeFillEndpointsLocked(i, st)
	}
	requireNoReq(t, got)
}

// DNS still down (the boot case) must keep retrying but never invent a push.
func TestFillEndpointUnresolvedSendsNothing(t *testing.T) {
	pub, _ := wgconf.GeneratePrivateKey()
	i, got := newFillFixture(t, pub, "ddns.example:51820")
	stubLookup(t, map[string][]string{}) // NXDOMAIN
	sup, _ := newTestSupervisor(t)

	sup.maybeFillEndpointsLocked(i, &uapi.DeviceStatus{})
	requireNoReq(t, got)
	if !hasEvent(sup.ev, logs.Warn, "unresolved (will retry)") {
		t.Error("an unresolved endpoint should warn")
	}
}

func TestFillEndpointLeavesHealthyPeerAlone(t *testing.T) {
	pub, _ := wgconf.GeneratePrivateKey()
	i, got := newFillFixture(t, pub, "ddns.example:51820")
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7"}})
	sup, _ := newTestSupervisor(t)

	st := &uapi.DeviceStatus{Peers: []uapi.PeerStatus{{
		PublicKey: pub, Endpoint: "198.51.100.7:51820",
	}}}
	sup.maybeFillEndpointsLocked(i, st)
	requireNoReq(t, got)

	// a healthy peer also clears any earlier give-up counter
	i.bumpPeerRetry(pub)
	sup.maybeFillEndpointsLocked(i, st)
	if i.peerRetry[pub] != 0 {
		t.Error("a healthy peer must clear its retry counter")
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
