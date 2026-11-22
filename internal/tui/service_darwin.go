//go:build darwin

package tui

import (
	"fmt"
	"os/exec"
	"strings"
)

// serviceStatus reports whether the LaunchDaemon is loaded. `launchctl
// print` is readable by an admin user (verified), so the TUI can tell "job
// booted out" from "job running but socket broken" without root.
func serviceStatus() string {
	out, err := exec.Command("launchctl", "print", "system/com.wgtun.daemon").Output()
	if err != nil {
		return "not loaded"
	}
	var state, pid string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "state = "); ok {
			state = v
		}
		if v, ok := strings.CutPrefix(line, "pid = "); ok {
			pid = v
		}
	}
	if state == "" {
		state = "loaded"
	}
	if pid != "" {
		return fmt.Sprintf("%s (pid %s)", state, pid)
	}
	return state
}
