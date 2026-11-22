//go:build linux

package daemon

import "os/exec"

// removeTunLink deletes a kernel WireGuard interface. A userspace device's
// TUN link disappears with its process, so a failed delete is expected and
// ignored.
func removeTunLink(ifname string) {
	if ifname == "" {
		return
	}
	_ = exec.Command(ipPath(), "link", "del", ifname).Run()
}
