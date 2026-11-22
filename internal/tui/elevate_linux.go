//go:build linux

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/dncore/wgtun/internal/paths"
)

// elevateInstall runs `wgtun daemon --install` through pkexec in a graphical
// session, falling back to passwordless sudo when configured. Neither is
// assumed to exist: the manual command is returned when both fail.
func elevateInstall() error {
	exe := paths.ExecutablePath()
	var tries []string
	if p, err := exec.LookPath("pkexec"); err == nil &&
		(os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "") {
		out, err := exec.Command(p, exe, "daemon", "--install").CombinedOutput()
		if err == nil {
			return nil
		}
		tries = append(tries, fmt.Sprintf("pkexec: %v: %s", err, strings.TrimSpace(string(out))))
	}
	if err := exec.Command("sudo", "-n", "true").Run(); err == nil {
		out, err := exec.Command("sudo", "-n", exe, "daemon", "--install").CombinedOutput()
		if err == nil {
			return nil
		}
		tries = append(tries, fmt.Sprintf("sudo: %v: %s", err, strings.TrimSpace(string(out))))
	}
	msg := "run manually: sudo " + exe + " daemon --install"
	if len(tries) > 0 {
		msg += " (" + strings.Join(tries, "; ") + ")"
	}
	return fmt.Errorf("%s", msg)
}
