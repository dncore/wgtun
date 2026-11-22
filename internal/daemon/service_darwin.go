//go:build darwin

package daemon

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dncore/wgtun/internal/paths"
)

// launchdLabel is the system daemon label under which wgtun runs.
const launchdLabel = "com.wgtun.daemon"

// installService writes the LaunchDaemon plist and loads it.
// Must run as root.
func installService() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (sudo)")
	}
	// Deliberately not EvalSymlinks'd: Homebrew installs bin/wgtun as a symlink
	// into Cellar/wgtun/<version>/bin/, and baking that versioned target into
	// the plist leaves the LaunchDaemon unable to exec after `brew upgrade`
	// removes the directory — silently, until the next boot. ExecutablePath
	// keeps the symlink as invoked, which survives upgrades.
	exe := paths.ExecutablePath()
	if err := os.MkdirAll(paths.LogDir, 0o755); err != nil {
		return err
	}
	plist := launchdPlist(exe)

	dst := filepath.Join("/Library/LaunchDaemons", launchdLabel+".plist")
	if _, err := os.Stat(dst); err == nil {
		// The service-target form is required: a bare label is rejected with
		// "Boot-out failed: 5: Input/output error" and the old job stays
		// loaded, which makes the bootstrap below fail with the same error.
		// Failing here just means no job was loaded, so warn and carry on.
		if err := launchctl("bootout", "system/"+launchdLabel); err != nil {
			fmt.Printf("warning: %v\n", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := os.WriteFile(dst, []byte(plist), 0o644); err != nil {
		return err
	}
	// A service another tool disabled would make bootstrap fail with
	// "Service is disabled"; clearing the flag is part of repair.
	_ = launchctl("enable", "system/"+launchdLabel)
	if err := launchctl("bootstrap", "system", dst); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	persistServiceEnv(serviceEnv())
	fmt.Printf("installed and started %s (%s daemon)\n", launchdLabel, exe)
	if pairs, _ := launchdEnv(); len(pairs) > 0 {
		fmt.Printf("environment: %s\n", strings.Join(pairs, " "))
	}
	return nil
}

// uninstallService removes the LaunchDaemon and the tunnels it was running.
func uninstallService() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (sudo)")
	}
	dst := filepath.Join("/Library/LaunchDaemons", launchdLabel+".plist")
	launchctl("bootout", "system/"+launchdLabel)
	time.Sleep(500 * time.Millisecond)
	// The plist deliberately asks launchd to abandon our process group, so the
	// tunnels outlive a bootout. Tear them down here instead of leaving routes
	// pointing at utun devices until the next reboot.
	killOrphans()
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("removed %s\n", launchdLabel)
	return nil
}

// launchdEnv returns the KEY=VALUE pairs to bake into the plist, in a stable
// order, and the XML fragment declaring them (empty when nothing is set).
// serviceEnv merges the current environment with what the previous install
// saved, so a repair does not drop settings made by the original install.
func launchdEnv() ([]string, string) {
	pairs := serviceEnv()
	if len(pairs) == 0 {
		return nil, ""
	}
	var frag strings.Builder
	for _, kv := range pairs {
		k, v, _ := strings.Cut(kv, "=")
		frag.WriteString("\t\t<key>")
		xmlEscape(&frag, k)
		frag.WriteString("</key>\n\t\t<string>")
		xmlEscape(&frag, v)
		frag.WriteString("</string>\n")
	}
	return pairs, "\t<key>EnvironmentVariables</key>\n\t<dict>\n" + frag.String() + "\t</dict>\n"
}

// xmlEscape writes s with XML metacharacters escaped (config values are simple
// duration strings, but the plist must stay valid whatever they contain).
func xmlEscape(w io.Writer, s string) {
	_ = xml.EscapeText(w, []byte(s))
}

// launchdPlist renders the LaunchDaemon.
//
// AbandonProcessGroup is deliberate: wireguard-go runs in the daemon's process
// group, so without it launchd takes every tunnel down with the job on each
// bootout / KeepAlive restart — leaving the next daemon with nothing to adopt,
// which is exactly what crash adoption exists to prevent. The price is that
// UninstallLaunchd must reap the tunnels itself.
func launchdPlist(exe string) string {
	_, env := launchdEnv()
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>AbandonProcessGroup</key>
	<true/>
%s	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, launchdLabel, exe, env, filepath.Join(paths.LogDir, "daemon.out.log"), filepath.Join(paths.LogDir, "daemon.err.log"))
}

// launchctl runs launchctl, folding its output into the error so failures read
// as "Boot-out failed: 5: Input/output error" rather than a bare exit status.
func launchctl(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
