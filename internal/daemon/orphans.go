package daemon

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dncore/wgtun/internal/paths"
)

// killOrphans terminates the WireGuard devices a previous daemon left behind
// and clears their runtime files. It is used by --uninstall, where the service
// manager deliberately leaves the process group alone so a daemon restart can
// adopt the tunnels; uninstalling is the one case that must clean them up.
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
		// Remove endpoint host routes the daemon pinned; a root-level route
		// outliving the service is exactly the kind of rot this project exists
		// to prevent.
		if data, err := os.ReadFile(filepath.Join(dir, "pins")); err == nil {
			for _, field := range strings.Fields(string(data)) {
				if ip, err := netip.ParseAddr(field); err == nil {
					unpinEndpoint(ip)
				}
			}
		}
		os.RemoveAll(dir)
	}
	if killed > 0 {
		time.Sleep(300 * time.Millisecond) // let the device processes exit
	}
	for _, tun := range tuns {
		os.Remove(filepath.Join(paths.WireGuardSockDir, tun+".sock"))
		removeTunLink(tun) // kernel devices outlive the daemon process
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
