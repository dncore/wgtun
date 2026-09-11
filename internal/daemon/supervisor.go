// wireguard-go subprocess supervision: start/stop, config push over UAPI,
// network setup, crash adoption and restart backoff.
package daemon

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dncore/wg-service/internal/logs"
	"github.com/dncore/wg-service/internal/paths"
	"github.com/dncore/wg-service/internal/state"
	"github.com/dncore/wg-service/internal/uapi"
	"github.com/dncore/wg-service/internal/wgconf"
	"github.com/dncore/wg-service/internal/wire"
)

// inst is the runtime view of one configured instance.
type inst struct {
	name     string
	confPath string
	conf     *wgconf.Config

	pid      int
	tun      string // utunN
	adopted  bool   // true when the process was started by a previous daemon
	startedAt time.Time

	restarts    []time.Time // restart attempts for backoff
	nextAttempt time.Time
	failStreak  int
	lastErr     string

	// cached live status, refreshed by the reconcile probe
	status    *uapi.DeviceStatus
	statusAt  time.Time
}

func (i *inst) running() bool { return i.pid != 0 }

// Supervisor owns all instances. All public methods are safe concurrently;
// lifecycle operations are serialized.
type Supervisor struct {
	mu    sync.Mutex // guards insts and lifecycle serialization
	insts map[string]*inst

	st *state.Store
	ev *logs.Store
}

// New creates a supervisor.
func New(st *state.Store, ev *logs.Store) *Supervisor {
	return &Supervisor{
		insts: map[string]*inst{},
		st:    st,
		ev:    ev,
	}
}

// LoadConfigs scans the config dir and (re)loads every *.conf.
// Unparseable files are logged and skipped, never fatal.
func (s *Supervisor) LoadConfigs() error {
	if err := os.MkdirAll(paths.ConfDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(paths.ConfDir)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".conf")
		seen[name] = true
		path := filepath.Join(paths.ConfDir, e.Name())
		conf, err := loadConfFile(path)
		if err != nil {
			s.ev.Error(name, "config invalid, skipped: %v", err)
			if old, ok := s.insts[name]; ok {
				old.lastErr = fmt.Sprintf("config invalid: %v", err)
			}
			continue
		}
		cur, ok := s.insts[name]
		if !ok {
			s.insts[name] = &inst{name: name, confPath: path, conf: conf}
			continue
		}
		cur.confPath, cur.conf = path, conf
	}
	for name := range s.insts {
		if !seen[name] {
			delete(s.insts, name)
		}
	}
	return nil
}

func loadConfFile(path string) (*wgconf.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	conf, err := wgconf.Parse(strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	if err := conf.Validate(); err != nil {
		return nil, err
	}
	return conf, nil
}

// BootInit applies boot semantics. freshBoot is true when the daemon starts
// at system boot (or after a crash): every enabled instance that is not
// currently running gets DesiredRun=true. On a mere daemon restart
// (clean shutdown marker seen) persisted desired state is respected, so an
// instance the user stopped stays stopped.
func (s *Supervisor) BootInit(freshBoot bool) {
	if !freshBoot {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, i := range s.insts {
		st := s.st.Get(name)
		if st.Enabled && !st.DesiredRun && !i.running() {
			s.st.Set(name, state.Instance{Enabled: true, DesiredRun: true})
			s.ev.Info(name, "autostart enabled at boot")
		}
	}
}

// Reconcile drives actual state toward desired state and refreshes cached
// statuses. It is the periodic heartbeat and the only place that restarts.
func (s *Supervisor) Reconcile() {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.insts))
	for n := range s.insts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		i := s.insts[name]
		st := s.st.Get(name)
		switch {
		case st.DesiredRun && !i.running():
			s.tryStartLocked(i)
		case !st.DesiredRun && i.running():
			s.stopLocked(i, "desired stopped")
		case i.running():
			s.probeLocked(i)
		}
	}
}

