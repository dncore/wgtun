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

// launchdLabel is the system daemon label under which wgtun runs.
const launchdLabel = "com.wgtun.daemon"

// InstallLaunchd writes the LaunchDaemon plist and load it.
// Must run as root.
func InstallLaunchd() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (sudo)")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Deliberately not EvalSymlinks'd: Homebrew installs bin/wgtun as a symlink
	// into Cellar/wgtun/<version>/bin/, and baking that versioned target into
	// the plist leaves the LaunchDaemon unable to exec after `brew upgrade`
	// removes the directory — silently, until the next boot. os.Executable()
	// returns the symlink as invoked (absolute), which survives upgrades.
	if err := os.MkdirAll(paths.LogDir, 0o755); err != nil {
		return err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
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
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, launchdLabel, exe, filepath.Join(paths.LogDir, "daemon.out.log"), filepath.Join(paths.LogDir, "daemon.err.log"))

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
	if err := launchctl("bootstrap", "system", dst); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	fmt.Printf("installed and started %s (%s daemon)\n", launchdLabel, exe)
	return nil
}

// UninstallLaunchd removes the LaunchDaemon.
func UninstallLaunchd() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (sudo)")
	}
	dst := filepath.Join("/Library/LaunchDaemons", launchdLabel+".plist")
	launchctl("bootout", "system/"+launchdLabel)
	time.Sleep(500 * time.Millisecond)
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("removed %s\n", launchdLabel)
	return nil
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
