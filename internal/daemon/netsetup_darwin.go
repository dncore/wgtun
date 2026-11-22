//go:build darwin

package daemon

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// setupNetwork applies addresses, MTU and routes to a utun interface,
// mirroring what wg-quick does on darwin:
//
//	ifconfig utunN inet <addr/prefix> <addr> alias   (per Address)
//	ifconfig utunN mtu <mtu>
//	ifconfig utunN up
//	route -q -n add -inet <cidr> -interface utunN    (per peer AllowedIPs)
//
// 0.0.0.0/0 and ::/0 are split into two /1s so the default route is not
// overridden (same trick wg-quick uses).
func (s *Supervisor) setupNetwork(i *inst) error {
	tun, conf := i.tun, i.conf
	for _, p := range conf.Interface.Addresses {
		proto := "inet"
		if p.Addr().Is6() {
			proto = "inet6"
		}
		if err := runCmd("ifconfig", tun, proto, p.String(), p.Addr().String(), "alias"); err != nil {
			return fmt.Errorf("address %s: %v", p, err)
		}
	}
	mtu := conf.Interface.MTU
	if mtu == 0 {
		mtu = 1420 // wg-quick darwin default
	}
	if err := runCmd("ifconfig", tun, "mtu", strconv.Itoa(mtu)); err != nil {
		return fmt.Errorf("mtu: %v", err)
	}
	if err := runCmd("ifconfig", tun, "up"); err != nil {
		return fmt.Errorf("up: %v", err)
	}
	for _, peer := range conf.Peers {
		for _, ap := range peer.AllowedIPs {
			for _, r := range splitDefaultRoute(ap) {
				flag := "-inet"
				if ap.Addr().Is6() {
					flag = "-inet6"
				}
				if err := runCmd("route", "-q", "-n", "add", flag, r, "-interface", tun); err != nil {
					if strings.Contains(err.Error(), "File exists") {
						continue // route already present (e.g. duplicate AllowedIPs)
					}
					return fmt.Errorf("route %s: %v", r, err)
				}
			}
		}
	}
	return nil
}

// runCmd runs a setup command; stderr is captured into the error.
func runCmd(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return err
	}
	return nil
}