// probeLocked checks liveness and caches status. A wedged backend (dead pid
// or unresponsive UAPI) triggers a full cleanup + restart with backoff.
func (s *Supervisor) probeLocked(i *inst) {
	alive := pidAlive(i.pid)
	var st *uapi.DeviceStatus
	var err error
	if alive {
		st, err = uapi.Get(i.uapiPath())
	}
	if alive && err == nil {
		i.status, i.statusAt = st, time.Now()
		i.failStreak = 0
		s.maybeFillEndpointsLocked(i, st)
		return
	}
	i.failStreak++
	reason := "process dead"
	if alive {
		reason = fmt.Sprintf("uapi unresponsive: %v", err)
	}
	s.ev.Error(i.name, "instance unhealthy (%s), cleaning up for restart", reason)
	s.cleanupLocked(i)
	s.tryStartLocked(i)
}

// maybeFillEndpointsLocked re-resolves DNS for peers whose live endpoint is
// empty (e.g. DNS was not ready at start) and re-pushes the config when it
// succeeds. This is the "wake up before network" self-heal.
func (s *Supervisor) maybeFillEndpointsLocked(i *inst, st *uapi.DeviceStatus) {
	need := false
	for _, p := range i.conf.Peers {
		if p.Endpoint == "" {
			continue
		}
		live := findLivePeer(st, p.PublicKey)
		if live == nil || live.Endpoint == "" {
			need = true
			break
		}
	}
	if !need {
		return
	}
	if err := s.pushConfLocked(i); err != nil {
		s.ev.Warn(i.name, "endpoint re-resolve push failed: %v", err)
	} else {
		s.ev.Info(i.name, "endpoint resolved and config re-pushed")
	}
}

func findLivePeer(st *uapi.DeviceStatus, pubB64 string) *uapi.PeerStatus {
	for idx := range st.Peers {
		if st.Peers[idx].PublicKey == pubB64 {
			return &st.Peers[idx]
		}
	}
	return nil
}

// tryStartLocked respects restart backoff: after 5 failed starts in the
// last 10 minutes it waits 5 minutes between attempts.
func (s *Supervisor) tryStartLocked(i *inst) {
	now := time.Now()
	if now.Before(i.nextAttempt) {
		return
	}
	if err := s.startLocked(i); err != nil {
		i.failStreak++
		i.restarts = append(i.restarts, now)
		cut := now.Add(-10 * time.Minute)
		recent := 0
		for _, t := range i.restarts {
			if t.After(cut) {
				recent++
			}
		}
		if recent >= 5 {
			i.nextAttempt = now.Add(5 * time.Minute)
			s.ev.Error(i.name, "5 failed starts in 10m; backing off until %s", i.nextAttempt.Format(time.Kitchen))
		}
	}
}

// Start brings an instance up (explicit user action).
func (s *Supervisor) Start(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.insts[name]
	if !ok {
		return fmt.Errorf("unknown instance %q", name)
	}
	if i.running() {
		s.stopLocked(i, "restart")
	}
	i.nextAttempt = time.Time{} // explicit user action clears backoff
	err := s.startLocked(i)
	if err == nil {
		return s.st.Update(name, func(in *state.Instance) { in.DesiredRun = true })
	}
	return err
}

// Stop tears an instance down (explicit user action).
func (s *Supervisor) Stop(name string) error {
	s.mu.Lock()
	i, ok := s.insts[name]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown instance %q", name)
	}
	if i.running() {
		s.stopLocked(i, "user stop")
	}
	s.mu.Unlock()
	return s.st.Update(name, func(in *state.Instance) { in.DesiredRun = false })
}

// Restart stops then starts, clearing backoff.
func (s *Supervisor) Restart(name string) error {
	if err := s.Stop(name); err != nil {
		return err
	}
	return s.Start(name)
}

// SetEnabled toggles boot autostart for an instance.
func (s *Supervisor) SetEnabled(name string, enabled bool) error {
	if _, ok := s.insts[name]; !ok {
		return fmt.Errorf("unknown instance %q", name)
	}
	return s.st.Update(name, func(in *state.Instance) { in.Enabled = enabled })
}

