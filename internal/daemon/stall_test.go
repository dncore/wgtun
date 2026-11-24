package daemon

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dncore/wgtun/internal/logs"
	"github.com/dncore/wgtun/internal/paths"
	"github.com/dncore/wgtun/internal/uapi"
	"github.com/dncore/wgtun/internal/wgconf"
)

// Test keys are generated per run; only their byte shape matters here.
var (
	stallPeerKey    = testKey()
	stallSiblingKey = testKey()
	stallDeviceKey  = testKey()
)

func testKey() string {
	k, err := wgconf.GeneratePrivateKey()
	if err != nil {
		panic(err)
	}
	return k
}

// stallInst builds an instance with one peer and one live device status.
func stallInst(keepalive int, startedAgo time.Duration) *inst {
	return &inst{
		name:      "wg0",
		conf:      &wgconf.Config{Peers: []wgconf.Peer{{PublicKey: stallPeerKey, PersistentKeepalive: keepalive}}},
		startedAt: time.Now().Add(-startedAgo),
	}
}

func devStatus(lastHandshake time.Time, tx uint64) *uapi.DeviceStatus {
	return &uapi.DeviceStatus{Peers: []uapi.PeerStatus{{
		PublicKey:     stallPeerKey,
		Endpoint:      "203.0.113.9:51820",
		LastHandshake: lastHandshake,
		TxBytes:       tx,
	}}}
}

// TestStalledPeer pins the detection rule: keepalive-enabled peers only, with
// an endpoint, a scaled window, and a grace period before "never handshaked"
// counts.
func TestStalledPeer(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name       string
		keepalive  int
		startedAgo time.Duration
		status     *uapi.DeviceStatus
		wantStall  bool
		wantWindow time.Duration
	}{
		{
			name: "stale handshake with keepalive", keepalive: 25, startedAgo: time.Hour,
			status:    devStatus(now.Add(-10*time.Minute), 4096),
			wantStall: true, wantWindow: handshakeStallAfter,
		},
		{
			name: "healthy handshake", keepalive: 25, startedAgo: time.Hour,
			status: devStatus(now.Add(-time.Minute), 4096),
		},
		{
			name: "no keepalive never stalls", keepalive: 0, startedAgo: time.Hour,
			status: devStatus(now.Add(-24*time.Hour), 4096),
		},
		{
			name: "long keepalive scales the window", keepalive: 300, startedAgo: time.Hour,
			// 10 minutes stale is normal when the peer only asks every 5m
			status: devStatus(now.Add(-10*time.Minute), 4096),
		},
		{
			name: "long keepalive past its scaled window", keepalive: 300, startedAgo: time.Hour,
			status:    devStatus(now.Add(-25*time.Minute), 4096),
			wantStall: true, wantWindow: 20 * time.Minute,
		},
		{
			name: "never handshaked inside grace", keepalive: 25, startedAgo: time.Minute,
			status: devStatus(time.Time{}, 148),
		},
		{
			name: "old handshake but just restarted", keepalive: 25, startedAgo: time.Minute,
			status: devStatus(now.Add(-time.Hour), 4096),
		},
		{
			name: "never handshaked, transmitting", keepalive: 25, startedAgo: 10 * time.Minute,
			status:    devStatus(time.Time{}, 148*3),
			wantStall: true, wantWindow: handshakeStallAfter,
		},
		{
			name: "never handshaked, not yet transmitting", keepalive: 25, startedAgo: 10 * time.Minute,
			status: devStatus(time.Time{}, 0),
		},
		{
			name: "no endpoint means wgtun has not handed it a destination", keepalive: 25, startedAgo: time.Hour,
			status: &uapi.DeviceStatus{Peers: []uapi.PeerStatus{{
				PublicKey: stallPeerKey, LastHandshake: now.Add(-time.Hour),
			}}},
		},
		{
			name: "peer absent from the device", keepalive: 25, startedAgo: time.Hour,
			status: &uapi.DeviceStatus{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := stallInst(tc.keepalive, tc.startedAgo)
			live, p, window := i.stalledPeer(tc.status, now)
			if got := live != nil; got != tc.wantStall {
				t.Fatalf("stalled = %v want %v (peer %v)", got, tc.wantStall, p)
			}
			if tc.wantStall && window != tc.wantWindow {
				t.Errorf("window = %s want %s", window, tc.wantWindow)
			}
		})
	}
}

func TestStallBackoff(t *testing.T) {
	for _, tc := range []struct {
		tries int
		want  time.Duration
	}{
		{1, 5 * time.Minute}, {2, 10 * time.Minute}, {3, 20 * time.Minute},
		{4, 40 * time.Minute}, {5, time.Hour}, {9, time.Hour},
	} {
		if got := stallBackoff(tc.tries); got != tc.want {
			t.Errorf("stallBackoff(%d) = %s want %s", tc.tries, got, tc.want)
		}
	}
}

