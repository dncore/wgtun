# wgtun

**English** | [简体中文](README.zh-CN.md)

[![Release](https://img.shields.io/github/v/release/dncore/wgtun)](https://github.com/dncore/wgtun/releases)
[![License](https://img.shields.io/github/license/dncore/wgtun)](LICENSE)

WireGuard orchestrator for macOS and Linux — a terminal UI plus a root
launchd/systemd daemon that manages any number of WireGuard instances: the
in-kernel implementation on Linux (with automatic `wireguard-go` fallback),
and `wireguard-go` everywhere else. No `wg-quick` scripts, no per-interface
service definitions, no kernel extensions.

```
wgtun                 open the terminal UI
wgtun daemon          run the privileged supervisor (the service runs this)
```

## Why

macOS has no in-kernel WireGuard; everyone runs the userspace
`wireguard-go` binary. Linux does have one, but the orchestration problem is
the same: `wg-quick` gives every interface its own unit, resolves the endpoint
once at start, and has no supervisor to adopt or repair a wedged device.
Managing more than one tunnel with the stock tools means hand-rolled shell
scripts, per-interface service definitions and a pile of shared runtime files
that rot in interesting ways (stale locks, zombie backends, `already exists`
deadlocks on restart).

wgtun replaces all of that with two pieces:

- **A daemon** that owns every wireguard-go process and continuously
  reconciles reality against the desired state (adopt after crashes,
  restart with backoff, follow endpoint DNS — boot readiness and DDNS
  re-binds alike).
- **A TUI** that talks to the daemon over a local socket and gives you
  dashboards, instance management, a config editor and log replay.

|  | wg-quick + scripts | wgtun |
|---|---|---|
| Multiple tunnels | one script per tunnel | any number, one supervisor |
| Boot autostart | per-interface service files | per-instance flag |
| Crash recovery | usually none | adopt + self-heal + backoff |
| Endpoint DNS | resolved once at start, then stale for good | re-resolved, retargeted **only when the tunnel is unhealthy** (DDNS, no flapping) |
| Linux implementation | kernel or hand-rolled userspace | kernel first, automatic wireguard-go fallback |
| Live state | `wg show` parsing | native UAPI / netlink, structured data |
| Config editing | edit files by hand | TUI editor with conflict checks |
| Logs | un-timestamped stderr dumps | structured events, replay + follow |

## TUI preview

**Dashboard** — running/online/traffic tiles, one card per instance with the
full peer picture (endpoints, allowed IPs, keepalive, PSK flag, handshake
age, byte counters) and a live rx-throughput sparkline:

```
 Dashboard    Instances    Logs    Settings
 WireGuard Dashboard

 ╭───────────╮ ╭───────────────╮ ╭────────────────────╮
 │  Running  │ │  Online peers │ │  Total traffic     │
 │  2/2      │ │  2            │ │  1.4M ↓ / 3.8M ↑   │
 ╰───────────╯ ╰───────────────╯ ╰────────────────────╯

 ╭──────────────────────────────────────╮ ╭──────────────────────────────────────╮
 │ work  running  utun0                 │ │ home  running  utun7                 │
 │ listen 51820  pub AbCd…WxYz=         │ │ listen 51821  pub EfGh…YzAb=         │
 │ ● peer 1  1a2B…c3D4=  ka25           │ │ ● peer 1  5e6F…g7H8=  PSK ka25       │
 │   endpoint 203.0.113.7:51820         │ │   endpoint vpn.example.com:51820     │
 │   allowed  10.0.0.0/24, 10.0.1.0/24  │ │   allowed  10.10.0.0/24              │
 │   just now  rx 12.3M tx 4.5M         │ │   2m ago  rx 812K tx 240K            │
 │           ▁▂▃▅▇█▇▅▃▂▁                │ │   ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁                    │
 │ rx 48.2KB/s                          │ │ rx 0B/s                              │
 ╰──────────────────────────────────────╯ ╰──────────────────────────────────────╯

    r refresh   R repair daemon   ctrl+q quit
```

**Instances** — table with boot-autostart flag, state, listen port, peer
counts; `u/d` start-stop, `r` restart, `b` toggle autostart, `e` edit,
`n` new, `x` delete, `enter` live peer detail.

```
 Instances
 ╭────────────────┬──────┬─────────┬───────┬───────┬────────┬──────────╮
 │ NAME           │ BOOT │ STATE   │ PORT  │ PEERS │ ONLINE │ UPTIME   │
 │ home           │  ★   │ up      │ 51821 │ 1     │ 1      │ 2h14m    │
 │ work           │  ★   │ up      │ 51820 │ 1     │ 1      │ 2h14m    │
 ╰────────────────┴──────┴─────────┴───────┴───────┴────────┴──────────╯
```

**Logs** — structured events (time · level · instance · message) with
filters (`i` instance, `l` level) and live follow (`f`):

```
 Logs  [follow]
   inst:all  level:WARN  [i] inst  [l] level  [f] follow  [c] clear

 14:02:11  INFO   work    started on utun0 (pid 1234)
 14:03:40  WARN   home    endpoint vpn.example.com unresolved (will retry)
 14:05:02  INFO   work    endpoint resolved and pushed
 14:07:31  ERROR  home    instance unhealthy (process dead), cleaning up for restart
 14:07:32  INFO   home    started on utun7 (pid 1387)
```

**Editor** — full form editing for interface and peers: generate keys
inline, per-peer CRUD, validation on save (CIDR, key format, and
**listen-port conflicts** against other configs or the host).

**Settings** — switch the UI language (English / 中文, persisted per user),
daemon info and paths.

## Install

```bash
brew trust dncore/tap           # one-time: Homebrew requires trusting third-party taps
brew install dncore/tap/wgtun   # depends on wireguard-go
sudo wgtun daemon --install     # install + start the daemon (launchd)
wgtun                           # open the TUI
```

Configs live in `/usr/local/etc/wireguard/*.conf` (macOS) or
`/etc/wireguard/*.conf` (Linux), wg-quick compatible. Existing files are
imported on first start; nothing to migrate.

### Linux

```bash
# wireguard-go is only needed when the kernel module is unavailable
sudo apt install wireguard-go        # Debian/Ubuntu; other distros: package manager or source

sudo wgtun daemon --install          # writes /etc/systemd/system/wgtun.service
wgtun                                # open the TUI
wgtun status                         # daemon health + endpoint DNS diagnostics
```

`--install` bakes `WGTUN_RECONCILE_INTERVAL`, `WGTUN_ENDPOINT_RESOLVER`,
`WGTUN_BACKEND` and the invoking user's primary group (as
`WGTUN_SOCKET_GROUP`, so the unprivileged TUI can reach the socket) into the
unit. The kernel module is preferred; force either implementation with:

```bash
sudo env WGTUN_BACKEND=kernel    wgtun daemon --install
sudo env WGTUN_BACKEND=userspace wgtun daemon --install
```

When migrating from `wg-quick@`, run `sudo systemctl disable --now
wg-quick@wg0` first; otherwise both sides manage the same `wg0`. wgtun's
Linux interfaces are named `wgtun-<instance>` (trimmed and hashed past 15
characters), so they never collide with `wg0`.

## Importing an existing config

Three ways, depending on what you have:

```bash
wgtun import ~/Downloads/vpn.conf        # CLI: validates, enables autostart, starts it
wgtun import vpn.conf --name office      # override the instance name
```

- **In the TUI**: open the editor (`n` in the Instances tab) and press
  `ctrl+v` — paste the whole `[Interface]`/`[Peer]` config, `ctrl+s`
  fills the form, adjust and save.
- **Drop a file**: put any `.conf` into `/usr/local/etc/wireguard/` — the
  daemon picks it up automatically, enables autostart and starts it.

The daemon detects `wireguard-go` at startup (path and version are shown
in Settings). If it is missing, start-up logs and the TUI tell you what
to install instead of failing silently at first tunnel start.

## Architecture

```
             your user (no sudo)                   root (launchd / systemd)

 ┌─────────────────────────────┐            ┌─────────────────────────────────────┐
 │  wgtun — terminal UI        │            │  wgtun daemon — com.wgtun.daemon    │
 │                             │   HTTP     │                    / wgtun.service  │
 │  Dashboard  Instances       │◄─over unix─►│  supervisor / reconcile loop (5s)  │
 │  Editor     Logs  Settings  │   socket   │    ├── kernel WG → wgtun-wg0        │
 │                             │ /var/run/  │    ├── wireguard-go → utun0 / wgtun-*│
 │  reads live state, edits    │ wgtun.sock │    └── ... any number of instances │
 │  configs, follows logs      │ (root:admin│                                     │
 │                             │  0660)     │  kernel: wgctrl/netlink             │
 └─────────────────────────────┘            │  userspace: native UAPI             │
                                            │  ifconfig/route (macOS) · ip (Linux)│
                                            │  adopt · backoff · DNS re-resolve   │
                                            │   ├─ not ready at boot              │
                                            │   └─ DDNS move (health-gated)       │
                                            │  /var/lib/wgtun/state.json          │
                                            │  /var/log/wgtun/events.jsonl        │
                                            └─────────────────────────────────────┘
```

- **Two backends.** Linux prefers the in-kernel implementation (`ip link
  add ... type wireguard`, configured over netlink/wgctrl) and falls back
  to `wireguard-go` when the module is unavailable. macOS only has
  userspace. Config is pushed over UAPI or wgctrl; addresses, MTU and
  routes are applied with `ifconfig`/`route` (macOS) or `ip` (Linux).
- **The daemon owns the devices.** Each instance gets its own bookkeeping
  directory (`/var/run/wgtun/<name>/`: pid, tun name, endpoint
  diagnostics). When an endpoint address is covered by AllowedIPs
  (`0.0.0.0/0` being the common case), the daemon pins a host route to it
  through the physical gateway first — otherwise the encrypted packets
  would be routed back into their own tunnel.
- **Self-heal, not just restart.** Every 5 seconds the reconcile loop
  probes each running instance over UAPI. A dead process or a wedged
  socket is cleaned up (kill + runtime dir) and restarted with backoff
  (5 failed starts in 10 min → 5 min pause). Endpoints whose DNS was not
  ready at boot are re-resolved and pushed later, as one-peer UAPI writes:
  retrying cannot reset the sessions of the instance's healthy peers.
- **The heartbeat stays out of the way.** A pass runs in three phases: decide
  under the supervisor lock, do the blocking work (the UAPI round trip and any
  DNS lookup) with the lock released, then apply the results under it again —
  so a slow or dead resolver cannot stall the TUI or an API call. Unchanged
  config files are not re-read or re-parsed either: size, mtime and inode are
  compared first. `WGTUN_RECONCILE_INTERVAL` tunes the tick (default `5s`; keep
  it well below the 15s endpoint fast lane, or that lane coarsens to the tick).
  Set it for the daemon with `sudo env WGTUN_RECONCILE_INTERVAL=30s wgtun daemon
  --install`, which bakes it into the plist. `--install` regenerates the service
  definition from its template, but the WGTUN_* values it wrote are saved in
  `/var/lib/wgtun/service-env.json` and reused by later repairs/upgrades; a value
  edited into the plist by hand still would not survive.
- **Endpoint DNS is followed, not frozen.** An endpoint written as a hostname
  is re-resolved on a schedule, but retargeted in place only when the current
  address is no longer healthy — a healthy tunnel is not disturbed by a DNS
  answer that moves between two working addresses. See
  [Endpoint DNS following](#endpoint-dns-following-ddns).
- **Crash adoption.** If the daemon itself dies, the devices keep running
  (wireguard-go processes in userspace mode, kernel interfaces in kernel
  mode); the next daemon start adopts them by pid/socket/interface
  liveness — tunnels are not interrupted by daemon restarts. That depends
  on the service definition sparing our children (`AbandonProcessGroup` /
  `KillMode=process`, or stopping the service takes the tunnels down with
  it); the price is that `daemon --uninstall` reaps them itself, verifying
  each pid still runs wireguard-go before signalling it.
- **Boot semantics.** Enabled instances come up at boot. A clean-shutdown
  marker distinguishes a reboot (apply autostart) from a plain daemon
  restart (respect whatever you had stopped).
- **One privileged component.** You never run `wgtun` with `sudo`; the TUI
  talks to the root daemon over a unix socket. Config files stay
  root-owned.

> Implementation note: wgtun launches wireguard-go with `-f` (foreground), so
> the process it execs is the device process itself — without `-f` the binary
> daemonizes and the recorded pid would be a corpse. The **holder of the UAPI
> socket** is still treated as authoritative when adopting after a daemon
> restart: macOS uses `lsof`, Linux uses `SO_PEERCRED` on the socket.

## Endpoint DNS following (DDNS)

A peer whose `Endpoint` is a hostname (`vpn.example.com:51820`) is treated as a
*moving* address, not as a value to resolve once at start-up. WireGuard itself
resolves an endpoint exactly once and afterwards only updates it from inbound
packets ("roaming"), so a DDNS name re-bound to a new address would leave the
tunnel hammering the address it memorised until someone restarted the
interface. wgtun re-resolves the name itself and retargets the peer in place.

**How a change is detected.** Every 5 seconds the reconcile loop probes each
running instance over the UAPI socket, which also refreshes the endpoint the
device is *actually* using and its last handshake time. On a due check the name
is re-resolved and compared against that live value:

```
5s tick
  │
  ├─ probe instance (UAPI get=1) ──► live endpoint + last handshake time
  │
  ├─ endpoint a hostname? ───────────── no ──► literal ip:port, never touched
  │                                   yes
  ├─ check due? ────────────┐
  │  (last check >= 60s,
  │   or >= 15s and handshake stale > 150s)
  │                         │ yes
  │                         ▼
  │                    resolve every A/AAAA the name answers with
  │                         │
  │      live endpoint equals any candidate?
  │        ├─ yes ──► tunnel healthy?
  │        │            ├─ yes ──► hold (DEBUG)
  │        │            └─ no  ──► rotate to another answer (>=60s apart)
  │        └─ no  ──► tunnel healthy?
  │                     ├─ yes ──► hold the current endpoint, remember the
  │                     │            new answer (DEBUG)
  │                     └─ no  ──► set=1 public_key=<peer> endpoint=<new>
  │                                 (no replace_peers) ──► INFO old -> new
```

**Retarget only when unhealthy.** This is deliberate. A name can answer with
two A records, or the host can have two resolvers that disagree; "push on every
answer change" makes a working tunnel flap between addresses (the log fills
with paired `A -> B` / `B -> A` lines) while the old address is still
handshaking fine. DNS answers are therefore treated as *fallback information*:
if the current endpoint is still among the answers and the handshake is fresh,
nothing happens; if it disappeared from the answers but the tunnel is still
healthy, the new answer is held until the tunnel actually dies (handshake older
than 150s). When a multi-answer record's first entry is dead, an unhealthy peer
rotates through the remaining answers at most once per 60 seconds.

**Health rule.** A peer that has handshaked is unhealthy once its last
handshake is older than 150s. A peer that never handshaked counts as unhealthy
only when it asks for keepalives, the instance has been up for more than two
minutes, and it has transmitted (`tx > 0`) — otherwise an idle peer would hold
the fast lane open forever.

**It is observable.** `wgtun status` (headless) and the TUI instance detail
pane show, per hostname endpoint: when DNS was last consulted, which resolver
answered, every answer (with its TTL when the resolver reports one), a fake-IP
marker, and the decision taken
(`unchanged` / `held (healthy)` / `retargeted` / `rotated`). The same event is
written at DEBUG level.

**Two lanes (not to scale).** A healthy tunnel is re-checked every 60 seconds —
about the shortest TTL a DDNS record realistically carries, so a cached answer
is not interrogated faster than it can change. A peer whose handshake has gone
older than 150 seconds (more than one rekey period: the signature of a name
that now points at a dead address) is re-checked on a 15 second floor instead,
so recovery does not wait out the interval. Peers that never handshaked normally
stay on the slow lane on purpose: an idle peer is indistinguishable from an
unreachable one, and guessing would hold the fast lane open forever. The
exception is a keepalive-enabled peer that has been up for two minutes and
transmitted but never completed a handshake — the signature of a dead first
DNS answer — which also gets the fast lane so it can rotate to another answer.

```
healthy tunnel   ├──── 60s ────┼──── 60s ────┼──── 60s ────┤
wedged tunnel    ├── 15s ──┼── 15s ──┼── 15s ──┼── 15s ──┼── 15s ──┤
                           ▲
          a handshake older than 150s opens the fast lane
```

**The update is surgical.** Retargeting sends one peer block and nothing else,
so session keys, allowed IPs and the keepalive survive; only the destination
address changes. Addresses, MTU and routes are not touched.

```
targeted (what a change sends)      full re-push (deliberately avoided)
  set=1                               set=1
  public_key=<peer>                   private_key=...
  endpoint=<new ip:port>              replace_peers=true  <- drops every peer
                                      public_key=<peer>
                                      replace_allowed_ips=true
                                      allowed_ip=192.0.2.0/24
```

The same one-peer writes are used to hand an endpoint to a peer the device
never accepted, and to fill in an endpoint whose DNS was not up yet at boot;
both are capped so a peer the device keeps refusing cannot be re-pushed on
every tick forever.

**What it deliberately does not do.** A literal `ip:port` endpoint is
authoritative and never re-resolved. Multi-answer records are compared as a
set: if the device already uses any one of the answers nothing happens, so a
round-robin record cannot make the daemon flip between equivalent addresses. A
name that resolves to an address the tunnel cannot reach is still pushed —
reachability is not probed first — and that is what lets the tunnel recover by
itself within one interval after the record is corrected.

**The record's TTL is the floor.** Detection cannot be faster than the TTL: every
resolver between the daemon and the authoritative server caches the old answer
for exactly that long, and looking more often neither refreshes nor expires
that cache. Measured against a real DDNS record carrying a ten-minute TTL:

```
home IP changes            cache expires          retarget + handshake
      │                          │                          │
T ────┴── handshake stale ≤150s ─┴─── up to TTL (600s) ─────┴── ≤15s ──► up
      └── fast lane on ─────────►│◄── cached: still the old address
```

Recovery therefore lands between roughly 2.5 minutes (the handshake-stale
threshold has to be crossed before the fast lane opens) and TTL + that
threshold + one fast-lane tick, depending on how much of the cached answer's
lifetime was left when the address changed. Two ways to remove the wait: keep
the TTL at 60s or lower, or take DNS out of the reverse direction entirely by
putting `PersistentKeepalive = 25` on
the remote peer that faces this machine — its packets then arrive from the new
address and WireGuard's own roaming retargets the endpoint in about 25 seconds,
with no lookup involved. That second option also covers the case no amount of
re-resolving can: a DDNS record that never gets updated at all.

### fake-IP and untrusted system resolvers

If the upstream router (OpenWrt + OpenClash/Mihomo, Surge, …) runs fake-IP, it
may answer *any* name with a synthetic address such as `198.18.x.x`. Then:

- the resolver always returns the same fake IP, so a real address change is
  **invisible** to DDNS tracking;
- the system resolver's port-53 queries are commonly hijacked by the router, so
  pointing it at another nameserver does not help;
- whether the tunnel works at all depends on the router proxying UDP to that
  fake IP correctly — WireGuard handshakes are UDP, and many fake-IP setups do
  not forward them.

wgtun recognises `198.18.0.0/15` and `fc00::/18`, logs a WARN (once per
endpoint per hour) and marks the endpoint as `fake-IP` in diagnostics. To
actually bypass fake-IP, resolve over HTTPS (port 53 hijacking does not apply):

```bash
sudo env WGTUN_ENDPOINT_RESOLVER=doh:https://dns.alidns.com/dns-query \
  wgtun daemon --install
```

`udp:1.1.1.1:53` (a trusted DNS server; still subject to port-53 interception)
and the default `system` are also supported. The selected resolver is shown by
`wgtun status` and in Settings. DoH reveals the real address, so make sure it
is reachable before switching if the tunnel currently relies on the router's
fake-IP proxying.

## Configuration

Fully wg-quick compatible parsing: multiple `Address` lines, `DNS`,
`MTU`, `Table`, `PostUp`/`PostDown` (executed on up/down). Deliberate
deviations:

- **DNS is parsed and preserved but not applied** to the system resolver —
  changing global DNS on macOS is invasive, and Linux resolvconf /
  systemd-resolved integrations differ wildly. Use a `PostUp` hook if you
  need it.
- `Table`: ignored on macOS; on Linux `off` is honoured (no routes, manage
  them yourself with `PostUp`) and a numeric table is warned about and skipped
  (no fwmark/policy routing).
- Linux interfaces are named `wgtun-<instance>` (trimmed and hashed past 15
  characters) so they never collide with wg-quick's `wg0`.
- Kernel and userspace devices speak the same WireGuard protocol, so
  `wg show all` keeps working in both modes.

Conflicts are checked when you save in the editor: `ListenPort` clashes
with other wgtun instances and with ports already bound on the host are
refused (force-save overrides).

## Daemon operations

```bash
sudo wgtun daemon --install     # install + start (launchd / systemd)
sudo wgtun daemon --uninstall   # stop + remove (and reap leftover tunnels)
sudo wgtun daemon               # run in the foreground (debugging)
wgtun status                    # daemon health + per-instance endpoint DNS

# macOS
sudo launchctl print system/com.wgtun.daemon | head
# Linux
systemctl status wgtun
journalctl -u wgtun -n 50
```

**Self-supervision.** Every reconcile pass updates a heartbeat; if the loop
makes no progress at all for five minutes (a deadlock, not a slow pass), the
daemon logs an ERROR and exits so launchd/systemd restarts it. Tunnels survive
the restart (`AbandonProcessGroup` / `KillMode=process`) and are adopted, so
this deliberate self-restart does not drop traffic. If the socket itself is
unreachable, the TUI shows a red banner on every tab; press `R` to run
`daemon --install` through the system authorization dialog
(osascript/pkexec).

| Data | macOS | Linux |
|---|---|---|
| Wiring configs | `/usr/local/etc/wireguard/` | `/etc/wireguard/` |
| Desired state | `/var/lib/wgtun/state.json` | same |
| Runtime | `/var/run/wgtun/` | `/run/wgtun/` |
| Control socket | `/var/run/wgtun.sock` | `/run/wgtun.sock` |
| Event log | `/var/log/wgtun/events.jsonl` (rotated) | same |
| TUI settings | `~/.config/wgtun/settings.json` | same |

## Releasing

Pushing a `v*` tag triggers the release workflow: tests run (including an
Ubuntu job so the Linux build is exercised), binaries build for
darwin/amd64 + arm64 and linux/amd64 + arm64, a GitHub release is created, and
`Formula/wgtun.rb` is pushed to `dncore/homebrew-tap`.

**One-time setup** (already done for this repo):

1. Create the tap repository:
   `gh repo create dncore/homebrew-tap --public --add-readme`
2. Create a fine-grained PAT with **Contents: Read and write**, repository
   access limited to `dncore/homebrew-tap`, and store it:
   `gh secret set HOMEBREW_TAP_TOKEN --repo dncore/wgtun`
   (The default `GITHUB_TOKEN` cannot push to a different repository.)

**Each release:**

```bash
git tag v0.2.0
git push origin v0.2.0
```

Users upgrade with `brew update && brew upgrade wgtun`. If the LaunchDaemon
was installed with `wgtun daemon --install`, re-run it once when upgrading
from 0.2.1 or earlier: those versions baked the versioned
`Cellar/wgtun/<version>/bin/wgtun` path into the plist, and `brew upgrade`
deletes that directory (0.2.2+ writes the stable `bin/wgtun` symlink).

## Troubleshooting

| Symptom | Check |
|---|---|
| TUI says `daemon offline` | press `R` in the red banner, or `sudo wgtun daemon --install`; `wgtun status` pinpoints socket vs service vs stalled reconcile |
| Endpoint log pairs `A -> B` / `B -> A` | old "retarget on every answer" behaviour; this version only switches when unhealthy. If it still flips, check the handshake and fake-IP fields in `wgtun status` |
| Endpoint resolves to `198.18.x.x` | fake-IP upstream; reinstall with `WGTUN_ENDPOINT_RESOLVER=doh:https://dns.alidns.com/dns-query` |
| Linux did not use the kernel backend | `lsmod \| grep wireguard`, `sudo modprobe wireguard`; force with `WGTUN_BACKEND=kernel` (it falls back and logs if unavailable) |
| Linux TUI cannot reach the socket | install with `sudo` so the invoking user's group is baked in, or `sudo env WGTUN_SOCKET_GROUP=$(id -gn) wgtun daemon --install` |
| Daemon stops starting after `brew upgrade` | `sudo wgtun daemon --install` — 0.2.1 and earlier wrote a versioned `Cellar/...` path into the plist |
| Instance up but no handshake | Logs tab, or `/var/log/wgtun/events.jsonl`; check endpoint DNS |
| Instance keeps restarting | Logs tab shows the reason; backoff pauses after 5 failures |
| Log says a peer `still absent from the device ... giving up` | The peer's public key equals the instance's own public key; wireguard-go accepts it with `errno=0` and silently drops the peer |
| A peer never gets an endpoint while DNS is down at boot | Expected: the daemon retries the one-peer push, and the warning repeats until the resolver answers |
| Tunnel dies after the remote IP changed, no `dns changed` event | DDNS record TTL is above 60s, so the resolver still serves the old answer; lower it. A healthy tunnel deliberately holds its current endpoint even if DNS moved; if the endpoint is a literal `ip:port` it is never re-resolved by design |
| Port conflict on save | The editor names the conflicting instance or host process |

## Development

```bash
go build -o wgtun ./cmd/wgtun
sudo env WGTUN_CONF_DIR=$PWD/conf ./wgtun daemon   # sandboxed run

go test ./...                                  # unit + integration tests (macOS)
GOOS=linux GOARCH=amd64 go build ./...         # Linux cross-build
GOOS=linux GOARCH=amd64 go vet ./...
```

Every location is overridable via `WGTUN_*` environment variables
(`internal/paths`), so the daemon runs fully unprivileged in tests:
`WGTUN_CONF_DIR WGTUN_STATE_DIR WGTUN_RUN_DIR WGTUN_LOG_DIR WGTUN_SOCK
WGTUN_WIREGUARD_GO`.

## Migrating from the v1 shell stack

Earlier versions of this project deployed per-interface launchd plists
(`com.wireguard.*`), a `healthcheck.sh` and sleepwatcher hooks. Stop them
before running wgtun — both would fight over the same tunnels:

```bash
# 1. stop the old jobs and remove their plists
for l in com.wireguard.wg0 com.wireguard.wg1 com.wireguard.healthcheck com.wireguard.ui; do
  sudo launchctl bootout system/$l 2>/dev/null; done
sudo rm -f /Library/LaunchDaemons/com.wireguard.*.plist
brew services stop sleepwatcher

# 2. kill leftover v1 processes and stale runtime files
sudo pkill -f 'wg-quick up wg'; sudo pkill -f 'wireguard-go utun'
sudo rm -rf /var/run/wireguard

# 3. start the new world (configs are reused as-is)
sudo wgtun daemon --install
wgtun
```

On Linux, disable any `wg-quick@<if>` unit for the same config first
(`sudo systemctl disable --now wg-quick@wg0`); wgtun creates its own
`wgtun-<instance>` interface and would otherwise fight the old service.

## License

MIT