// startLocked launches wireguard-go, pushes config, sets addresses/routes.
func (s *Supervisor) startLocked(i *inst) error {
	started := time.Now()
	dir := i.runtimeDir()
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clean runtime dir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir runtime dir: %v", err)
	}
	for _, pre := range i.conf.Interface.PostUp {
		if err := runHook(i.name, pre); err != nil {
			s.ev.Warn(i.name, "PostUp failed: %v", err)
		}
	}
	cmd := exec.Command(paths.WireGuardGo, "utun")
	cmd.Env = append(os.Environ(),
		"WG_UAPI_DIR="+dir,
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
	i.pid = cmd.Process.Pid
	i.adopted = false
	i.startedAt = started
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(i.pid)), 0o644); err != nil {
		s.ev.Warn(i.name, "write pid file: %v", err)
	}
	go s.pumpWireguardLogs(i.name, stderr)
	go func() {
		if err := cmd.Wait(); err != nil {
			s.ev.Info(i.name, "wireguard-go exited: %v", err)
		}
	}()

	// wait for the tun name and UAPI socket
	tun, err := waitTunName(dir, 5*time.Second)
	if err != nil {
		s.cleanupLocked(i)
		i.lastErr = err.Error()
		return err
	}
	i.tun = tun
	if err := waitFile(i.uapiPath(), 5*time.Second); err != nil {
		s.cleanupLocked(i)
		i.lastErr = err.Error()
		return fmt.Errorf("uapi socket: %v", err)
	}
	if err := s.pushConfLocked(i); err != nil {
		s.cleanupLocked(i)
		i.lastErr = err.Error()
		return err
	}
	if err := netsetup(i.tun, i.conf); err != nil {
		s.cleanupLocked(i)
		i.lastErr = err.Error()
		return fmt.Errorf("network setup: %v", err)
	}
	i.lastErr = ""
	s.ev.Info(i.name, "started on %s (pid %d)", i.tun, i.pid)
	return nil
}

// pushConfLocked writes the parsed config to the device over UAPI,
// resolving peer endpoint DNS first.
func (s *Supervisor) pushConfLocked(i *inst) error {
	req := uapi.SetRequest{
		PrivateKey: i.conf.Interface.PrivateKey,
		ListenPort: i.conf.Interface.ListenPort,
	}
	for _, p := range i.conf.Peers {
		sp := uapi.SetPeer{
			PublicKey:           p.PublicKey,
			PresharedKey:        p.PresharedKey,
			PersistentKeepalive: p.PersistentKeepalive,
			AllowedIPs:          prefixesToStrings(p.AllowedIPs),
		}
		if p.Endpoint != "" {
			resolved, err := resolveEndpoint(p.Endpoint)
			if err != nil {
				s.ev.Warn(i.name, "endpoint %s unresolved (will retry): %v", p.Endpoint, err)
			} else {
				sp.Endpoint = resolved
			}
		}
		req.Peers = append(req.Peers, sp)
	}
	return uapi.Set(i.uapiPath(), req)
}

// stopLocked tears down: hooks, process, runtime dir. Routes and addresses
// disappear with the utun interface.
func (s *Supervisor) stopLocked(i *inst, reason string) {
	for _, hook := range i.conf.Interface.PostDown {
		if err := runHook(i.name, hook); err != nil {
			s.ev.Warn(i.name, "PostDown failed: %v", err)
		}
	}
	s.cleanupLocked(i)
	s.ev.Info(i.name, "stopped (%s)", reason)
}

// cleanupLocked kills the process (if alive) and removes the runtime dir.
func (s *Supervisor) cleanupLocked(i *inst) {
	if i.pid != 0 && pidAlive(i.pid) {
		syscall.Kill(i.pid, syscall.SIGTERM)
		deadline := time.Now().Add(3 * time.Second)
		for pidAlive(i.pid) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if pidAlive(i.pid) {
			syscall.Kill(i.pid, syscall.SIGKILL)
		}
	}
	os.RemoveAll(i.runtimeDir())
	i.pid, i.tun, i.status = 0, "", nil
	i.adopted = false
}

