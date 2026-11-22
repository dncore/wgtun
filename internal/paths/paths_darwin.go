//go:build darwin

package paths

var (
	// ConfDir holds the wg-quick compatible *.conf files (one instance per file).
	ConfDir = env("WGTUN_CONF_DIR", "/usr/local/etc/wireguard")
	// StateDir holds desired-state persistence.
	StateDir = env("WGTUN_STATE_DIR", "/var/lib/wgtun")
	// RunDir holds per-instance runtime dirs (pid, tun name, diagnostics).
	RunDir = env("WGTUN_RUN_DIR", "/var/run/wgtun")
	// LogDir holds daemon logs.
	LogDir = env("WGTUN_LOG_DIR", "/var/log/wgtun")
	// SocketPath is the Unix socket shared by daemon and TUI.
	SocketPath = env("WGTUN_SOCK", "/var/run/wgtun.sock")
	// WireGuardGo is the wireguard-go binary the daemon orchestrates.
	WireGuardGo = env("WGTUN_WIREGUARD_GO", "/opt/homebrew/bin/wireguard-go")
	// WireGuardSockDir is where wireguard-go itself creates its UAPI
	// sockets (<tun>.sock). Upstream hardcodes /var/run/wireguard on
	// darwin (there is no WG_UAPI_DIR); the directory is only overridable
	// here for tests.
	WireGuardSockDir = env("WGTUN_WG_SOCK_DIR", "/var/run/wireguard")
)
