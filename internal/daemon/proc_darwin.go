//go:build darwin

package daemon

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dncore/wgtun/internal/paths"
)

// socketPid returns the pid holding the given UAPI socket, 0 if none.
// wireguard-go may fork on darwin, so the pid returned by exec is not always
// the device process — the socket holder is the authoritative one.
func socketPid(sockPath string) int {
	if sockPath == "" {
		return 0
	}
	out, err := exec.Command("/usr/sbin/lsof", "-t", sockPath).Output()
	if err != nil {
		return 0
	}
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil {
			return pid
		}
	}
	return 0
}

// isWireGuardGo reports whether a pid currently runs wireguard-go, so the
// daemon never signals a pid that was recycled by another process.
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
