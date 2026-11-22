//go:build linux

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dncore/wgtun/internal/paths"
)

const (
	systemdUnitName = "wgtun.service"
	systemdUnitPath = "/etc/systemd/system/" + systemdUnitName
)

// installService writes the systemd unit and (re)starts it. Must run as root.
func installService() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (sudo)")
	}
	// ExecutablePath keeps a stable symlink (e.g. Homebrew's bin/wgtun)
	// instead of the versioned target /proc/self/exe would report.
	exe := paths.ExecutablePath()
	if err := os.MkdirAll(paths.LogDir, 0o755); err != nil {
		return err
	}
	unit := systemdUnit(exe, serviceEnv())
	if err := os.WriteFile(systemdUnitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return fmt.Errorf("%v (unit written to %s)", err, systemdUnitPath)
	}
	// Undo a mask installed by another tool before enabling.
	_ = systemctl("unmask", systemdUnitName)
	if err := systemctl("enable", systemdUnitName); err != nil {
		return err
	}
	if err := systemctl("restart", systemdUnitName); err != nil {
		return err
	}
	persistServiceEnv(serviceEnv())
	fmt.Printf("installed and started %s (%s daemon)\n", systemdUnitName, exe)
	if pairs := serviceEnv(); len(pairs) > 0 {
		fmt.Printf("environment: %s\n", strings.Join(pairs, " "))
	}
	// Give it a moment, then report the unit's own verdict instead of
	// pretending success and letting the TUI discover the failure.
	time.Sleep(time.Second)
	out, _ := exec.Command("systemctl", "is-active", systemdUnitName).Output()
	state := strings.TrimSpace(string(out))
	if state != "active" {
		return fmt.Errorf("unit is %q; inspect with: journalctl -u %s -n 30", state, systemdUnitName)
	}
	fmt.Printf("service: %s\n", state)
	return nil
}

// uninstallService stops and removes the unit, then reaps the devices the
// daemon left running (KillMode=process deliberately lets them survive a
// restart so the next daemon can adopt them).
func uninstallService() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (sudo)")
	}
	_ = systemctl("disable", "--now", systemdUnitName)
	time.Sleep(500 * time.Millisecond)
	killOrphans()
	if err := os.Remove(systemdUnitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = systemctl("daemon-reload")
	fmt.Printf("removed %s\n", systemdUnitName)
	return nil
}

// systemdUnit renders the unit. KillMode=process is the analogue of
// launchd's AbandonProcessGroup: restarting the daemon must not tear down the
// tunnels it is about to adopt. StartLimitIntervalSec=0 keeps systemd trying
// forever, including after the self-watchdog deliberately exits a wedged
// daemon.
func systemdUnit(exe string, env []string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=wgtun WireGuard orchestrator daemon\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("StartLimitIntervalSec=0\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + systemdArg(exe) + " daemon\n")
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=2\n")
	b.WriteString("KillMode=process\n")
	for _, kv := range env {
		b.WriteString("Environment=" + systemdArg(kv) + "\n")
	}
	b.WriteString("StandardOutput=append:" + filepath.Join(paths.LogDir, "daemon.out.log") + "\n")
	b.WriteString("StandardError=append:" + filepath.Join(paths.LogDir, "daemon.err.log") + "\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// systemdArg quotes one argument for a systemd unit value. Double quotes and
// backslashes are the only characters systemd treats specially inside quotes.
func systemdArg(s string) string {
	if s == "" {
		return `""`
	}
	needsQuotes := strings.ContainsAny(s, " \t\"\\")
	// % starts a unit-file specifier even inside Environment=.
	s = strings.ReplaceAll(s, "%", "%%")
	if !needsQuotes {
		return s
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