// pumpWireguardLogs forwards wireguard-go stderr lines into the event log.
func (s *Supervisor) pumpWireguardLogs(name string, r io.Reader) {
	// PostUp/PostDown hooks and wireguard-go log lines flow through here;
	// both are authored by the machine admin, not untrusted input.
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line != "" {
			s.ev.Info(name, "wireguard-go: %s", line)
		}
	}
}

// Adopt discovers instances still running from a previous daemon life and
// re-adopts them (pid + UAPI responsiveness).
func (s *Supervisor) Adopt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(paths.RunDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		i, ok := s.insts[name]
		if !ok {
			// unknown instance: clean up stale runtime dir
			os.RemoveAll(filepath.Join(paths.RunDir, name))
			s.ev.Warn(name, "removed stale runtime dir (no config)")
			continue
		}
		dir := i.runtimeDir()
		data, err := os.ReadFile(filepath.Join(dir, "pid"))
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || !pidAlive(pid) {
			os.RemoveAll(dir)
			continue
		}
		tun, err := os.ReadFile(filepath.Join(dir, "tun.name"))
		if err != nil {
			os.RemoveAll(dir)
			continue
		}
		i.pid = pid
		i.tun = strings.TrimSpace(string(tun))
		if _, err := uapi.Get(i.uapiPath()); err != nil {
			// process alive but backend wedged — clean and let reconcile restart
			s.ev.Warn(name, "adopted pid %d but UAPI dead, cleaning", pid)
			s.cleanupLocked(i)
			continue
		}
		i.adopted = true
		i.startedAt = time.Now() // best effort
		s.ev.Info(name, "adopted running instance on %s (pid %d)", i.tun, i.pid)
	}
}

