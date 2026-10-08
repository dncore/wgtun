# wgtun

**English** | [简体中文](README.zh-CN.md)

[![Release](https://img.shields.io/github/v/release/dncore/wgtun)](https://github.com/dncore/wgtun/releases)
[![License](https://img.shields.io/github/license/dncore/wgtun)](LICENSE)

WireGuard orchestrator for macOS — a terminal UI plus a root launchd daemon
that manage any number of `wireguard-go` instances for you. No `wg-quick`
scripts, no per-interface plists, no kernel extensions.

```
wgtun                 open the terminal UI
wgtun daemon          run the privileged supervisor (launchd runs this)
```

## Why

macOS has no in-kernel WireGuard; everyone runs the userspace
`wireguard-go` binary. Managing more than one tunnel with the stock tools
means hand-rolled shell scripts, per-interface launchd jobs and a pile of
shared runtime files that rot in interesting ways (stale locks, zombie
backends, `already exists` deadlocks on restart).

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
| Boot autostart | per-interface launchd plists | per-instance flag |
| Crash recovery | usually none | adopt + self-heal + backoff |
| Endpoint DNS | resolved once at start, then stale for good | re-resolved and retargeted in place (DDNS) |
| Live state | `wg show` parsing | native UAPI, structured data |
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

    r refresh   ctrl+q quit
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
sudo wgtun daemon --install     # install + start the launchd daemon
wgtun                           # open the TUI
```

Configs live in `/usr/local/etc/wireguard/*.conf` (wg-quick compatible).
Existing files are imported on first start; nothing to migrate.

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
             your user (no sudo)                       root (launchd)

 ┌─────────────────────────────┐            ┌─────────────────────────────────────┐
 │  wgtun — terminal UI        │            │  wgtun daemon — com.wgtun.daemon    │
 │                             │   HTTP     │                                     │
 │  Dashboard  Instances       │◄─over unix─►│  supervisor / reconcile loop (5s)  │
 │  Editor     Logs  Settings  │   socket   │    ├── wireguard-go → utun0        │
 │                             │ /var/run/  │    ├── wireguard-go → utun7        │
 │  reads live state, edits    │ wgtun.sock │    └── ... any number of instances │
 │  configs, follows logs      │ (root:admin│                                     │
 │                             │  0660)     │  native UAPI: set/get config        │
 └─────────────────────────────┘            │  ifconfig / route for addresses     │
                                            │  adopt · backoff · DNS re-resolve   │
                                            │   ├─ not ready at boot              │
                                            │   └─ DDNS name moved (60s)          │
                                            │  /var/lib/wgtun/state.json          │
                                            │  /var/log/wgtun/events.jsonl        │
                                            └─────────────────────────────────────┘
```

- **The daemon owns the processes.** Each instance is a `wireguard-go`
  process launched with its own bookkeeping directory
  (`/var/run/wgtun/<name>/`: pid file + kernel-chosen tun name). Config is
  pushed over the native UAPI protocol; addresses, MTU and routes are
  applied with `ifconfig`/`route` exactly like wg-quick does.
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
- **Endpoint DNS is followed, not frozen.** An endpoint written as a hostname
  is re-resolved on a schedule and retargeted in place when the answer moves,
  so a DDNS name stays a live address instead of a value frozen at boot —
  see [Endpoint DNS following](#endpoint-dns-following-ddns).
- **Crash adoption.** If the daemon itself dies, the wireguard-go
  processes keep running; the next daemon start adopts them by pid +
  socket liveness — tunnels are not interrupted by daemon restarts. That
  depends on the plist asking launchd to spare our process group
  (`AbandonProcessGroup`, or `launchctl bootout` takes the tunnels down
  with the job); the price is that `daemon --uninstall` reaps them itself,
  verifying each pid still runs wireguard-go before signalling it.
- **Boot semantics.** Enabled instances come up at boot. A clean-shutdown
  marker distinguishes a reboot (apply autostart) from a plain daemon
  restart (respect whatever you had stopped).
- **One privileged component.** You never run `wgtun` with `sudo`; the TUI
  talks to the root daemon over a unix socket. Config files stay
  root-owned.

> Implementation note: on darwin wireguard-go forks once and the launcher
> process exits, so the daemon considers the **holder of the UAPI socket**
> (`lsof -t /var/run/wireguard/<tun>.sock`) to be the authoritative
> instance pid, not the value returned by `exec`.

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
  │      live endpoint equals any candidate? ── yes ──► silent no-op
  │                         │ no
  └──────────────────────► set=1 public_key=<peer> endpoint=<new ip:port>
                           (no replace_peers) ──► INFO endpoint old -> new
```

**Two lanes (not to scale).** A healthy tunnel is re-checked every 60 seconds —
about the shortest TTL a DDNS record realistically carries, so a cached answer
is not interrogated faster than it can change. A peer whose handshake has gone
older than 150 seconds (more than one rekey period: the signature of a name
that now points at a dead address) is re-checked on a 15 second floor instead,
so recovery does not wait out the interval. Peers that never handshaked stay on
the slow lane on purpose: an idle peer is indistinguishable from an unreachable
one, and guessing would hold the fast lane open forever.

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

Recovery therefore lands anywhere between ~20 seconds and TTL + ~1 minute,
depending on how much of the cached answer's lifetime was left when the address
changed. Two ways to remove the wait: keep the TTL at 60s or lower, or take DNS
out of the reverse direction entirely by putting `PersistentKeepalive = 25` on
the remote peer that faces this machine — its packets then arrive from the new
address and WireGuard's own roaming retargets the endpoint in about 25 seconds,
with no lookup involved. That second option also covers the case no amount of
re-resolving can: a DDNS record that never gets updated at all.

## Configuration

Fully wg-quick compatible parsing: multiple `Address` lines, `DNS`,
`MTU`, `Table`, `PostUp`/`PostDown` (executed on up/down). Two deliberate
deviations:

- **DNS is parsed and preserved but not applied** to the system resolver —
  changing global DNS on macOS is invasive. Use a `PostUp` hook if you
  need it.
- `Table` is accepted and ignored.

Conflicts are checked when you save in the editor: `ListenPort` clashes
with other wgtun instances and with ports already bound on the host are
refused (force-save overrides).

## Daemon operations

```bash
sudo wgtun daemon --install     # install + start com.wgtun.daemon
sudo wgtun daemon --uninstall   # stop + remove
sudo wgtun daemon               # run in the foreground (debugging)
sudo launchctl list | grep wgtun
sudo wg show all                # still works
```

| Data | Location |
|---|---|
| Wiring configs | `/usr/local/etc/wireguard/*.conf` |
| Desired state (autostart / stopped) | `/var/lib/wgtun/state.json` |
| Runtime (sockets, pids) | `/var/run/wgtun/` |
| Event log | `/var/log/wgtun/events.jsonl` (rotated) |
| TUI settings | `~/.config/wgtun/settings.json` |

## Releasing

Pushing a `v*` tag triggers the release workflow: tests run, binaries
build for darwin/amd64 + arm64, a GitHub release is created, and
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
| TUI says `daemon offline` | `sudo wgtun daemon --install`; socket at `/var/run/wgtun.sock` |
| Daemon stops starting after `brew upgrade` | `sudo wgtun daemon --install` — 0.2.1 and earlier wrote a versioned `Cellar/...` path into the plist |
| Instance up but no handshake | Logs tab, or `/var/log/wgtun/events.jsonl`; check endpoint DNS |
| Instance keeps restarting | Logs tab shows the reason; backoff pauses after 5 failures |
| Log says a peer `still absent from the device ... giving up` | The peer's public key equals the instance's own public key; wireguard-go accepts it with `errno=0` and silently drops the peer |
| A peer never gets an endpoint while DNS is down at boot | Expected: the daemon retries the one-peer push, and the warning repeats until the resolver answers |
| Tunnel dies after the remote IP changed, no `dns changed` event | DDNS record TTL is above 60s, so the resolver still serves the old answer; lower it. If the endpoint is written as a literal `ip:port` in the config it is never re-resolved by design |
| Port conflict on save | The editor names the conflicting instance or host process |

## Development

```bash
go build -o wgtun ./cmd/wgtun
sudo env WGTUN_CONF_DIR=$PWD/conf ./wgtun daemon   # sandboxed run

go test ./...           # unit + integration tests
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

## License

MIT