// TestStallRestartDue covers the whole policy: a stale keepalive peer is
// restarted, the backoff suppresses the next attempt, a later pass restarts
// again, and evidence of a working receive path (a sibling's fresh handshake,
// or this peer coming back) clears the budget.
func TestStallRestartDue(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	i := stallInst(25, time.Hour)
	st := devStatus(time.Now().Add(-10*time.Minute), 4096)
	t0 := time.Now()

	if !sup.stallRestartDue(i, st, t0) {
		t.Fatal("a peer stale for 10m with keepalive=25 must be restarted")
	}
	if i.stallRestarts != 1 || !i.stallNext.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("after first restart: tries=%d next=%s", i.stallRestarts, i.stallNext)
	}
	if !hasEvent(sup.ev, logs.Error, "wedged receive path") {
		t.Error("the restart must be reported at error level")
	}
	if sup.stallRestartDue(i, st, t0.Add(time.Minute)) {
		t.Error("the backoff must suppress a restart one minute later")
	}
	if !sup.stallRestartDue(i, st, t0.Add(5*time.Minute)) {
		t.Error("the next restart must be allowed once the backoff elapses")
	}
	if i.stallNext.Sub(t0) != 15*time.Minute {
		t.Errorf("second backoff ends at %s, want t0+15m", i.stallNext.Sub(t0))
	}

	// The peer handshakes again (the restart worked): counters reset.
	after := t0.Add(6 * time.Minute)
	healthy := devStatus(after.Add(-time.Minute), 4096)
	if sup.stallRestartDue(i, healthy, after) {
		t.Fatal("a healthy handshake must not restart anything")
	}
	if i.stallRestarts != 0 || !i.stallNext.IsZero() {
		t.Fatalf("health must clear the budget, got tries=%d next=%s", i.stallRestarts, i.stallNext)
	}
	if !sup.stallRestartDue(i, st, t0.Add(7*time.Minute)) {
		t.Error("a fresh stall after recovery restarts immediately again")
	}
}

// A sibling peer with a fresh handshake proves the process is still receiving,
// so the stalled peer is the far end's problem and must not cost the healthy
// peer its session.
func TestStallRestartDueSparesDeviceWithLiveSibling(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	i := stallInst(25, time.Hour)
	now := time.Now()
	st := devStatus(now.Add(-10*time.Minute), 4096)
	st.Peers = append(st.Peers, uapi.PeerStatus{
		PublicKey:     stallSiblingKey,
		Endpoint:      "203.0.113.10:51820",
		LastHandshake: now.Add(-30 * time.Second),
		TxBytes:       1024,
	})
	if sup.stallRestartDue(i, st, now) {
		t.Fatal("a device with a recently handshaked peer must not be restarted")
	}
	if i.stallRestarts != 0 || !i.stallNext.IsZero() {
		t.Errorf("no restart means no budget consumed, got tries=%d next=%s", i.stallRestarts, i.stallNext)
	}
}

// An explicit start must not leave an old stall backoff in place: the user
// asked for a fresh attempt. The start itself is made to fail (no binary), so
// the test never touches a real device.
func TestStartClearsStallBackoff(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	writeConf(t, "wg0")
	if err := sup.LoadConfigs(); err != nil {
		t.Fatal(err)
	}
	oldBin := paths.WireGuardGo
	paths.WireGuardGo = filepath.Join(t.TempDir(), "no-such-wireguard-go")
	t.Cleanup(func() { paths.WireGuardGo = oldBin })

	i := sup.insts["wg0"]
	i.stallRestarts = 3
	i.stallNext = time.Now().Add(time.Hour)
	if err := sup.Start("wg0"); err == nil {
		t.Fatal("start should have failed without a wireguard-go binary")
	}
	if i.stallRestarts != 0 || !i.stallNext.IsZero() {
		t.Fatalf("explicit start must clear the stall budget, got tries=%d next=%s", i.stallRestarts, i.stallNext)
	}
}

// Guard against the config test helper drifting from the fields the policy
// reads: a parsed config must produce the peer key the status matches on.
func TestStalledPeerMatchesParsedConfig(t *testing.T) {
	conf, err := wgconf.Parse(strings.NewReader(
		"[Interface]\nPrivateKey = " + stallDeviceKey + "\nAddress = 192.0.2.2/32\n\n" +
			"[Peer]\nPublicKey = " + stallPeerKey + "\nAllowedIPs = 192.0.2.0/24\nPersistentKeepalive = 25\n"))
	if err != nil {
		t.Fatal(err)
	}
	i := &inst{name: "wg0", conf: conf, startedAt: time.Now().Add(-time.Hour)}
	live, _, _ := i.stalledPeer(devStatus(time.Now().Add(-time.Hour), 4096), time.Now())
	if live == nil {
		t.Fatal("a parsed keepalive peer must be detected as stalled")
	}
}