// Views returns a snapshot of all instances.
func (s *Supervisor) Views() []wire.InstanceView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]wire.InstanceView, 0, len(s.insts))
	for name, i := range s.insts {
		st := s.st.Get(name)
		v := wire.InstanceView{
			Name:       name,
			Enabled:    st.Enabled,
			Running:    i.running(),
			Tun:        i.tun,
			Pid:        i.pid,
			Adopted:    i.adopted,
			ListenPort: i.conf.Interface.ListenPort,
			PeerCount:  len(i.conf.Peers),
			LastErr:    i.lastErr,
		}
		if i.running() && !i.startedAt.IsZero() {
			v.UptimeSec = int64(time.Since(i.startedAt).Seconds())
		}
		if i.status != nil {
			cut := time.Now().Add(-3 * time.Minute)
			for _, p := range i.status.Peers {
				if p.LastHandshake.After(cut) {
					v.OnlinePeers++
				}
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// LiveStatus returns the cached UAPI status for one instance.
func (s *Supervisor) LiveStatus(name string) (*uapi.DeviceStatus, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.insts[name]
	if !ok {
		return nil, time.Time{}, fmt.Errorf("unknown instance %q", name)
	}
	if !i.running() {
		return nil, time.Time{}, fmt.Errorf("instance %q not running", name)
	}
	return i.status, i.statusAt, nil
}

// Conf returns the parsed config of one instance (for the editor).
func (s *Supervisor) Conf(name string) (string, error) {
	s.mu.Lock()
	i, ok := s.insts[name]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("unknown instance %q", name)
	}
	var sb strings.Builder
	if err := i.conf.Serialize(&sb); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// CreateInstance validates and writes a new config file.
func (s *Supervisor) CreateInstance(name, content string, force bool) error {
	if err := validName(name); err != nil {
		return err
	}
	conf, err := parseAndValidate(content)
	if err != nil {
		return err
	}
	if err := s.checkConflicts(name, conf, force); err != nil {
		return err
	}
	path := filepath.Join(paths.ConfDir, name+".conf")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("instance %q already exists", name)
	}
	if err := os.MkdirAll(paths.ConfDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	s.ev.Info(name, "instance created")
	return s.LoadConfigs()
}

// UpdateInstance validates and replaces the config file content.
func (s *Supervisor) UpdateInstance(name, content string, force bool) error {
	conf, err := parseAndValidate(content)
	if err != nil {
		return err
	}
	if err := s.checkConflicts(name, conf, force); err != nil {
		return err
	}
	path := filepath.Join(paths.ConfDir, name+".conf")
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("instance %q not found", name)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	s.ev.Info(name, "config updated")
	return s.LoadConfigs()
}

// DeleteInstance removes the config; refuses running instances unless force.
func (s *Supervisor) DeleteInstance(name string, force bool) error {
	s.mu.Lock()
	i, ok := s.insts[name]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown instance %q", name)
	}
	if i.running() && !force {
		return fmt.Errorf("instance %q is running; stop it first or force", name)
	}
	if i.running() {
		s.mu.Lock()
		s.stopLocked(i, "delete")
		s.mu.Unlock()
	}
	if err := os.Remove(filepath.Join(paths.ConfDir, name+".conf")); err != nil {
		return err
	}
	s.st.Remove(name)
	s.ev.Info(name, "instance deleted")
	return s.LoadConfigs()
}

// checkConflicts returns an error on ListenPort clashes with other configs
// or with a port currently bound on the host, unless force is set. A port
// bound by this very instance (it is running with the same port) is fine.
func (s *Supervisor) checkConflicts(name string, conf *wgconf.Config, force bool) error {
	if force {
		return nil
	}
	others := map[string]*wgconf.Config{}
	s.mu.Lock()
	self, ok := s.insts[name]
	selfPort := 0
	selfRunning := ok && self.running()
	if selfRunning {
		selfPort = self.conf.Interface.ListenPort
	}
	for n, i := range s.insts {
		if n != name {
			others[n] = i.conf
		}
	}
	s.mu.Unlock()
	for _, c := range wgconf.DetectListenPortConflicts(name, conf, others) {
		return fmt.Errorf("ListenPort %d conflicts with instance %q", c.Port, c.Other)
	}
	if conf.Interface.ListenPort != selfPort &&
		wgconf.PortInUse(conf.Interface.ListenPort) {
		return fmt.Errorf("ListenPort %d is already bound on this host", conf.Interface.ListenPort)
	}
	return nil
}

// ---- helpers ----

func (i *inst) runtimeDir() string { return filepath.Join(paths.RunDir, i.name) }
func (i *inst) uapiPath() string   { return filepath.Join(i.runtimeDir(), i.tun+".sock") }

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
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

func prefixesToStrings(ps []netip.Prefix) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

// resolveEndpoint resolves host:port to ip:port with a bounded timeout.
func resolveEndpoint(ep string) (string, error) {
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var r net.Resolver
	ips, err := r.LookupIPAddr(ctx, host)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no addresses for %s", host)
	}
	ip := ips[0].IP.String()
	if strings.Contains(ip, ":") {
		return fmt.Sprintf("[%s]:%s", ip, port), nil
	}
	return fmt.Sprintf("%s:%s", ip, port), nil
}

func validName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("name must be 1-64 chars")
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return fmt.Errorf("name contains invalid character %q (use [a-zA-Z0-9_-])", c)
		}
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("name must not start with a dot")
	}
	return nil
}

func parseAndValidate(content string) (*wgconf.Config, error) {
	conf, err := wgconf.Parse(strings.NewReader(content))
	if err != nil {
		return nil, err
	}
	if err := conf.Validate(); err != nil {
		return nil, err
	}
	return conf, nil
}

func runHook(name, cmd string) error {
	c := exec.Command("sh", "-c", cmd)
	out, err := c.CombinedOutput()
	if err != nil && len(out) > 0 {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return err
}
