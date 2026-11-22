//go:build linux

package tui

import (
	"fmt"
	"os/exec"
	"strings"
)

// serviceStatus reports the systemd unit state, readable without root.
func serviceStatus() string {
	out, err := exec.Command("systemctl", "show", "-p", "ActiveState", "-p", "MainPID", "--value", "wgtun").Output()
	if err != nil {
		return "not loaded"
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "not loaded"
	}
	state := fields[0]
	if len(fields) > 1 && fields[1] != "0" {
		return fmt.Sprintf("%s (pid %s)", state, fields[1])
	}
	return state
}
