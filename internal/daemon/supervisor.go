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

	"github.com/dncore/wgtun/internal/logs"
	"github.com/dncore/wgtun/internal/paths"
	"github.com/dncore/wgtun/internal/state"
	"github.com/dncore/wgtun/internal/uapi"
	"github.com/dncore/wgtun/internal/wgconf"
	"github.com/dncore/wgtun/internal/wire"
)

// Endpoint DNS re-validation. WireGuard resolves a peer endpoint once, at
// config time, and afterwards only roams on inbound packets — so a DDNS name
// that starts pointing somewhere else is never noticed on its own and the
// tunnel keeps hammering the address it memorised. These three constants turn
// the reconcile tick into a follow-up loop for that case.
const (
	// endpointRecheck is the periodic re-resolution interval. It matches the
	// floor a DDNS record can realistically carry (its TTL), so checking
	// faster would only burn queries on a cached answer.
	endpointRecheck = 60 * time.Second
	// endpointFastRecheck is the minimum spacing between two "the tunnel
	// looks dead" checks, so a stale handshake cannot turn the 5s reconcile
	// tick into a DNS flood.
	endpointFastRecheck = 15 * time.Second
	// handshakeStaleAfter is when a peer that has handshaken at least once
	// counts as unreachable. WireGuard rekeys every ~2min, so 150s means no
	// traffic could complete for at least one rekey period.
	handshakeStaleAfter = 150 * time.Second
)

// inst is the runtime view of one configured instance.
type inst struct {
	name     string
	confPath string
	conf     *wgconf.Config

	pid       int
	tun       string // utunN
	adopted   bool   // true when the process was started by a previous daemon
	startedAt time.Time
	devicePub string // base64 public key derived from the config's private key

	restarts    []time.Time // restart attempts for backoff
	nextAttempt time.Time
	failStreak  int
	lastErr     string

	// cached live status, refreshed by the reconcile probe
	status   *uapi.DeviceStatus
	statusAt time.Time
	// lastEPCheck is when hostname peer endpoints were last re-resolved
	lastEPCheck time.Time
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadConfigsLocked()
}

