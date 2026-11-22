// Package paths centralizes filesystem locations.
// Every location can be overridden via environment variable for development
// and tests, so the daemon and TUI can run entirely unprivileged in a sandbox.
package paths

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// SocketGroup returns the group that may connect to the control socket.
// WGTUN_SOCKET_GROUP wins; the installer bakes it into the service unit so a
// non-root TUI can reach a root daemon. Empty means root-only.
func SocketGroup() string { return os.Getenv("WGTUN_SOCKET_GROUP") }

// ExecutablePath returns the path the binary was invoked as, preserving
// symlinks where possible. os.Executable() resolves symlinks on Linux
// (/proc/self/exe), and baking a versioned Cellar/... target into a service
// unit leaves it unable to exec after the next upgrade; argv[0] keeps the
// stable symlink path instead.
func ExecutablePath() string {
	if arg0 := os.Args[0]; arg0 != "" {
		if strings.ContainsRune(arg0, os.PathSeparator) {
			if abs, err := filepath.Abs(arg0); err == nil {
				return abs
			}
		}
		if p, err := exec.LookPath(arg0); err == nil {
			return p
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return exe
}

// StateFile is the desired-state JSON path.
func StateFile() string { return filepath.Join(StateDir, "state.json") }

// EventsFile is the append-only event log path.
func EventsFile() string { return filepath.Join(LogDir, "events.jsonl") }

// SettingsFile is the TUI-side settings path (per-user).
func SettingsFile() string {
	if v := os.Getenv("WGTUN_SETTINGS"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "wgtun-settings.json"
	}
	return filepath.Join(home, ".config", "wgtun", "settings.json")
}
