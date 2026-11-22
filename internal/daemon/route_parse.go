package daemon

import "strings"

// parseRouteGet extracts the gateway and interface from macOS
// `route -n get <ip>` output. A directly connected destination has no
// gateway line (or reports the destination itself); the interface is enough
// in that case.
func parseRouteGet(out string) (gateway, iface string) {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "gateway":
			gateway = strings.TrimSpace(v)
		case "interface":
			iface = strings.TrimSpace(v)
		}
	}
	return gateway, iface
}

// parseIPRouteGet extracts the gateway and device from Linux
// `ip route get <ip>` output, e.g.
//
//	1.2.3.4 via 192.168.1.1 dev eth0 src 192.168.1.5 uid 0
//	1.2.3.4 dev eth0 src 192.168.1.5
func parseIPRouteGet(out string) (gateway, dev string) {
	f := strings.Fields(out)
	for i := 0; i+1 < len(f); i++ {
		switch f[i] {
		case "via":
			gateway = f[i+1]
		case "dev":
			dev = f[i+1]
		}
	}
	return gateway, dev
}
