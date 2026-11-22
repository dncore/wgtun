//go:build linux

package daemon

import (
	"os"
	"os/exec"
)

// kernelAvailable reports whether the in-kernel WireGuard implementation can
// create a device. It first tries to load the module, then checks the loaded
// module (which also covers a builtin), and finally attempts a throwaway
// link as the ground truth for containers and restricted namespaces.
func kernelAvailable() bool {
	modprobe := ""
	if p, err := exec.LookPath("modprobe"); err == nil {
		modprobe = p
	} else {
		for _, p := range []string{"/sbin/modprobe", "/usr/sbin/modprobe"} {
			if _, err := os.Stat(p); err == nil {
				modprobe = p
				break
			}
		}
	}
	if modprobe != "" {
		_ = exec.Command(modprobe, "wireguard").Run()
	}
	if _, err := os.Stat("/sys/module/wireguard"); err == nil {
		return true
	}
	const probe = "wgtunprobe"
	_ = exec.Command(ipPath(), "link", "del", probe).Run() // stale from a crash
	if err := exec.Command(ipPath(), "link", "add", probe, "type", "wireguard").Run(); err != nil {
		return false
	}
	_ = exec.Command(ipPath(), "link", "del", probe).Run()
	return true
}
