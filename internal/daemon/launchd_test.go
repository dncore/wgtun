package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The plist must ask launchd to leave our process group alone: wireguard-go
// runs inside it, so without this key every bootout / KeepAlive restart takes
// the tunnels down with the daemon and the next daemon has nothing to adopt.
func TestLaunchdPlistAbandonsProcessGroup(t *testing.T) {
	plist := launchdPlist("/opt/homebrew/bin/wgtun")
	for _, want := range []string{
		"<key>AbandonProcessGroup</key>",
		"<key>KeepAlive</key>",
		"<string>/opt/homebrew/bin/wgtun</string>",
		"<string>daemon</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q:\n%s", want, plist)
		}
	}
	// exactly one <true/> for the abandon key, and it must not be malformed
	if strings.Count(plist, "<key>AbandonProcessGroup</key>\n\t<true/>") != 1 {
		t.Errorf("AbandonProcessGroup must be a single <true/> value:\n%s", plist)
	}

	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not available")
	}
	path := filepath.Join(t.TempDir(), "com.wgtun.daemon.plist")
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil -lint rejected the plist: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// lintPlist writes a plist and asks plutil to validate it.
func lintPlist(t *testing.T, plist string) {
	t.Helper()
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not available")
	}
	path := filepath.Join(t.TempDir(), "com.wgtun.daemon.plist")
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil -lint rejected the plist: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// `daemon --install` regenerates the plist from the template, so knobs set by
// hand there would be silently dropped on the next install or upgrade: the ones
// a user sets in the installing shell are baked in instead.
func TestLaunchdPlistBakesEnvironment(t *testing.T) {
	t.Setenv("WGTUN_RECONCILE_INTERVAL", "")
	if p := launchdPlist("/opt/homebrew/bin/wgtun"); strings.Contains(p, "EnvironmentVariables") {
		t.Errorf("nothing set, so the plist must not declare an environment:\n%s", p)
	}

	t.Setenv("WGTUN_RECONCILE_INTERVAL", "30s")
	p := launchdPlist("/opt/homebrew/bin/wgtun")
	for _, want := range []string{
		"<key>EnvironmentVariables</key>",
		"<key>WGTUN_RECONCILE_INTERVAL</key>",
		"<string>30s</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
	if pairs, _ := launchdEnv(); len(pairs) != 1 || pairs[0] != "WGTUN_RECONCILE_INTERVAL=30s" {
		t.Errorf("launchdEnv = %v", pairs)
	}
	lintPlist(t, p)
}

// A value with XML metacharacters must not be able to break the plist.
func TestLaunchdPlistEscapesEnvironment(t *testing.T) {
	t.Setenv("WGTUN_RECONCILE_INTERVAL", "a&b<c")
	p := launchdPlist("/opt/homebrew/bin/wgtun")
	if !strings.Contains(p, "<string>a&amp;b&lt;c</string>") {
		t.Errorf("value not escaped:\n%s", p)
	}
	lintPlist(t, p)
}

func TestReadPidFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "pid")
	if err := os.WriteFile(good, []byte("4321\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readPidFile(good); got != 4321 {
		t.Errorf("readPidFile = %d want 4321", got)
	}
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("not-a-pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readPidFile(bad); got != 0 {
		t.Errorf("readPidFile on garbage = %d want 0", got)
	}
	if got := readPidFile(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("readPidFile on missing file = %d want 0", got)
	}
}

// The uninstall reaper must only ever signal pids it can positively identify,
// so a recycled pid from an unrelated process is never killed.
func TestIsWireGuardGoRejectsForeignPid(t *testing.T) {
	if isWireGuardGo(os.Getpid()) {
		t.Error("the test binary is not wireguard-go")
	}
	if isWireGuardGo(0) || isWireGuardGo(-1) {
		t.Error("invalid pids must not be identified as wireguard-go")
	}
}

func TestIsWireGuardGoMatchesSelf(t *testing.T) {
	// /bin/sleep is not wireguard-go either, but it proves the ps lookup path
	// works on this host rather than failing open.
	cmd := exec.Command("/bin/sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a probe process: %v", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	if isWireGuardGo(cmd.Process.Pid) {
		t.Errorf("pid %d (sleep) must not be identified as wireguard-go", cmd.Process.Pid)
	}
	if _, err := exec.Command("/bin/ps", "-o", "comm=", "-p", strconv.Itoa(cmd.Process.Pid)).Output(); err != nil {
		t.Fatalf("ps lookup broke, so the check above is meaningless: %v", err)
	}
}
