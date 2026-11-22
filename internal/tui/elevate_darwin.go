//go:build darwin

package tui

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/dncore/wgtun/internal/paths"
)

// elevateInstall runs `wgtun daemon --install` through the macOS
// administrator authorization dialog. The command is fixed — no user input is
// interpolated — so this does not turn the TUI into a general sudo prompt.
func elevateInstall() error {
	exe := paths.ExecutablePath()
	// Quote for sh, then escape for the AppleScript string literal.
	shell := shellQuote(exe) + " daemon --install"
	script := "do shell script " + appleScriptString(shell) + " with administrator privileges"
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "User canceled") || strings.Contains(msg, "-128") {
			return fmt.Errorf("authorization canceled")
		}
		return fmt.Errorf("%v: %s", err, msg)
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
