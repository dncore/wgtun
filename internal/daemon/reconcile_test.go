package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dncore/wgtun/internal/logs"
	"github.com/dncore/wgtun/internal/paths"
	"github.com/dncore/wgtun/internal/wgconf"
)

// endpointRig is a loaded instance that looks running, whose UAPI socket is a
// fake device answering get=1 with a canned status.
type endpointRig struct {
	sup  *Supervisor
	inst *inst
	peer string // peer public key (base64)
	got  chan string
}

// newEndpointRig writes a config whose single peer points at confEndpoint and
// installs a fake device whose status reports liveEndpoint for it (or no peer
// at all when onDevice is false).
func newEndpointRig(t *testing.T, confEndpoint, liveEndpoint string, onDevice bool) *endpointRig {
	t.Helper()
	sup, _ := newTestSupervisor(t)
	priv, _ := wgconf.GeneratePrivateKey()
	peerPub, _ := wgconf.GeneratePrivateKey()

	content := "[Interface]\nPrivateKey = " + priv + "\nAddress = 203.0.113.2/24\nMTU = 1420\n\n" +
		"[Peer]\nPublicKey = " + peerPub + "\nAllowedIPs = 203.0.113.0/24\n"
	if confEndpoint != "" {
		content += "Endpoint = " + confEndpoint + "\n"
	}
	if err := os.WriteFile(filepath.Join(paths.ConfDir, "wg1.conf"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sup.LoadConfigs(); err != nil {
		t.Fatal(err)
	}

	sockDir, err := os.MkdirTemp("/tmp", "wgtun-rc") // unix socket paths must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	oldSock := paths.WireGuardSockDir
	paths.WireGuardSockDir = sockDir
	t.Cleanup(func() { paths.WireGuardSockDir = oldSock })

	reply := "errno=0\nlisten_port=51820\n\n"
	if onDevice {
		hexKey, err := wgconf.KeyHex(peerPub)
		if err != nil {
			t.Fatal(err)
		}
		reply = "errno=0\nlisten_port=51820\npublic_key=" + hexKey + "\n"
		if liveEndpoint != "" {
			reply += "endpoint=" + liveEndpoint + "\n"
		}
		reply += "allowed_ip=203.0.113.0/24\n\n"
	}

	i := sup.insts["wg1"]
	i.tun = "utun9"
	i.pid = os.Getpid() // "running": this test process is alive and is never reaped
	got := fakeUAPI(t, filepath.Join(sockDir, "utun9.sock"), reply)
	return &endpointRig{sup: sup, inst: i, peer: peerPub, got: got}
}

// prime runs one pass so the instance has a cached status, then drops the get=1
// traffic. Endpoint decisions need a status to compare against, exactly as in
// production, where the first tick caches and the later ones act.
func (r *endpointRig) prime(t *testing.T) {
	t.Helper()
	r.sup.Reconcile()
	drain(r.got)
}

func drain(got chan string) {
	for {
		select {
		case <-got:
		default:
			return
		}
	}
}

func collect(got chan string) []string {
	var out []string
	for {
		select {
		case r := <-got:
			out = append(out, r)
		default:
			return out
		}
	}
}

func hasRequest(reqs []string, substr string) bool {
	for _, r := range reqs {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}

// TestReconcileRetargetsMovedEndpoint is the DDNS case: the record points
// somewhere else now, so the peer is retargeted with one peer block and no
// replace_peers, which leaves the session and allowed IPs alone.
func TestReconcileRetargetsMovedEndpoint(t *testing.T) {
	rig := newEndpointRig(t, "ddns.example:51820", "203.0.113.7:51820", true)
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7"}})

	rig.prime(t)
	rig.sup.Reconcile()

	req := waitSet(t, rig.got)
	if !strings.Contains(req, "endpoint=198.51.100.7:51820\n") {
		t.Errorf("endpoint not pushed:\n%s", req)
	}
	for _, bad := range []string{"replace_peers", "replace_allowed_ips", "allowed_ip=", "private_key="} {
		if strings.Contains(req, bad) {
			t.Errorf("retarget must not send %q (it would reset the peer):\n%s", bad, req)
		}
	}
	if !hasEvent(rig.sup.ev, logs.Info, "changed), re-pushed") {
		t.Error("a moved endpoint should be logged")
	}
}

// A round-robin record answers in a different order on every lookup; flipping
// between equivalent addresses would be churn, not progress.
func TestReconcileKeepsEndpointWhenAnyCandidateMatches(t *testing.T) {
	rig := newEndpointRig(t, "ddns.example:51820", "203.0.113.7:51820", true)
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7", "203.0.113.7"}})

	rig.prime(t)
	rig.sup.Reconcile()
	requireNoSet(t, rig.got)
}

// A literal ip:port is authoritative and never re-resolved.
func TestReconcileIgnoresLiteralEndpoint(t *testing.T) {
	rig := newEndpointRig(t, "203.0.113.9:51820", "203.0.113.7:51820", true)
	stubLookup(t, map[string][]string{}) // any lookup would fail loudly

	rig.prime(t)
	rig.sup.Reconcile()
	requireNoSet(t, rig.got)
}

// DNS being down is not fatal and not a reason to invent a push.
func TestReconcileUnresolvedEndpointWarnsAndPushesNothing(t *testing.T) {
	rig := newEndpointRig(t, "ddns.example:51820", "203.0.113.7:51820", true)
	stubLookup(t, map[string][]string{}) // NXDOMAIN

	rig.prime(t)
	rig.sup.Reconcile()
	requireNoSet(t, rig.got)
	if !hasEvent(rig.sup.ev, logs.Warn, "unresolved (will retry)") {
		t.Error("an unresolved endpoint should warn")
	}
}

// A peer that exists without an endpoint only needs the endpoint: one peer
// block, and the device's allowed IPs are left alone.
func TestReconcileFillsEndpointWithoutAllowedIPs(t *testing.T) {
	rig := newEndpointRig(t, "ddns.example:51820", "", true) // peer exists, no endpoint
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7"}})

	rig.prime(t)
	rig.sup.Reconcile()

	req := waitSet(t, rig.got)
	if !strings.Contains(req, "endpoint=198.51.100.7:51820\n") {
		t.Errorf("endpoint not pushed:\n%s", req)
	}
	for _, bad := range []string{"replace_peers", "replace_allowed_ips", "allowed_ip=", "private_key="} {
		if strings.Contains(req, bad) {
			t.Errorf("filling an endpoint must not send %q:\n%s", bad, req)
		}
	}
	if !hasEvent(rig.sup.ev, logs.Info, "resolved and pushed") {
		t.Error("a filled endpoint should be logged")
	}
}

// A peer the device never accepted needs its whole block, allowed IPs included —
// still without replace_peers, so other peers are untouched.
func TestReconcileUpsertsMissingPeer(t *testing.T) {
	rig := newEndpointRig(t, "ddns.example:51820", "", false) // device has no peers
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7"}})

	rig.prime(t)
	rig.sup.Reconcile()

	req := waitSet(t, rig.got)
	for _, want := range []string{
		"public_key=", "endpoint=198.51.100.7:51820\n",
		"replace_allowed_ips=true\n", "allowed_ip=203.0.113.0/24\n",
	} {
		if !strings.Contains(req, want) {
			t.Errorf("missing %q:\n%s", want, req)
		}
	}
	if strings.Contains(req, "replace_peers") || strings.Contains(req, "private_key=") {
		t.Errorf("upsert must not touch other peers or the device key:\n%s", req)
	}
}

// A peer the device keeps dropping (wireguard-go ignores a peer whose key equals
// the device's own) must not be re-pushed on every tick forever.
func TestReconcileGivesUpOnRefusedPeer(t *testing.T) {
	rig := newEndpointRig(t, "ddns.example:51820", "", false)
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7"}})
	rig.prime(t)

	for n := 0; n < peerFillGiveUp; n++ {
		rig.sup.Reconcile()
		waitSet(t, rig.got)
	}
	if !hasEvent(rig.sup.ev, logs.Error, "still absent from the device") {
		t.Error("giving up must be reported once, at error level")
	}
	for n := 0; n < 3; n++ {
		rig.sup.Reconcile()
	}
	requireNoSet(t, rig.got)
}

// A healthy peer is left alone, and any earlier give-up counter is cleared.
func TestReconcileLeavesHealthyPeerAlone(t *testing.T) {
	rig := newEndpointRig(t, "ddns.example:51820", "198.51.100.7:51820", true)
	stubLookup(t, map[string][]string{"ddns.example": {"198.51.100.7"}})

	rig.prime(t)
	rig.inst.bumpPeerRetry(rig.peer)
	rig.sup.Reconcile()
	requireNoSet(t, rig.got)
	if rig.inst.peerRetry[rig.peer] != 0 {
		t.Error("a healthy peer must clear its retry counter")
	}
}

// TestReconcileReleasesTheLockDuringIO is the point of the three-phase pass:
// while a lookup is in flight the supervisor lock must be free. Holding it
// across the DNS lookup (up to its timeout) is what used to stall the TUI and
// every API call.
func TestReconcileReleasesTheLockDuringIO(t *testing.T) {
	rig := newEndpointRig(t, "ddns.example:51820", "203.0.113.7:51820", true)
	rig.prime(t)

	lookupStarted := make(chan struct{})
	lookupRelease := make(chan struct{})
	apiProceeded := make(chan struct{})
	old := lookupIPAddr
	lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		close(lookupStarted)
		<-lookupRelease // hold the lookup open while the API is poked
		return []net.IPAddr{{IP: net.ParseIP("198.51.100.7")}}, nil
	}
	t.Cleanup(func() { lookupIPAddr = old })

	finished := make(chan struct{})
	go func() {
		rig.sup.Reconcile()
		close(finished)
	}()

	<-lookupStarted
	go func() {
		// A read-only API call needs the same lock the reconcile loop takes.
		if views := rig.sup.Views(); len(views) != 1 {
			t.Errorf("views while resolving = %d", len(views))
		}
		close(apiProceeded)
	}()

	select {
	case <-apiProceeded:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor lock was held across the DNS lookup")
	}
	close(lookupRelease)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile did not finish")
	}

	// and the pass still pushed the retargeted endpoint
	reqs := collect(rig.got)
	if !hasRequest(reqs, "endpoint=198.51.100.7:51820") {
		t.Errorf("retarget missing from %d request(s): %v", len(reqs), reqs)
	}
}

// An untouched config must not be re-read and re-parsed on every tick, while a
// real change must still be picked up.
func TestConfigRescanSkipsUnchangedFile(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	writeConf(t, "wg1")
	if err := sup.LoadConfigs(); err != nil {
		t.Fatal(err)
	}

	// Tamper with the parsed config: if the rescan re-reads the file, the
	// tampering is gone afterwards — which is exactly what the cache prevents.
	sup.insts["wg1"].conf.Peers = nil
	if err := sup.LoadConfigs(); err != nil {
		t.Fatal(err)
	}
	if sup.insts["wg1"].conf.Peers != nil {
		t.Error("an unchanged config must not be re-parsed on every tick")
	}

	path := filepath.Join(paths.ConfDir, "wg1.conf")
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if err := sup.LoadConfigs(); err != nil {
		t.Fatal(err)
	}
	if len(sup.insts["wg1"].conf.Peers) != 1 {
		t.Error("a touched config must be re-parsed")
	}
}

// TestReconcileInterval pins the tick knob: default, override, rejected
// garbage, and a warning when the interval would coarsen the fast lane.
func TestReconcileInterval(t *testing.T) {
	newStore := func(t *testing.T) *logs.Store {
		t.Helper()
		ev, err := logs.New(filepath.Join(t.TempDir(), "events.jsonl"), 100, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}

	t.Setenv("WGTUN_RECONCILE_INTERVAL", "")
	if got := reconcileInterval(newStore(t)); got != 5*time.Second {
		t.Errorf("default = %s want 5s", got)
	}

	t.Setenv("WGTUN_RECONCILE_INTERVAL", "30s")
	if got := reconcileInterval(newStore(t)); got != 30*time.Second {
		t.Errorf("override = %s want 30s", got)
	}

	// garbage and absurdly small values fall back to the default and say so
	ev := newStore(t)
	t.Setenv("WGTUN_RECONCILE_INTERVAL", "nonsense")
	if got := reconcileInterval(ev); got != 5*time.Second {
		t.Errorf("garbage = %s want the default 5s", got)
	}
	t.Setenv("WGTUN_RECONCILE_INTERVAL", "10ms")
	if got := reconcileInterval(ev); got != 5*time.Second {
		t.Errorf("too small = %s want the default 5s", got)
	}
	if !hasEvent(ev, logs.Warn, "ignoring WGTUN_RECONCILE_INTERVAL") {
		t.Error("a bad interval should be reported")
	}

	// above the fast-lane floor it still applies, with a warning
	ev = newStore(t)
	t.Setenv("WGTUN_RECONCILE_INTERVAL", "30s")
	if got := reconcileInterval(ev); got != 30*time.Second {
		t.Errorf("large override = %s want 30s", got)
	}
	if !hasEvent(ev, logs.Warn, "fast-lane floor") {
		t.Error("an interval above the fast lane should warn")
	}
}

// BenchmarkLoadConfigsUnchanged vs ...AfterTouch measures the mtime cache: the
// cached path is what an idle tick costs now, the touched path is what every
// tick used to cost (a read plus a full wg-quick parse per config).
func BenchmarkLoadConfigsUnchanged(b *testing.B) {
	t := &testing.T{}
	sup, _ := newTestSupervisor(t)
	writeConf(t, "wg1")
	if err := sup.LoadConfigs(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := sup.LoadConfigs(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLoadConfigsAfterTouch(b *testing.B) {
	t := &testing.T{}
	sup, _ := newTestSupervisor(t)
	writeConf(t, "wg1")
	if err := sup.LoadConfigs(); err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(paths.ConfDir, "wg1.conf")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		when := time.Unix(0, int64(i)+1) // still a fresh revision for the cache
		if err := os.Chtimes(path, when, when); err != nil {
			b.Fatal(err)
		}
		if err := sup.LoadConfigs(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReconcilePass is one full heartbeat for one instance: the UAPI get,
// the endpoint decision and the status cache update.
func BenchmarkReconcilePass(b *testing.B) {
	t := &testing.T{}
	rig := newEndpointRig(t, "ddns.example:51820", "203.0.113.7:51820", true)
	stubLookup(t, map[string][]string{"ddns.example": {"203.0.113.7"}})
	rig.sup.Reconcile()
	drain(rig.got)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rig.sup.Reconcile()
		drain(rig.got)
	}
}

// A broken config is reported once per revision, not twelve times a minute.
func TestBrokenConfigIsReportedOnce(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	path := filepath.Join(paths.ConfDir, "broken.conf")
	if err := os.WriteFile(path, []byte("not a config at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 4; n++ {
		if err := sup.LoadConfigs(); err != nil {
			t.Fatal(err)
		}
	}
	if n := countEvents(sup.ev, logs.Error, "config invalid"); n != 1 {
		t.Fatalf("broken config logged %d times, want 1", n)
	}

	// a new revision must be reported again
	if err := os.WriteFile(path, []byte("still not a config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sup.LoadConfigs(); err != nil {
		t.Fatal(err)
	}
	if n := countEvents(sup.ev, logs.Error, "config invalid"); n != 2 {
		t.Fatalf("a new broken revision should be reported again, got %d", n)
	}
}
