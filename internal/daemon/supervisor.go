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
	// peerRetry counts consecutive endpoint fills for peers the device has not
	// accepted yet, so a refused peer cannot be re-pushed forever
	peerRetry map[string]int
	// confSig is the config file revision this instance was parsed from, so an
	// unchanged file is not re-read and re-parsed on every reconcile tick
	confSig fileSig
}

func (i *inst) running() bool { return i.pid != 0 }

// Supervisor owns all instances. All public methods are safe concurrently;
// lifecycle operations are serialized.
type Supervisor struct {
	mu    sync.Mutex // guards insts and lifecycle serialization
	insts map[string]*inst

	// badConf remembers the revision of every unparseable config so a broken
	// file is reported once per edit instead of once per tick.
	badConf map[string]fileSig

	st *state.Store
	ev *logs.Store
}

// fileSig identifies a config file revision cheaply: enough to notice an edit
// without reading and parsing every file on every tick. Size+mtime catch
// normal edits; the inode catches a preserving overwrite (cp -p, mv).
type fileSig struct {
	size int64
	mod  time.Time
	ino  uint64
}

func (sig fileSig) same(other fileSig) bool {
	return sig.size == other.size && sig.ino == other.ino && sig.mod.Equal(other.mod)
}

func statSig(path string) (fileSig, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileSig{}, err
	}
	sig := fileSig{size: fi.Size(), mod: fi.ModTime()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		sig.ino = st.Ino
	}
	return sig, nil
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
	if s.badConf == nil {
		s.badConf = map[string]fileSig{}
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".conf")
		seen[name] = true
		path := filepath.Join(paths.ConfDir, e.Name())
		sig, err := statSig(path)
		if err != nil {
			continue // gone between ReadDir and Stat; the next pass decides
		}
		cur, ok := s.insts[name]
		if ok && cur.confSig.same(sig) {
			// Unchanged revision: keep the parsed config and the derived key,
			// which is what keeps an idle tick almost free.
			continue
		}
		conf, err := loadConfFile(path)
		if err != nil {
			if !ok {
				// A file we never managed: report a broken revision once, not
				// once per tick (the event log is a rotating file).
				if s.badConf[name] != sig {
					s.ev.Error(name, "config invalid, skipped: %v", err)
					s.badConf[name] = sig
				}
				continue
			}
			// Keep running on the last good config; report the new broken
			// revision once (the sig marks it as seen).
			cur.confSig = sig
			cur.lastErr = fmt.Sprintf("config invalid: %v", err)
			s.ev.Error(name, "config invalid, skipped: %v", err)
			continue
		}
		delete(s.badConf, name)
		if !ok {
			s.insts[name] = &inst{name: name, confPath: path, conf: conf, confSig: sig}
			// First time this config is seen: treat it as something the user
			// wants managed — enable boot autostart and desired-running so a
			// migration from any previous setup hands over seamlessly.
			if !s.st.Has(name) {
				s.st.Set(name, state.Instance{Enabled: true, DesiredRun: true})
				s.ev.Info(name, "first import: enabled + desired-running")
			}
			continue
		}
		cur.confPath, cur.conf, cur.confSig = path, conf, sig
		cur.devicePub = "" // private key may have changed; re-derive lazily
		cur.lastErr = ""
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
	for name := range s.badConf {
		if !seen[name] {
			delete(s.badConf, name)
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

// probePlan is one instance's work for a reconcile pass: decided under the
// lock, executed without it. The UAPI round trip and any DNS lookups block for
// up to several seconds, and s.mu is what the TUI and the API need to stay
// responsive, so neither may happen while holding it.
type probePlan struct {
	name     string
	uapiPath string
	jobs     []endpointJob // hostname endpoints worth re-resolving this pass
}

// endpointJob is one hostname endpoint that needs a fresh DNS answer.
type endpointJob struct {
	peerKey  string // base64 public key of the peer in the config
	endpoint string // hostname:port as written in the config
}

// resolution is the outcome of resolving one endpointJob.
type resolution struct {
	candidates []string
	err        error
}

// probeResult is what the I/O phase of one plan learned.
type probeResult struct {
	plan     probePlan
	status   *uapi.DeviceStatus
	getErr   error
	resolved map[string]resolution // by peer key
}

// Reconcile drives actual state toward desired state and refreshes cached
// statuses. It is the periodic heartbeat and the only place that restarts.
//
// It runs in three phases: decide under the lock, do the blocking I/O (UAPI
// probe, DNS) without it, then apply the results under the lock again. Every
// phase re-validates what it touches, since the world can change in between.
func (s *Supervisor) Reconcile() {
	s.mu.Lock()
	confErr := s.loadConfigsLocked() // pick up config files changed on disk
	plans := s.planLocked()
	s.mu.Unlock()

	if confErr != nil {
		s.ev.Warn("", "config rescan failed: %v", confErr)
	}

	results := runProbes(plans)

	s.mu.Lock()
	s.finishLocked(results)
	s.mu.Unlock()
}

// planLocked handles the lifecycle transitions that are due and returns the
// probes to run. Callers hold s.mu.
func (s *Supervisor) planLocked() []probePlan {
	names := make([]string, 0, len(s.insts))
	for n := range s.insts {
		names = append(names, n)
	}
	sort.Strings(names)

	var plans []probePlan
	for _, name := range names {
		i := s.insts[name]
		st := s.st.Get(name)
		switch {
		case st.DesiredRun && !i.running():
			s.tryStartLocked(i)
		case !st.DesiredRun && i.running():
			s.stopLocked(i, "desired stopped")
		case i.running():
			plans = append(plans, probePlan{
				name:     i.name,
				uapiPath: i.uapiPath(),
				jobs:     endpointJobs(i),
			})
		}
	}
	return plans
}

// runProbes performs the blocking work: one UAPI round trip per instance plus
// one DNS resolution per endpoint that is due. Called WITHOUT s.mu held.
func runProbes(plans []probePlan) []probeResult {
	out := make([]probeResult, 0, len(plans))
	for _, p := range plans {
		res := probeResult{plan: p}
		st, err := uapi.Get(p.uapiPath)
		res.status, res.getErr = st, err
		// A device that does not answer has nothing useful to compare against,
		// so do not spend lookups on it.
		if err == nil && len(p.jobs) > 0 {
			res.resolved = make(map[string]resolution, len(p.jobs))
			for _, job := range p.jobs {
				cands, rerr := resolveEndpointAll(job.endpoint)
				res.resolved[job.peerKey] = resolution{candidates: cands, err: rerr}
			}
		}
		out = append(out, res)
	}
	return out
}

// finishLocked applies probe results: caches the fresh status, retires unhealthy
// instances with backoff, and pushes whatever the DNS answers imply. Callers
// hold s.mu.
func (s *Supervisor) finishLocked(results []probeResult) {
	for _, r := range results {
		i, ok := s.insts[r.plan.name]
		if !ok || i.uapiPath() != r.plan.uapiPath {
			continue // removed, or restarted on another tun while we probed
		}
		if r.getErr == nil {
			if !pidAlive(i.pid) {
				// Bookkeeping only: the device answered, so refresh the recorded
				// pid when it is the exited launcher wireguard-go forks off.
				if p := socketPid(i.uapiPath()); p > 0 {
					i.pid = p
				}
			}
			i.status, i.statusAt = r.status, time.Now()
			i.failStreak = 0
			s.applyEndpointsLocked(i, r.status, r.plan.jobs, r.resolved)
			continue
		}
		i.failStreak++
		reason := "process dead"
		if pidAlive(i.pid) {
			reason = fmt.Sprintf("uapi unresponsive: %v", r.getErr)
		}
		s.ev.Error(i.name, "instance unhealthy (%s), cleaning up for restart", reason)
		s.cleanupLocked(i)
		s.tryStartLocked(i)
	}
}

// peerFillGiveUp is how many consecutive surgical pushes one peer gets before
// the daemon concludes the device is refusing it and stops retrying. Only a
// peer the device silently drops can get here — wireguard-go accepts a peer
// whose public key equals the device's own and then ignores it — and without a
// cap the 5s reconcile tick would re-push it forever.
const peerFillGiveUp = 6

// endpointJobs lists the hostname endpoints that need a fresh DNS answer this
// pass: every one whose live endpoint is missing (the "came up before the
// network did" self-heal, retried each tick) plus, when the periodic gate
// allows, every other hostname endpoint. Read-only: it runs under the lock but
// performs no I/O, so the blocking lookups happen in runProbes instead.
//
// A device whose status was never read yet yields no jobs: the config was just
// pushed with a fresh resolution at start or adoption, and the next pass has a
// status to compare against.
func endpointJobs(i *inst) []endpointJob {
	if i.status == nil {
		return nil
	}
	due := i.endpointCheckDue(i.status)
	var jobs []endpointJob
	for _, p := range i.conf.Peers {
		if !isHostnameEndpoint(p.Endpoint) {
			continue
		}
		live := findLivePeer(i.status, p.PublicKey)
		if live != nil && live.Endpoint != "" && !due {
			continue
		}
		jobs = append(jobs, endpointJob{peerKey: p.PublicKey, endpoint: p.Endpoint})
	}
	return jobs
}

// applyEndpointsLocked pushes whatever the pre-computed DNS answers imply: a
// retargeted endpoint when the answer moved, or a peer's whole block when the
// device never got one. It never sends replace_peers, so an instance's healthy
// peers keep their sessions while another peer is being fixed.
func (s *Supervisor) applyEndpointsLocked(i *inst, st *uapi.DeviceStatus, jobs []endpointJob, res map[string]resolution) {
	if len(jobs) == 0 {
		return
	}
	i.lastEPCheck = time.Now()
	for _, job := range jobs {
		r, ok := res[job.peerKey]
		if !ok {
			continue
		}
		if r.err != nil {
			// Usually DNS not being up yet right after boot: keep asking, this
			// clears itself as soon as the resolver answers.
			s.ev.Warn(i.name, "endpoint %s unresolved (will retry): %v", job.endpoint, r.err)
			continue
		}
		live := findLivePeer(st, job.peerKey)

		if live != nil && live.Endpoint != "" {
			// A round-robin record answers in a different order on every lookup;
			// flipping between equivalent addresses is churn, not progress, so
			// any candidate matching what the device already uses counts as
			// unchanged.
			if containsString(r.candidates, live.Endpoint) {
				delete(i.peerRetry, job.peerKey) // healthy: forget any backoff
				continue
			}
			if err := uapi.SetPeerEndpoint(i.uapiPath(), job.peerKey, r.candidates[0]); err != nil {
				s.ev.Warn(i.name, "endpoint %s -> %s push failed: %v", live.Endpoint, r.candidates[0], err)
				continue
			}
			s.ev.Info(i.name, "endpoint %s -> %s (%s changed), re-pushed", live.Endpoint, r.candidates[0], job.endpoint)
			continue
		}

		// The device has no usable endpoint for this peer.
		if i.peerRetry[job.peerKey] >= peerFillGiveUp {
			continue // already reported; a config change or restart retries
		}
		var err error
		if live == nil {
			// No such peer at all, so its allowed IPs are missing too: only a
			// full peer block can fix that.
			p := findConfPeer(i.conf, job.peerKey)
			if p == nil {
				continue // config edited while we were resolving; next pass handles it
			}
			err = uapi.UpsertPeer(i.uapiPath(), uapi.SetPeer{
				PublicKey:           p.PublicKey,
				PresharedKey:        p.PresharedKey,
				Endpoint:            r.candidates[0],
				AllowedIPs:          prefixesToStrings(p.AllowedIPs),
				PersistentKeepalive: p.PersistentKeepalive,
			})
		} else {
			err = uapi.SetPeerEndpoint(i.uapiPath(), job.peerKey, r.candidates[0])
		}
		if err != nil {
			s.ev.Warn(i.name, "endpoint push for %s failed: %v", job.endpoint, err)
			continue
		}
		if live == nil {
			if i.bumpPeerRetry(job.peerKey) == peerFillGiveUp {
				s.ev.Error(i.name, "peer %s still absent from the device after %d pushes, giving up; wireguard-go silently drops a peer whose public key equals this instance's own", abbrevKey(job.peerKey), peerFillGiveUp)
			}
			continue
		}
		s.ev.Info(i.name, "endpoint %s resolved and pushed", r.candidates[0])
	}
}

// findConfPeer returns the configured peer with the given public key.
func findConfPeer(conf *wgconf.Config, pubB64 string) *wgconf.Peer {
	for idx := range conf.Peers {
		if conf.Peers[idx].PublicKey == pubB64 {
			return &conf.Peers[idx]
		}
	}
	return nil
}

// bumpPeerRetry counts one more rejected endpoint fill for a peer key.
// Lazily initialised so a hand-built inst (tests) cannot panic on assignment.
func (i *inst) bumpPeerRetry(key string) int {
	if i.peerRetry == nil {
		i.peerRetry = map[string]int{}
	}
	i.peerRetry[key]++
	return i.peerRetry[key]
}

// abbrevKey shortens a base64 key for log lines.
func abbrevKey(k string) string {
	if len(k) <= 12 {
		return k
	}
	return k[:12] + "..."
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
		if live == nil || live.Endpoint == "" || !handshaked(live.LastHandshake) {
			continue
		}
		if live.LastHandshake.Before(cut) {
			return true
		}
	}
	return false
}

// handshaked reports whether a last-handshake timestamp means a handshake
// actually happened. wireguard-go reports 0 seconds for "never", which
// time.Unix turns into 1970 — not the zero Time — so depending on parseGet
// alone is too subtle a contract to rely on here.
func handshaked(t time.Time) bool { return !t.IsZero() && t.Unix() > 0 }

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
	if st == nil {
		return nil
	}
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
	os.RemoveAll(i.runtimeDir())
	i.pid, i.tun, i.status = 0, "", nil
	i.lastEPCheck = time.Time{}
	i.peerRetry = nil
	i.adopted = false
}

// ownsProcess reports whether the recorded pid really is this instance's
// wireguard-go. A pid file outlives its process, and once the OS recycles that
// pid the daemon would otherwise SIGTERM an unrelated process — as root. The
// UAPI socket holder is proof; failing that (the device is wedged or already
// gone, which is exactly when we get here) demand that the pid still looks like
// wireguard-go.
func (i *inst) ownsProcess() bool {
	if pid := socketPid(i.uapiPath()); pid != 0 && pid == i.pid {
		return true
	}
	return isWireGuardGo(i.pid)
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
