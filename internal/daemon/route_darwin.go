//go:build darwin

package daemon

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
)

// pinEndpoint adds a host route to ip through the interface/gateway the host
// currently uses to reach it. -inet/-inet6 mirrors what wg-quick does.
func pinEndpoint(ip netip.Addr) error {
	out, err := exec.Command("/sbin/route", "-n", "get", ip.String()).Output()
	if err != nil {
		return fmt.Errorf("route get: %v", err)
	}
	gateway, iface := parseRouteGet(string(out))
	// When a full-tunnel config is already up, the endpoint itself resolves
	// into the tunnel; pinning through that would be a loop. Fall back to the
	// host's real default route instead.
	if strings.HasPrefix(iface, "utun") {
		if def, err := exec.Command("/sbin/route", "-n", "get", "default").Output(); err == nil {
			gateway, iface = parseRouteGet(string(def))
		}
	}
	args := []string{"-q", "-n", "add"}
	if ip.Is6() {
		args = append(args, "-inet6")
	} else {
		args = append(args, "-inet")
	}
	args = append(args, ip.String())
	switch {
	case gateway != "" && gateway != ip.String():
		args = append(args, "-gateway", gateway)
	case iface != "":
		args = append(args, "-interface", iface)
	default:
		return fmt.Errorf("no route to %s", ip)
	}
	if out, err := exec.Command("/sbin/route", args...).CombinedOutput(); err != nil {
		if strings.Contains(string(out), "File exists") {
			return nil
		}
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func unpinEndpoint(ip netip.Addr) {
	family := "-inet"
	if ip.Is6() {
		family = "-inet6"
	}
	_ = exec.Command("/sbin/route", "-q", "-n", "delete", family, ip.String()).Run()
}
