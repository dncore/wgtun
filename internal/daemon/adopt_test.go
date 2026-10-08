package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dncore/wgtun/internal/logs"
	"github.com/dncore/wgtun/internal/paths"
)

// adoptFixture lays out an instance left behind by a previous daemon: config
// file, runtime dir (tun name + pid) and a UAPI socket whose holder answers.
func adoptFixture(t *testing.T, pidFile string) (*Supervisor, string) {
	t.Helper()
	sup, _ := newTestSupervisor(t)
	writeConf(t, "wg1")
	if err := sup.LoadConfigs(); err != nil {
		t.Fatal(err)
	}
	sockDir, err := os.MkdirTemp("/tmp", "wgtun-adopt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	oldSock := paths.WireGuardSockDir
	paths.WireGuardSockDir = sockDir
	t.Cleanup(func() { paths.WireGuardSockDir = oldSock })

	dir := filepath.Join(paths.RunDir, "wg1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tun.name"), []byte("utun9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(pidFile), 0o644); err != nil {
		t.Fatal(err)
	}
	return sup, sockDir
}

const adoptGetReply = "errno=0\n" +
	"listen_port=51820\n" +
	"public_key=a1a2a3a4a5a6a7a8a9aab1b2b3b4b5b6b7b8b9bac1c2c3c4c5c6c7c8c9cad0d1\n" +
	"endpoint=198.51.100.7:51820\n" +
	"allowed_ip=198.51.100.0/24\n\n"

// TestAdoptTakesOverRunningInstance is the regression guard for the path a
// launchctl restart is supposed to take: with AbandonProcessGroup in the plist
// the wireguard-go children outlive the old daemon, and the new one must take
// them over (pid + UAPI liveness) instead of recreating the tunnels. In
// production this never ran — the children were killed by launchd first.
func TestAdoptTakesOverRunningInstance(t *testing.T) {
	sup, sockDir := adoptFixture(t, strconv.Itoa(os.Getpid())+"\n")
	fakeUAPI(t, filepath.Join(sockDir, "utun9.sock"), adoptGetReply)

	sup.Adopt()

	i := sup.insts["wg1"]
	if i == nil {
		t.Fatal("instance lost")
	}
	if i.tun != "utun9" || i.pid <= 0 {
		t.Fatalf("not adopted: tun=%q pid=%d", i.tun, i.pid)
	}
	if !i.adopted {
		t.Error("adopted flag not set")
	}
	if !i.running() {
		t.Error("adopted instance must count as running")
	}
	if !hasEvent(sup.ev, logs.Info, "adopted running instance on utun9") {
		t.Error("adoption should be logged")
	}
	// the runtime dir must survive adoption (it is the handle for the next one)
	if _, err := os.Stat(filepath.Join(paths.RunDir, "wg1", "tun.name")); err != nil {
		t.Errorf("runtime dir lost: %v", err)
	}
}

// On darwin wireguard-go forks, so the pid in the file is often the exited
// launcher: the socket holder is authoritative.
func TestAdoptFallsBackToSocketHolder(t *testing.T) {
	deadPid := 99999
	if pidAlive(deadPid) {
		t.Skipf("pid %d is in use, cannot test the dead-pid path", deadPid)
	}
	sup, sockDir := adoptFixture(t, strconv.Itoa(deadPid)+"\n")
	fakeUAPI(t, filepath.Join(sockDir, "utun9.sock"), adoptGetReply)

	sup.Adopt()

	i := sup.insts["wg1"]
	if i.pid == deadPid || i.pid <= 0 || !i.adopted {
		t.Fatalf("should have adopted the socket holder, got pid=%d adopted=%v", i.pid, i.adopted)
	}
}

// A runtime dir with nothing alive behind it is debris: drop it and let the
// reconcile loop start the instance fresh.
func TestAdoptDropsStaleRuntimeDir(t *testing.T) {
	deadPid := 99999
	if pidAlive(deadPid) {
		t.Skip("pid in use")
	}
	sup, _ := adoptFixture(t, strconv.Itoa(deadPid)+"\n") // no socket at all

	sup.Adopt()

	i := sup.insts["wg1"]
	if i.running() || i.tun != "" || i.adopted {
		t.Fatalf("stale dir must not be adopted: %+v", i)
	}
	if _, err := os.Stat(filepath.Join(paths.RunDir, "wg1")); !os.IsNotExist(err) {
		t.Errorf("stale runtime dir should be removed, stat err = %v", err)
	}
}

// A live process whose UAPI socket does not answer is wedged: adopt must not
// pretend it is healthy. And a pid file alone must never be enough to signal a
// process — if that pid was recycled it belongs to something unrelated, while
// the daemon runs as root. This test wrote the *test's own* pid into the file
// originally and the daemon dutifully SIGTERMed it.
func TestAdoptRejectsWedgedUapiWithoutKillingStrangers(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	sup, _ := adoptFixture(t, strconv.Itoa(cmd.Process.Pid)+"\n") // no socket → uapi.Get fails

	sup.Adopt()

	i := sup.insts["wg1"]
	if i.adopted || i.running() {
		t.Fatalf("wedged instance must not be adopted: %+v", i)
	}
	if !hasEvent(sup.ev, logs.Warn, "UAPI dead, cleaning") {
		t.Error("a wedged instance should warn and be cleaned")
	}
	// pidAlive() keeps reporting a zombie as alive, so the evidence that has to
	// hold is: the process was NOT terminated (it never becomes waitable here).
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Errorf("cleanup terminated a process that is not wireguard-go: %v", err)
	case <-time.After(time.Second):
	}
}

// ...but a pid that *is* wireguard-go must still be reaped, otherwise a wedged
// instance could never be restarted.
func TestCleanupKillsOwnWireGuardGo(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "wireguard-go")
	if err := os.Symlink("/bin/sleep", fake); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(fake, "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot exec a wireguard-go-named probe: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	if !isWireGuardGo(cmd.Process.Pid) {
		t.Skipf("ps reports %q, which this host does not match as wireguard-go", probeComm(t, cmd.Process.Pid))
	}

	sup, _ := newTestSupervisor(t)
	i := &inst{name: "wg1", tun: "utun9", pid: cmd.Process.Pid}
	sup.cleanupLocked(i)

	// reap in the background: pidAlive() keeps reporting a zombie as alive, so
	// the process actually being waitable is the evidence that it was killed
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("a wireguard-go process must be reaped")
	}
}

func probeComm(t *testing.T, pid int) string {
	t.Helper()
	out, _ := exec.Command("/bin/ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(out))
}
