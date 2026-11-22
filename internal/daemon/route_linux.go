//go:build linux

package daemon

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
)

// ipPath locates the iproute2 binary; systemd's default PATH does not always
// contain /sbin on older distributions.
func ipPath() string {
	if p, err := exec.LookPath("ip"); err == nil {
		return p
	}
	for _, p := range []string{"/sbin/ip", "/usr/sbin/ip", "/usr/bin/ip", "/bin/ip"} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return "ip"
}

// pinEndpoint adds a host route to ip through the gateway/device the host
// currently uses. `replace` makes the call idempotent and updates a stale
// route after a network change.
func pinEndpoint(ip netip.Addr) error {
	out, err := exec.Command(ipPath(), "route", "get", ip.String()).Output()
	if err != nil {
		return fmt.Errorf("route get: %v", err)
	}
	gateway, dev := parseIPRouteGet(string(out))
	// A full-tunnel route makes the endpoint resolve into the tunnel itself;
	// pinning through it would loop. Use the host's real default route.
	if strings.HasPrefix(dev, "wgtun-") {
		if def, err := exec.Command(ipPath(), "route", "show", "default").Output(); err == nil {
			gateway, dev = parseIPRouteGet(string(def))
		}
	}
	family := "-4"
	cidr := ip.String() + "/32"
	if ip.Is6() {
		family = "-6"
		cidr = ip.String() + "/128"
	}
	args := []string{family, "route", "replace", cidr}
	if gateway != "" {
		args = append(args, "via", gateway)
	}
	if dev != "" {
		args = append(args, "dev", dev)
	}
	if len(args) == 4 {
		return fmt.Errorf("no route to %s", ip)
	}
	if out, err := exec.Command(ipPath(), args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func unpinEndpoint(ip netip.Addr) {
	family := "-4"
	cidr := ip.String() + "/32"
	if ip.Is6() {
		family = "-6"
		cidr = ip.String() + "/128"
	}
	_ = exec.Command(ipPath(), family, "route", "del", cidr).Run()
}
