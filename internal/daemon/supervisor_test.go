package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dncore/wg-service/internal/logs"
	"github.com/dncore/wg-service/internal/paths"
	"github.com/dncore/wg-service/internal/state"
	"github.com/dncore/wg-service/internal/wgconf"
)

// newTestSupervisor points the global paths at temp dirs and returns a
// supervisor with an empty state/logs store.
func newTestSupervisor(t *testing.T) (*Supervisor, *state.Store) {
	t.Helper()
	confDir := t.TempDir()
	oldConf, oldRun := paths.ConfDir, paths.RunDir
	paths.ConfDir = confDir
	paths.RunDir = t.TempDir()
	t.Cleanup(func() { paths.ConfDir, paths.RunDir = oldConf, oldRun })

	st, err := state.Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ev, err := logs.New(filepath.Join(t.TempDir(), "events.jsonl"), 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return New(st, ev), st
}

// writeConf writes a valid config into the test config dir.
func writeConf(t *testing.T, name string) {
	t.Helper()
	priv, _ := wgconf.GeneratePrivateKey()
	pub, _ := wgconf.GeneratePrivateKey() // any valid key works as a peer key
	content := "[Interface]\nPrivateKey = " + priv + "\nAddress = 203.0.113.2/24\n\n" +
		"[Peer]\nPublicKey = " + pub + "\nAllowedIPs = 203.0.113.0/24\n"
	if err := os.WriteFile(filepath.Join(paths.ConfDir, name+".conf"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFirstImportEnablesAndDesiresRun(t *testing.T) {
	sup, st := newTestSupervisor(t)
	writeConf(t, "wg0")
	if err := sup.LoadConfigs(); err != nil {
		t.Fatal(err)
	}
	got := st.Get("wg0")
	if !got.Enabled || !got.DesiredRun {
		t.Fatalf("first import should be enabled+desired, got %+v", got)
	}
}

func TestExistingStateIsNotOverwritten(t *testing.T) {
	sup, st := newTestSupervisor(t)
	writeConf(t, "wg0")
	// user had explicitly disabled this instance before it appeared again
	st.Set("wg0", state.Instance{Enabled: false, DesiredRun: false})
	if err := sup.LoadConfigs(); err != nil {
		t.Fatal(err)
	}
	got := st.Get("wg0")
	if got.Enabled || got.DesiredRun {
		t.Fatalf("existing state must be respected, got %+v", got)
	}
}

func TestRemovedConfigDropsInstance(t *testing.T) {
	sup, st := newTestSupervisor(t)
	writeConf(t, "wg0")
	sup.LoadConfigs()
	if len(sup.Views()) != 1 {
		t.Fatalf("want 1 instance, got %d", len(sup.Views()))
	}
	os.Remove(filepath.Join(paths.ConfDir, "wg0.conf"))
	if err := sup.LoadConfigs(); err != nil {
		t.Fatal(err)
	}
	if len(sup.Views()) != 0 {
		t.Fatalf("removed config should drop the instance, got %v", sup.Views())
	}
	// state record survives for a config file that comes back
	if !st.Has("wg0") {
		t.Fatal("state record should survive config removal")
	}
}

func TestUAPIPathUsesWireGuardSockDir(t *testing.T) {
	old := paths.WireGuardSockDir
	paths.WireGuardSockDir = "/tmp/wgs-test-sockdir"
	defer func() { paths.WireGuardSockDir = old }()
	i := &inst{name: "wg0", tun: "utun9"}
	if got, want := i.uapiPath(), "/tmp/wgs-test-sockdir/utun9.sock"; got != want {
		t.Fatalf("uapiPath = %q want %q", got, want)
	}
}

func TestCreateInstanceStartsButNoAutostart(t *testing.T) {
	sup, st := newTestSupervisor(t)
	priv, _ := wgconf.GeneratePrivateKey()
	pub, _ := wgconf.GeneratePrivateKey()
	content := "[Interface]\nPrivateKey = " + priv + "\nAddress = 203.0.113.3/24\n\n" +
		"[Peer]\nPublicKey = " + pub + "\nAllowedIPs = 203.0.113.0/24\n"
	if err := sup.CreateInstance("wg7", content, false); err != nil {
		t.Fatal(err)
	}
	got := st.Get("wg7")
	if got.Enabled {
		t.Fatalf("new instance must not get boot autostart implicitly: %+v", got)
	}
	if !got.DesiredRun {
		t.Fatalf("new instance should be desired-running once: %+v", got)
	}
}
