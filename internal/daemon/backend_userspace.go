package daemon

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dncore/wgtun/internal/paths"
	"github.com/dncore/wgtun/internal/uapi"
)

// startUserspaceLocked launches wireguard-go and waits until its tun device
// and UAPI socket are usable.
//
// -f is deliberate: without it wireguard-go daemonizes itself (on Linux too),
// the launcher exits, and the real process is reparented away from us — the
// pid in the pid file would be a corpse and stderr logging would go to
// /dev/null. In the foreground the child is the device process, so the
// recorded pid and the captured log lines are both authoritative. It still
// writes WG_TUN_NAME_FILE on darwin, where the utun name is kernel-assigned.
func (s *Supervisor) startUserspaceLocked(i *inst) error {
	dir := i.runtimeDir()
	if err := os.MkdirAll(paths.WireGuardSockDir, 0o755); err != nil {
		return fmt.Errorf("mkdir wireguard socket dir: %v", err)
	}
	want := userspaceIfName(i.name)
	// A kernel interface left behind by a backend switch would make the TUN
	// creation fail with "device exists". Nothing is running for this instance
	// (reconcile only calls start when it is not), so removing it is safe.
	if want != "utun" {
		removeTunLink(want)
	}
	cmd := exec.Command(paths.WireGuardGo, "-f", want)
	cmd.Env = append(os.Environ(),
		"WG_TUN_NAME_FILE="+filepath.Join(dir, "tun.name"),
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		i.lastErr = err.Error()
		return fmt.Errorf("start wireguard-go: %v", err)
	}
	// With -f this is the device process. Record it immediately so a timeout
	// while waiting for the tun/socket still leaves cleanup able to kill it.
	i.pid = cmd.Process.Pid
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	go s.pumpWireguardLogs(i.name, stderr)

	tun := want
	if want == "utun" {
		tun, err = waitTunName(dir, 5*time.Second)
		if err != nil {
			return withExit(waitCh, err)
		}
	} else if err := waitInterface(tun, 5*time.Second); err != nil {
		return withExit(waitCh, err)
	}
	i.tun = tun
	if err := waitFile(i.uapiPath(), 5*time.Second); err != nil {
		return withExit(waitCh, fmt.Errorf("uapi socket: %v", err))
	}
	pid, err := waitSocketPid(i.uapiPath(), 3*time.Second)
	if err != nil {
		return withExit(waitCh, err)
	}
	i.pid = pid
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(i.pid)), 0o644); err != nil {
		s.ev.Warn(i.name, "write pid file: %v", err)
	}
	return nil
}

// teardownUserspaceLocked kills the device process (when it is really ours)
// and removes its UAPI socket. The runtime dir is removed by cleanupLocked.
func (s *Supervisor) teardownUserspaceLocked(i *inst) {
	// trust the socket holder over the recorded pid when possible
	if i.tun != "" {
		if p := socketPid(i.uapiPath()); p > 0 {
			i.pid = p
		}
	}
	if i.pid != 0 && pidAlive(i.pid) && i.ownsProcess() {
		syscall.Kill(i.pid, syscall.SIGTERM)
		deadline := time.Now().Add(3 * time.Second)
		for pidAlive(i.pid) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if pidAlive(i.pid) {
			syscall.Kill(i.pid, syscall.SIGKILL)
		}
	}
	if i.tun != "" {
		os.Remove(i.uapiPath())
	}
}

// adoptUserspaceLocked re-attaches to a device process that outlived its
// daemon (launchd AbandonProcessGroup / systemd KillMode=process).
func (s *Supervisor) adoptUserspaceLocked(i *inst) bool {
	dir := i.runtimeDir()
	tun, err := os.ReadFile(filepath.Join(dir, "tun.name"))
	if err != nil {
		os.RemoveAll(dir)
		return false
	}
	i.tun = strings.TrimSpace(string(tun))
	pid := readPidFile(filepath.Join(dir, "pid"))
	if pid <= 0 || !pidAlive(pid) {
		// the recorded pid may be an exited launcher; trust the socket
		pid = socketPid(i.uapiPath())
	}
	if pid <= 0 {
		// No process: if a kernel interface from a previous backend remains,
		// remove it before the next start tries to create the same name.
		removeTunLink(i.tun)
		os.RemoveAll(dir)
		i.tun = ""
		return false
	}
	i.pid = pid
	st, err := uapi.Get(i.uapiPath())
	if err != nil {
		// process alive but backend wedged — clean and let reconcile restart
		s.ev.Warn(i.name, "adopted pid %d but UAPI dead, cleaning", pid)
		s.cleanupLocked(i)
		return false
	}
	// Re-establish endpoint host routes the previous daemon may have had:
	// they live in the kernel routing table and a full-tunnel config needs
	// them as soon as the tunnel is up again.
	for _, p := range st.Peers {
		if p.Endpoint != "" {
			s.pinEndpointFor(i, p.Endpoint)
		}
	}
	i.adopted = true
	i.active = true
	i.startedAt = time.Now() // best effort
	s.ev.Info(i.name, "adopted running instance on %s (pid %d)", i.tun, i.pid)
	return true
}

// withExit replaces a wait timeout with the child's real exit status when the
// process died instead of just being slow — "operation not permitted" from
// wireguard-go is far more useful than "timeout waiting for tun.name".
func withExit(waitCh <-chan error, waitErr error) error {
	select {
	case err := <-waitCh:
		if err == nil {
			return fmt.Errorf("wireguard-go exited before the device was ready (exit status 0); its log lines are in the event log")
		}
		return fmt.Errorf("wireguard-go exited before the device was ready (%v); its log lines are in the event log", err)
	default:
		return waitErr
	}
}

func waitTunName(dir string, timeout time.Duration) (string, error) {
	path := filepath.Join(dir, "tun.name")
	if err := waitFile(path, timeout); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(data))
	if name == "" {
		return "", fmt.Errorf("empty tun name")
	}
	return name, nil
}

func waitFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", path)
}

// waitInterface waits for a kernel-visible interface to appear.
func waitInterface(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := net.InterfaceByName(name); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for interface %s", name)
}

// waitSocketPid polls until the socket has an owner.
func waitSocketPid(sockPath string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pid := socketPid(sockPath); pid > 0 {
			return pid, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, fmt.Errorf("no process owns %s", sockPath)
}
