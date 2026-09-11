package wgconf

import (
	"fmt"
	"net"
)

// Conflict describes a ListenPort clash between two configs.
type Conflict struct {
	Port  int
	Other string // name of the other config
}

// DetectListenPortConflicts returns conflicts between the given config's
// ListenPort and every other config in others (keyed by name). A ListenPort
// of 0 (auto) never conflicts.
func DetectListenPortConflicts(name string, cfg *Config, others map[string]*Config) []Conflict {
	var out []Conflict
	port := cfg.Interface.ListenPort
	if port == 0 {
		return nil
	}
	for other, oc := range others {
		if other == name {
			continue
		}
		if oc.Interface.ListenPort == port {
			out = append(out, Conflict{Port: port, Other: other})
		}
	}
	return out
}

// DetectDuplicatePeers returns peer public keys that appear in both configs.
func DetectDuplicatePeers(name string, cfg *Config, others map[string]*Config) []string {
	var out []string
	for _, p := range cfg.Peers {
		for other, oc := range others {
			if other == name {
				continue
			}
			for _, op := range oc.Peers {
				if p.PublicKey == op.PublicKey {
					out = append(out, p.PublicKey)
				}
			}
		}
	}
	return out
}

// PortInUse reports whether a UDP port is already bound on this host
// (excluding our own check socket, which is closed immediately).
func PortInUse(port int) bool {
	if port == 0 {
		return false
	}
	c, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true
	}
	c.Close()
	return false
}
