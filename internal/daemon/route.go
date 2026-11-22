package daemon

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/dncore/wgtun/internal/wgconf"
)

// needsEndpointPin reports whether the tunnel's own routes would capture
// packets addressed to the peer endpoint. With AllowedIPs covering the
// endpoint (0.0.0.0/0 being the common case) the encrypted WireGuard packets
// would be routed back into the tunnel they belong to, so the endpoint needs
// a more specific route through the physical network. wg-quick solves this
// with set_endpoint_direct_route; this is the same idea.
func needsEndpointPin(conf *wgconf.Config, ip netip.Addr) bool {
	if strings.EqualFold(conf.Interface.Table, "off") {
		return false // the caller owns routing entirely
	}
	for _, p := range conf.Peers {
		for _, ap := range p.AllowedIPs {
			if ap.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// pinEndpointFor makes the endpoint reachable outside the tunnel before its
// address is handed to the device or used for a retarget. Failures are logged
// but never fatal: a narrow AllowedIPs config needs no pin at all.
func (s *Supervisor) pinEndpointFor(i *inst, ep string) {
	host, _, err := net.SplitHostPort(ep)
	if err != nil {
		return
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !needsEndpointPin(i.conf, ip) {
		return
	}
	for _, p := range i.pins {
		if p == ip {
			return
		}
	}
	if err := pinEndpoint(ip); err != nil {
		s.ev.Warn(i.name, "endpoint route for %s: %v", ip, err)
		return
	}
	i.pins = append(i.pins, ip)
	writePins(i)
}

// unpinEndpoints removes the host routes this instance added.
func (s *Supervisor) unpinEndpoints(i *inst) {
	for _, ip := range i.pins {
		unpinEndpoint(ip)
	}
	i.pins = nil
	os.Remove(filepath.Join(i.runtimeDir(), "pins"))
}

// writePins records the host routes in the runtime dir so --uninstall can
// remove them even though it runs in a different process (and after a daemon
// crash, so would the next daemon's cleanup).
func writePins(i *inst) {
	if len(i.pins) == 0 {
		return
	}
	parts := make([]string, len(i.pins))
	for n, ip := range i.pins {
		parts[n] = ip.String()
	}
	_ = os.WriteFile(filepath.Join(i.runtimeDir(), "pins"), []byte(strings.Join(parts, "\n")), 0o644)
}

// splitDefaultRoute expands a default route into two half-routes so adding it
// does not replace the host's real default route (wg-quick's trick).
func splitDefaultRoute(p netip.Prefix) []string {
	switch p.String() {
	case "0.0.0.0/0":
		return []string{"0.0.0.0/1", "128.0.0.0/1"}
	case "::/0":
		return []string{"::/1", "8000::/1"}
	}
	return []string{p.String()}
}
