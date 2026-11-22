//go:build linux

package daemon

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// userspaceIfName gives wireguard-go an explicit Linux interface name; unlike
// darwin's utun, Linux needs one up front and it must be stable across
// restarts so adoption can find the UAPI socket.
func userspaceIfName(instance string) string { return linuxIfName(instance) }

// linuxIfName derives a deterministic, valid interface name:
// "wgtun-" + the instance name, trimmed to 9 characters and suffixed with a
// short hash when it does not fit in the 15-character limit. The prefix keeps
// wgtun devices from colliding with wg-quick's wg0/wg1.
func linuxIfName(instance string) string {
	var b strings.Builder
	for _, r := range instance {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.', r == '+', r == '=':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	clean := b.String()
	const prefix = "wgtun-"
	const maxLen = 15
	if len(prefix)+len(clean) <= maxLen {
		return prefix + clean
	}
	keep := maxLen - len(prefix) - 5 // "-" + 4 hex digits
	if keep < 1 {
		keep = 1
	}
	if len(clean) > keep {
		clean = clean[:keep]
	}
	h := fnv.New32a()
	h.Write([]byte(instance))
	return fmt.Sprintf("%s%s-%04x", prefix, clean, h.Sum32()&0xffff)
}
