//go:build linux

package daemon

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// setupNetwork applies addresses, MTU and routes with iproute2. It is the
// Linux counterpart of wg-quick's address/route handling:
//
//	ip -4|-6 addr add <cidr> dev <if>
//	ip link set dev <if> mtu <mtu> up
//	ip -4|-6 route add <cidr> dev <if>
//
// Table=off is honoured (addresses only); a numeric table is reported and
// skipped rather than silently putting routes somewhere unexpected.
func (s *Supervisor) setupNetwork(i *inst) error {
	ifname, conf := i.tun, i.conf
	for _, p := range conf.Interface.Addresses {
		if out, err := exec.Command(ipPath(), familyFlag(p.Addr().Is6()), "addr", "add", p.String(), "dev", ifname).CombinedOutput(); err != nil {
			if strings.Contains(string(out), "File exists") {
				continue
			}
			return fmt.Errorf("address %s: %v: %s", p, err, strings.TrimSpace(string(out)))
		}
	}
	mtu := conf.Interface.MTU
	if mtu == 0 {
		mtu = 1420 // wg-quick's default on Linux too
	}
	if out, err := exec.Command(ipPath(), "link", "set", "dev", ifname, "mtu", strconv.Itoa(mtu), "up").CombinedOutput(); err != nil {
		return fmt.Errorf("link up: %v: %s", err, strings.TrimSpace(string(out)))
	}
	switch {
	case strings.EqualFold(conf.Interface.Table, "off"):
		return nil
	case conf.Interface.Table != "" && !strings.EqualFold(conf.Interface.Table, "auto"):
		s.ev.Warn(i.name, "Table=%s is not supported on Linux; routes were not added (use Table=off or PostUp)", conf.Interface.Table)
		return nil
	}
	for _, peer := range conf.Peers {
		for _, ap := range peer.AllowedIPs {
			for _, r := range splitDefaultRoute(ap) {
				if out, err := exec.Command(ipPath(), familyFlag(ap.Addr().Is6()), "route", "add", r, "dev", ifname).CombinedOutput(); err != nil {
					if strings.Contains(string(out), "File exists") {
						continue // duplicate AllowedIPs, or already installed
					}
					return fmt.Errorf("route %s: %v: %s", r, err, strings.TrimSpace(string(out)))
				}
			}
		}
	}
	return nil
}

func familyFlag(v6 bool) string {
	if v6 {
		return "-6"
	}
	return "-4"
}