// loadConfigsLocked is LoadConfigs without locking; callers hold s.mu.
func (s *Supervisor) loadConfigsLocked() error {
	if err := os.MkdirAll(paths.ConfDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(paths.ConfDir)
	if err != nil {
		return err // never clean up instances on a transient read failure
	}
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
			// First time this config is seen: treat it as something the user
			// wants managed — enable boot autostart and desired-running so a
			// migration from any previous setup hands over seamlessly.
			if !s.st.Has(name) {
				s.st.Set(name, state.Instance{Enabled: true, DesiredRun: true})
				s.ev.Info(name, "first import: enabled + desired-running")
			}
			continue
		}
		cur.confPath, cur.conf = path, conf
		cur.devicePub = "" // private key may have changed; re-derive lazily
	}
	for name, i := range s.insts {
		if !seen[name] {
			if i.running() {
				s.stopLocked(i, "config removed")
			}
			delete(s.insts, name)
			s.ev.Info(name, "config removed, instance dropped")
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

// devicePublicKey returns the base64 public key derived from the instance's
// private key, or "" when it cannot be derived.
func (i *inst) devicePublicKey() string {
	if i.devicePub != "" {
		return i.devicePub
	}
	if pub, err := wgconf.PublicKeyOf(i.conf.Interface.PrivateKey); err == nil {
		i.devicePub = pub
	}
	return i.devicePub
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
	// pick up config files added/removed on disk behind our back
	if err := s.loadConfigsLocked(); err != nil {
		s.ev.Warn("", "config rescan failed: %v", err)
	}
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
	if !alive {
		// stale recorded pid (e.g. after daemon adoption) — re-check the
		// socket holder before declaring the instance dead
		if p := socketPid(i.uapiPath()); p > 0 {
			i.pid = p
			alive = true
		}
	}
	var st *uapi.DeviceStatus
	var err error
	if alive {
		st, err = uapi.Get(i.uapiPath())
	}
	if alive && err == nil {
		i.status, i.statusAt = st, time.Now()
		i.failStreak = 0
		s.maybeFillEndpointsLocked(i, st)
		s.maybeRefreshEndpointsLocked(i, st)
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

// maybeRefreshEndpointsLocked re-resolves hostname peer endpoints and
// retargets the peer when the DNS answer moved. It is the periodic follow-up
// to maybeFillEndpointsLocked: that one covers "DNS was not ready yet" at
// start, this one covers "DNS now says something else" — the DDNS case, where
// the name is re-bound to a new address long after the tunnel came up.
//
// Only hostname endpoints are touched (a literal ip:port is authoritative)
// and only the endpoint field is written, so an existing session survives
// whenever the address did not actually change.
func (s *Supervisor) maybeRefreshEndpointsLocked(i *inst, st *uapi.DeviceStatus) {
	if !i.endpointCheckDue(st) {
		return
	}
	i.lastEPCheck = time.Now()
	for _, p := range i.conf.Peers {
		if !isHostnameEndpoint(p.Endpoint) {
			continue
		}
		live := findLivePeer(st, p.PublicKey)
		if live == nil || live.Endpoint == "" {
			// never pushed (or still empty): that is maybeFillEndpointsLocked's job
			continue
		}
		candidates, err := resolveEndpointAll(p.Endpoint)
		if err != nil {
			s.ev.Warn(i.name, "endpoint %s unresolved (will retry): %v", p.Endpoint, err)
			continue
		}
		// A round-robin record answers in a different order on every lookup;
		// flipping between equivalent addresses is churn, not progress, so any
		// candidate matching what the device already uses counts as unchanged.
		if containsString(candidates, live.Endpoint) {
			continue
		}
		if err := uapi.SetPeerEndpoint(i.uapiPath(), p.PublicKey, candidates[0]); err != nil {
			s.ev.Warn(i.name, "endpoint re-resolve push failed: %v", err)
			continue
		}
		s.ev.Info(i.name, "endpoint %s -> %s (%s changed), re-pushed", live.Endpoint, candidates[0], p.Endpoint)
	}
}

// endpointCheckDue rate-limits DNS re-validation to the periodic interval,
// plus a faster lane when a peer that used to handshake has gone quiet —
// exactly the signature of a name that moved to a dead address.
func (i *inst) endpointCheckDue(st *uapi.DeviceStatus) bool {
	since := time.Since(i.lastEPCheck)
	if since >= endpointRecheck {
		return true
	}
	if since < endpointFastRecheck {
		return false
	}
	return i.handshakeStale(st)
}

// handshakeStale reports whether a hostname peer that has handshaken before
// has stopped doing so. Peers that never handshaked are ignored: an idle peer
// looks exactly like an unreachable one, and guessing would hold the fast lane
// open forever.
func (i *inst) handshakeStale(st *uapi.DeviceStatus) bool {
	cut := time.Now().Add(-handshakeStaleAfter)
	for _, p := range i.conf.Peers {
		if !isHostnameEndpoint(p.Endpoint) {
			continue
		}
		live := findLivePeer(st, p.PublicKey)
		if live == nil || live.Endpoint == "" || live.LastHandshake.IsZero() {
			continue
		}
		if live.LastHandshake.Before(cut) {
			return true
		}
	}
	return false
}

// isHostnameEndpoint reports whether an endpoint still needs DNS: a host:port
// whose host is a name rather than a literal address.
func isHostnameEndpoint(ep string) bool {
	if ep == "" {
		return false
	}
	host, _, err := net.SplitHostPort(ep)
	if err != nil {
		return false
	}
	return net.ParseIP(host) == nil
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
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
	if err := os.MkdirAll(paths.WireGuardSockDir, 0o755); err != nil {
		return fmt.Errorf("mkdir wireguard socket dir: %v", err)
	}
	cmd := exec.Command(paths.WireGuardGo, "utun")
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
	i.adopted = false
	i.startedAt = started
	// wireguard-go forks on darwin: the launcher process exits immediately
	// and the real device process is reparented to launchd. Reap the
	// launcher silently; the authoritative pid comes from the UAPI socket
	// below.
	go func() { cmd.Wait() }()
	go s.pumpWireguardLogs(i.name, stderr)

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
	// the pid that matters is the one holding the UAPI socket
	pid, err := waitSocketPid(i.uapiPath(), 3*time.Second)
	if err != nil {
		s.cleanupLocked(i)
		i.lastErr = err.Error()
		return err
	}
	i.pid = pid
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(i.pid)), 0o644); err != nil {
		s.ev.Warn(i.name, "write pid file: %v", err)
	}
	if err := s.pushConfLocked(i); err != nil {
		s.cleanupLocked(i)
		i.lastErr = err.Error()
		return err
	}
	// the endpoints were just resolved by the push
	i.lastEPCheck = time.Now()
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

// cleanupLocked kills the process (if alive) and removes the runtime dir
// and the device's UAPI socket.
func (s *Supervisor) cleanupLocked(i *inst) {
	// trust the socket holder over the recorded pid when possible
	if i.tun != "" {
		if p := socketPid(i.uapiPath()); p > 0 {
			i.pid = p
		}
	}
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
	if i.tun != "" {
		os.Remove(i.uapiPath())
	}
	os.RemoveAll(i.runtimeDir())
	i.pid, i.tun, i.status = 0, "", nil
	i.lastEPCheck = time.Time{}
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
		tun, err := os.ReadFile(filepath.Join(dir, "tun.name"))
		if err != nil {
			os.RemoveAll(dir)
			continue
		}
		i.tun = strings.TrimSpace(string(tun))
		pid := 0
		if data, err := os.ReadFile(filepath.Join(dir, "pid")); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		if pid <= 0 || !pidAlive(pid) {
			// the recorded pid may be the exited launcher; trust the socket
			pid = socketPid(i.uapiPath())
		}
		if pid <= 0 {
			os.RemoveAll(dir)
			i.tun = ""
			continue
		}
		i.pid = pid
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

// DevicePublicKey returns the derived public key of one instance.
func (s *Supervisor) DevicePublicKey(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.insts[name]; ok {
		return i.devicePublicKey()
	}
	return ""
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
	// A freshly created instance starts at once but does not get boot
	// autostart implicitly.
	s.st.Set(name, state.Instance{Enabled: false, DesiredRun: true})
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

// uapiPath is where wireguard-go itself creates the control socket:
// its socket directory (hardcoded /var/run/wireguard upstream) plus the
// kernel-assigned tun name. The per-instance runtime dir only holds our
// pid/tun.name bookkeeping.
func (i *inst) uapiPath() string {
	return filepath.Join(paths.WireGuardSockDir, i.tun+".sock")
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// socketPid returns the pid holding the given UAPI socket, 0 if none.
// wireguard-go forks on darwin, so the pid returned by exec is not the
// device process — the socket holder is the authoritative one.
func socketPid(sockPath string) int {
	if sockPath == "" {
		return 0
	}
	out, err := exec.Command("/usr/sbin/lsof", "-t", sockPath).Output()
	if err != nil {
		return 0
	}
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil {
			return pid
		}
	}
	return 0
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

// resolveEndpoint resolves host:port to the preferred ip:port. A literal
// address is passed through untouched.
func resolveEndpoint(ep string) (string, error) {
	all, err := resolveEndpointAll(ep)
	if err != nil {
		return "", err
	}
	return all[0], nil
}

// resolveEndpointAll resolves host:port to every ip:port it may legitimately
// point at (multi-A records have more than one), with a bounded timeout.
func resolveEndpointAll(ep string) ([]string, error) {
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		return []string{ep}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := lookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, joinHostPort(a.IP, port))
	}
	return out, nil
}

// lookupIPAddr is a test seam around the system resolver.
var lookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	var r net.Resolver
	return r.LookupIPAddr(ctx, host)
}

// joinHostPort formats an ip:port the way the UAPI protocol expects it:
// bracketed for IPv6, bare for IPv4.
func joinHostPort(ip net.IP, port string) string {
	if ip.To4() != nil {
		return ip.String() + ":" + port
	}
	return "[" + ip.String() + "]:" + port
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
