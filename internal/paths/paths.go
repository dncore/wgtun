// Package paths centralizes filesystem locations.
// Every location can be overridden via environment variable for development
// and tests, so the daemon and TUI can run entirely unprivileged in a sandbox.
package paths

import (
	"os"
	"path/filepath"
)


func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var (
	// ConfDir holds the wg-quick compatible *.conf files (one instance per file).
	ConfDir = env("WGS_CONF_DIR", "/usr/local/etc/wireguard")
	// StateDir holds desired-state persistence.
	StateDir = env("WGS_STATE_DIR", "/var/lib/wgs")
	// RunDir holds per-instance runtime dirs (UAPI socket, tun name, pid).
	RunDir = env("WGS_RUN_DIR", "/var/run/wgs")
	// LogDir holds daemon logs.
	LogDir = env("WGS_LOG_DIR", "/var/log/wgs")
	// SocketPath is the Unix socket shared by daemon and TUI.
	SocketPath = env("WGS_SOCK", "/var/run/wgs.sock")
	// WireGuardGo is the wireguard-go binary the daemon orchestrates.
	WireGuardGo = env("WGS_WIREGUARD_GO", "/opt/homebrew/bin/wireguard-go")
)

// StateFile is the desired-state JSON path.
func StateFile() string { return filepath.Join(StateDir, "state.json") }

// EventsFile is the append-only event log path.
func EventsFile() string { return filepath.Join(LogDir, "events.jsonl") }

// SettingsFile is the TUI-side settings path (per-user).
func SettingsFile() string {
	if v := os.Getenv("WGS_SETTINGS"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "wgs-settings.json"
	}
	return filepath.Join(home, ".config", "wgs", "settings.json")
}
