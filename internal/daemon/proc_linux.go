//go:build linux

package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/dncore/wgtun/internal/paths"
)

// socketPid returns the pid holding the given UAPI socket, 0 if none.
// On Linux the peer credentials of a connected AF_UNIX socket carry the
// server's pid, so no lsof dependency is needed.
func socketPid(sockPath string) int {
	if sockPath == "" {
		return 0
	}
	c, err := net.Dial("unix", sockPath)
	if err != nil {
		return 0
	}
	defer c.Close()
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0
	}
	var pid int32
	_ = raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			pid = cred.Pid
		}
	})
	return int(pid)
}

// isWireGuardGo reports whether a pid currently runs wireguard-go. /proc/comm
// is truncated to 15 characters, which still covers the binary name.
func isWireGuardGo(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(data))
	if comm == "" {
		return false
	}
	return comm == filepath.Base(paths.WireGuardGo) || strings.Contains(comm, "wireguard-go")
}
