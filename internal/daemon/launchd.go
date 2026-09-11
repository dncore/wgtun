package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
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
		launchctl("bootout", "system", launchdLabel)
		time.Sleep(500 * time.Millisecond)
	}
	if err := os.WriteFile(dst, []byte(plist), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("launchctl", "bootstrap", "system", dst).CombinedOutput(); err != nil {
		return fmt.Errorf("bootstrap: %v: %s", err, out)
	}
	fmt.Printf("installed and started %s\n", launchdLabel)
	return nil
}

// UninstallLaunchd removes the LaunchDaemon.
func UninstallLaunchd() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (sudo)")
	}
	dst := filepath.Join("/Library/LaunchDaemons", launchdLabel+".plist")
	launchctl("bootout", "system", launchdLabel)
	time.Sleep(500 * time.Millisecond)
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("removed %s\n", launchdLabel)
	return nil
}

func launchctl(args ...string) {
	exec.Command("launchctl", args...).Run()
}
