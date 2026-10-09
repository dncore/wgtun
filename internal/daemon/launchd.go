package daemon

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	if err := launchctl("bootstrap", "system", dst); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	fmt.Printf("installed and started %s (%s daemon)\n", launchdLabel, exe)
	if pairs, _ := launchdEnv(); len(pairs) > 0 {
		fmt.Printf("environment: %s\n", strings.Join(pairs, " "))
	}
	return nil
}

// UninstallLaunchd removes the LaunchDaemon and the tunnels it was running.
func UninstallLaunchd() error {
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

// killOrphans terminates the wireguard-go processes a previous daemon left
// behind and clears their runtime files.
func killOrphans() {
	entries, err := os.ReadDir(paths.RunDir)
	if err != nil {
		return
	}
	tuns := make([]string, 0, len(entries))
	killed := 0
	for _, e := range entries {
		dir := filepath.Join(paths.RunDir, e.Name())
		if tun, err := os.ReadFile(filepath.Join(dir, "tun.name")); err == nil {
			if name := strings.TrimSpace(string(tun)); name != "" {
				tuns = append(tuns, name)
			}
		}
		if pid := readPidFile(filepath.Join(dir, "pid")); pid > 0 && isWireGuardGo(pid) {
			// sigterm only: never kill a pid we cannot positively identify
			syscall.Kill(pid, syscall.SIGTERM)
			killed++
		}
		os.RemoveAll(dir)
	}
	if killed > 0 {
		time.Sleep(300 * time.Millisecond) // let the device processes exit
	}
	for _, tun := range tuns {
		os.Remove(filepath.Join(paths.WireGuardSockDir, tun+".sock"))
	}
	if killed > 0 {
		fmt.Printf("stopped %d wireguard-go instance(s)\n", killed)
	}
}

// readPidFile reads a daemon-written pid file, 0 when absent or malformed.
func readPidFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// isWireGuardGo reports whether a pid currently runs wireguard-go, so the
// daemon never signals a pid that was recycled by another process. It is used
// both by the uninstall reaper and by cleanupLocked, which runs as root.
func isWireGuardGo(pid int) bool {
	out, err := exec.Command("/bin/ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(out))
	if comm == "" {
		return false
	}
	// The configured path may be a custom build; the basename check covers a
	// renamed installation of the binary we actually launch.
	return comm == paths.WireGuardGo || strings.Contains(filepath.Base(comm), "wireguard-go")
}

// launchdEnvKeys are the environment variables baked into the plist when they
// are set while installing. `daemon --install` regenerates the plist from this
// template, so anything set by hand there would be silently dropped on the next
// install or upgrade.
var launchdEnvKeys = []string{
	"WGTUN_RECONCILE_INTERVAL",
}

// launchdEnv returns the KEY=VALUE pairs to bake into the plist, in a stable
// order, and the XML fragment declaring them (empty when nothing is set).
func launchdEnv() ([]string, string) {
	var pairs []string
	var frag strings.Builder
	for _, k := range launchdEnvKeys {
		v := os.Getenv(k)
		if v == "" {
			continue
		}
		pairs = append(pairs, k+"="+v)
		frag.WriteString("\t\t<key>")
		xmlEscape(&frag, k)
		frag.WriteString("</key>\n\t\t<string>")
		xmlEscape(&frag, v)
		frag.WriteString("</string>\n")
	}
	if len(pairs) == 0 {
		return nil, ""
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
