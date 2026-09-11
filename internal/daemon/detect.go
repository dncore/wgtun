package daemon

import (
	"os"
	"os/exec"
	"strings"

	"github.com/dncore/wgtun/internal/logs"
	"github.com/dncore/wgtun/internal/paths"
)

// detectWireGuardGo verifies the userspace implementation the daemon will
// orchestrate. Detected once at start; the result is surfaced through
// /state so the TUI can guide the user instead of failing at first start.
func detectWireGuardGo(ev *logs.Store) (path string, ok bool, version string) {
	path = paths.WireGuardGo
	if _, err := os.Stat(path); err != nil {
		ev.Error("", "wireguard-go not found at %s — install it with `brew install wireguard-go`, or set WGTUN_WIREGUARD_GO to its location", path)
		return path, false, ""
	}
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		ev.Warn("", "wireguard-go found at %s (version check failed: %v)", path, err)
		return path, true, ""
	}
	version = strings.TrimSpace(string(out))
	ev.Info("", "wireguard-go: %s (%s)", version, path)
	return path, true, version
}
