//go:build linux

package paths

var (
	// ConfDir is the standard wg-quick location on Linux.
	ConfDir = env("WGTUN_CONF_DIR", "/etc/wireguard")
	// StateDir holds desired-state persistence.
	StateDir = env("WGTUN_STATE_DIR", "/var/lib/wgtun")
	// RunDir holds per-instance runtime dirs. /run is tmpfs, so runtime
	// identity is also derivable from the instance name on a cold start.
	RunDir = env("WGTUN_RUN_DIR", "/run/wgtun")
	// LogDir holds daemon logs.
	LogDir = env("WGTUN_LOG_DIR", "/var/log/wgtun")
	// SocketPath is the Unix socket shared by daemon and TUI.
	SocketPath = env("WGTUN_SOCK", "/run/wgtun.sock")
	// WireGuardGo is the userspace fallback the daemon orchestrates when the
	// kernel module is unavailable.
	WireGuardGo = env("WGTUN_WIREGUARD_GO", "/usr/bin/wireguard-go")
	// WireGuardSockDir is where wireguard-go creates its UAPI sockets on
	// Linux (<ifname>.sock); upstream hardcodes /var/run/wireguard.
	WireGuardSockDir = env("WGTUN_WG_SOCK_DIR", "/var/run/wireguard")
)
