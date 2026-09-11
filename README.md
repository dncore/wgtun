# wgs — wireguard-go orchestrator

A WireGuard manager for macOS built as a **Go TUI + root daemon**. It manages
any number of WireGuard instances by orchestrating `wireguard-go` processes,
so you never shell out to `wg`, `wg-quick` or juggle per-interface launchd
plists again.

- **TUI** (bubbletea): dashboard, instance CRUD, config editor with port
  conflict detection, log replay + live follow. 中英双语 (English default,
  switchable in Settings).
- **Daemon** (launchd, root): supervises `wireguard-go` child processes,
  pushes configs over the native UAPI protocol, sets addresses/routes, adopts
  instances after a daemon crash, and auto-restarts with backoff.
- **brew** distribution: `brew install dncore/tap/wgs`, services via launchd.
- Structured event log (`/var/log/wgs/events.jsonl`) with rotation, instead of
  un-timestamped stderr dumps.

> Replaces the v1 shell-script stack (`install.sh` + per-interface plists +
> `healthcheck.sh` + `sleepwatcher`). Migration notes at the bottom.
> The v1 stack's structural problems — zombie `wireguard-go` processes,
> `already exists` deadlocks, racy startup — are not mitigations here but
> impossible by design: the daemon owns every child process, tracks them via
> per-instance runtime dirs, and reconciles state continuously.

## Install

```bash
brew install dncore/tap/wgs   # requires wireguard-go (auto-depends)
sudo wgs daemon --install     # install + start the launchd daemon
wgs                           # open the TUI
```

Or run the daemon in the foreground for development:

```bash
sudo ./wgs daemon
```

Configs live in `/usr/local/etc/wireguard/*.conf` (wg-quick compatible).
On first start the daemon imports whatever is already there.

## TUI

Five tabs (`tab` to cycle):

| Tab | What it does |
|-----|--------------|
| Dashboard | running/total/online/traffic tiles, per-instance cards with handshake age coloring and rx sparklines |
| Instances | table of instances; `u/d` start-stop, `r` restart, `enter` live detail, `n` new, `e` edit, `x` delete (confirm), `b` toggle boot autostart |
| Logs | history with filters (`i` instance, `l` level, `text`), `f` live follow |
| Settings | language switch (`l`, persisted), daemon info |

The editor generates keys (`g`), and validates on save: CIDR → key → **ListenPort
conflicts with other configs or with a port already bound on the host** (a
pending conflict blocks the save with an inline error; `ctrl+f` force-saves).

Language is per-user, persisted to `~/.config/wgs/settings.json`. Default:
English.

## How it works

```
┌─────────────┐  HTTP over unix socket   ┌───────────────────────────────┐
│ wgs (TUI)   │◄────────────────────────►│ wgs daemon — com.wgs.daemon   │
│ bubbletea   │  /var/run/wgs.sock       │   supervisor → wireguard-go ×N│
│ your user   │  (root:admin 0660)       │   wgctrl UAPI set/get         │
└─────────────┘                          │   reconcile · adopt · backoff │
                                         │   state (enabled) + event log │
                                         └───────────────────────────────┘
```

- **Per-instance bookkeeping**: each `wireguard-go` writes its kernel-chosen
  tun name into `/var/run/wgs/<name>/` (`WG_TUN_NAME_FILE`) plus a pid file,
  so the daemon can adopt, probe and clean up each instance individually.
  (wireguard-go itself hardcodes its UAPI socket directory to
  `/var/run/wireguard/<tun>.sock` on darwin; socket names are per-tun so
  they never collide.) Note: on darwin wireguard-go forks once and the
  launcher exits, so the daemon treats the **holder of the UAPI socket**
  (`lsof -t <tun>.sock`) as the authoritative instance pid, not the pid
  returned by `exec`.
- **Autostart**: enabled instances come up on boot (daemon's `RunAtLoad`);
  a clean-shutdown marker distinguishes a reboot (apply autostart) from a
  plain daemon restart (respect what you stopped).
- **Self-heal**: reconcile probes each running instance over UAPI every 5s;
  a dead process or wedged socket is cleaned up (kill + runtime dir) and
  restarted with backoff (5 strikes in 10min → 5min pause). DNS that was
  unresolved at start is re-resolved and re-pushed later.
- **Crash adoption**: if the daemon itself dies, `wireguard-go` children
  keep running; on restart the daemon adopts them by pid + UAPI liveness.
- **Conflicts**: `ListenPort` collisions between configs and with other
  host processes are checked on save (and refuse, unless forced).

### Config format

Full wg-quick compatible parsing, including `PostUp` / `PostDown` hooks and
multiple `Address =` lines. Two deliberate deviations:

- **DNS is parsed, preserved, but not applied** to the system resolver
  (changing global DNS on macOS is invasive; apply via `PostUp` if you need it).
- `Table =` is passed through but ignored.

## Daemon ops

```bash
sudo wgs daemon --install     # install + start com.wgs.daemon
sudo wgs daemon --uninstall   # stop + remove
sudo launchctl list | grep wgs
sudo wg show all              # still works
```

State (which instances are enabled / desired-running) persists in
`/var/lib/wgs/state.json`. Runtime data lives in `/var/run/wgs/`, events in
`/var/log/wgs/`.

## Troubleshooting

| Symptom | Check |
|---------|-------|
| TUI says daemon offline | `sudo wgs daemon --install`; socket at `/var/run/wgs.sock` |
| Instance stuck with no handshake | `/var/log/wgs/events.jsonl` (or TUI Logs tab); check endpoint DNS |
| Something else broken | `go test ./...`, TUI Logs `f` follow |

## Development

```bash
go build . && sudo env WGS_CONF_DIR=$PWD/conf ./wgs daemon   # sandboxed run
```

All locations can be overridden with `WGS_*` env vars (see
`internal/paths`), so the daemon runs fully unprivileged for tests:
`WGS_CONF_DIR WGS_STATE_DIR WGS_RUN_DIR WGS_LOG_DIR WGS_SOCK WGS_WIREGUARD_GO`.

## Migration from v1 (shell scripts)

The old stack deployed `com.wireguard.{wg0,wg1,healthcheck,ui}` plists,
`sleepwatcher`, and shells in `~/scripts/wireguard-cli/`. It can keep running
alongside wgs IF it is stopped first (both would fight over the same utun
interfaces).

```bash
# 1. stop the old daemons and remove their plists
for l in com.wireguard.wg0 com.wireguard.wg1 com.wireguard.healthcheck com.wireguard.ui; do
  sudo launchctl bootout system/$l 2>/dev/null; done
sudo rm -f /Library/LaunchDaemons/com.wireguard.*.plist

# 2. stop sleepwatcher (its wake hook no longer applies)
brew services stop sleepwatcher

# 3. kill leftover v1 processes and stale runtime files
sudo pkill -f 'wg-quick up wg'; sudo pkill -f 'wireguard-go utun'
sudo rm -rf /var/run/wireguard

# 4. your configs are reused as-is
ls /usr/local/etc/wireguard/

# 5. start the new world
sudo wgs daemon --install
wgs
```

## License

MIT