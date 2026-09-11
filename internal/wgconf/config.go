// Package wgconf parses, validates and serializes wg-quick compatible
// WireGuard configuration files.
package wgconf

import (
	"bufio"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"
)

// Interface is the [Interface] section of a config file.
type Interface struct {
	PrivateKey  string   // base64, 32 bytes
	Addresses   []netip.Prefix
	DNS         []string // parsed and preserved; not applied to the system
	ListenPort  int      // 0 = auto
	MTU         int      // 0 = default (1420 on darwin)
	Table       string   // passed through for wg-quick compatibility
	PostUp      []string
	PostDown    []string
}

// Peer is one [Peer] section of a config file.
type Peer struct {
	PublicKey           string
	PresharedKey        string // optional
	AllowedIPs          []netip.Prefix
	Endpoint            string // host:port, optional (server side peers)
	PersistentKeepalive int    // seconds, 0 = off
}

// Config is a full parsed configuration file.
type Config struct {
	Interface Interface
	Peers     []Peer
}

var ifaceKeys = map[string]bool{
	"privatekey": true, "address": true, "dns": true, "listenport": true,
	"mtu": true, "table": true, "preup": true, "predown": true,
	"postup": true, "postdown": true, "saveconfig": true,
}

var peerKeys = map[string]bool{
	"publickey": true, "presharedkey": true, "allowedips": true,
	"endpoint": true, "persistentkeepalive": true,
}

// Parse reads a wg-quick compatible config.
func Parse(r io.Reader) (*Config, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	cfg := &Config{}
	section := "" // "", "interface", "peer"
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		switch strings.ToLower(line) {
		case "[interface]":
			section = "interface"
			continue
		case "[peer]":
			if section == "" {
				return nil, fmt.Errorf("line %d: [Peer] before [Interface]", ln)
			}
			section = "peer"
			cfg.Peers = append(cfg.Peers, Peer{})
			continue
		}
		if section == "" {
			return nil, fmt.Errorf("line %d: key %q before any section", ln, line)
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: expected key = value, got %q", ln, line)
		}
		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:])
		if key == "" || val == "" {
			return nil, fmt.Errorf("line %d: empty key or value: %q", ln, line)
		}
		if section == "interface" {
			if !ifaceKeys[key] {
				return nil, fmt.Errorf("line %d: unknown [Interface] key %q", ln, key)
			}
			if err := cfg.ifaceSet(ln, key, val); err != nil {
				return nil, err
			}
			continue
		}
		if !peerKeys[key] {
			return nil, fmt.Errorf("line %d: unknown [Peer] key %q", ln, key)
		}
		if err := cfg.peerSet(ln, key, val); err != nil {
			return nil, err
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) ifaceSet(ln int, key, val string) error {
	i := &c.Interface
	switch key {
	case "privatekey":
		i.PrivateKey = val
	case "address":
		pfx, err := parsePrefixList(val)
		if err != nil {
			return fmt.Errorf("line %d: Address: %v", ln, err)
		}
		i.Addresses = append(i.Addresses, pfx...)
	case "dns":
		i.DNS = append(i.DNS, splitList(val)...)
	case "listenport":
		p, err := strconv.Atoi(val)
		if err != nil || p < 0 || p > 65535 {
			return fmt.Errorf("line %d: bad ListenPort %q", ln, val)
		}
		i.ListenPort = p
	case "mtu":
		m, err := strconv.Atoi(val)
		if err != nil || m < 576 || m > 65535 {
			return fmt.Errorf("line %d: bad MTU %q", ln, val)
		}
		i.MTU = m
	case "table":
		i.Table = val
	case "preup", "postup", "predown", "postdown":
		switch key {
		case "preup", "postup":
			i.PostUp = append(i.PostUp, val) // PreUp order relative to PostUp is not tracked
		default:
			i.PostDown = append(i.PostDown, val)
		}
	case "saveconfig":
		// ignored
	}
	return nil
}

func (c *Config) peerSet(ln int, key, val string) error {
	p := &c.Peers[len(c.Peers)-1]
	switch key {
	case "publickey":
		p.PublicKey = val
	case "presharedkey":
		p.PresharedKey = val
	case "allowedips":
		pfx, err := parsePrefixList(val)
		if err != nil {
			return fmt.Errorf("line %d: AllowedIPs: %v", ln, err)
		}
		p.AllowedIPs = append(p.AllowedIPs, pfx...)
	case "endpoint":
		p.Endpoint = val
	case "persistentkeepalive":
		k, err := strconv.Atoi(val)
		if err != nil || (k != 0 && (k < 1 || k > 65535)) {
			return fmt.Errorf("line %d: bad PersistentKeepalive %q", ln, val)
		}
		p.PersistentKeepalive = k
	}
	return nil
}

// Validate checks semantic constraints beyond syntax.
func (c *Config) Validate() error {
	if c.Interface.PrivateKey == "" {
		return fmt.Errorf("Interface.PrivateKey is required")
	}
	if _, err := ParseKey(c.Interface.PrivateKey); err != nil {
		return fmt.Errorf("Interface.PrivateKey: %v", err)
	}
	for _, p := range c.Peers {
		if p.PublicKey == "" {
			return fmt.Errorf("peer with empty PublicKey")
		}
		if _, err := ParseKey(p.PublicKey); err != nil {
			return fmt.Errorf("peer PublicKey: %v", err)
		}
		if p.PresharedKey != "" {
			if _, err := ParseKey(p.PresharedKey); err != nil {
				return fmt.Errorf("peer PresharedKey: %v", err)
			}
		}
		if p.Endpoint != "" && !ValidEndpoint(p.Endpoint) {
			return fmt.Errorf("peer Endpoint %q: want host:port", p.Endpoint)
		}
	}
	if len(c.Interface.Addresses) == 0 {
		return fmt.Errorf("Interface.Address is required")
	}
	return nil
}

// Serialize writes the config in canonical wg-quick form.
func (c *Config) Serialize(w io.Writer) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, "[Interface]")
	fmt.Fprintf(bw, "PrivateKey = %s\n", c.Interface.PrivateKey)
	if len(c.Interface.Addresses) > 0 {
		fmt.Fprintf(bw, "Address = %s\n", joinPrefixes(c.Interface.Addresses))
	}
	if len(c.Interface.DNS) > 0 {
		fmt.Fprintf(bw, "DNS = %s\n", strings.Join(c.Interface.DNS, ", "))
	}
	if c.Interface.ListenPort != 0 {
		fmt.Fprintf(bw, "ListenPort = %d\n", c.Interface.ListenPort)
	}
	if c.Interface.MTU != 0 {
		fmt.Fprintf(bw, "MTU = %d\n", c.Interface.MTU)
	}
	if c.Interface.Table != "" {
		fmt.Fprintf(bw, "Table = %s\n", c.Interface.Table)
	}
	for _, s := range c.Interface.PostUp {
		fmt.Fprintf(bw, "PostUp = %s\n", s)
	}
	for _, s := range c.Interface.PostDown {
		fmt.Fprintf(bw, "PostDown = %s\n", s)
	}
	for _, p := range c.Peers {
		fmt.Fprintln(bw)
		fmt.Fprintln(bw, "[Peer]")
		fmt.Fprintf(bw, "PublicKey = %s\n", p.PublicKey)
		if p.PresharedKey != "" {
			fmt.Fprintf(bw, "PresharedKey = %s\n", p.PresharedKey)
		}
		if len(p.AllowedIPs) > 0 {
			fmt.Fprintf(bw, "AllowedIPs = %s\n", joinPrefixes(p.AllowedIPs))
		}
		if p.Endpoint != "" {
			fmt.Fprintf(bw, "Endpoint = %s\n", p.Endpoint)
		}
		if p.PersistentKeepalive != 0 {
			fmt.Fprintf(bw, "PersistentKeepalive = %d\n", p.PersistentKeepalive)
		}
	}
	return bw.Flush()
}

func parsePrefixList(val string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range splitList(val) {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("%q: %v", s, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func splitList(val string) []string {
	var out []string
	for _, s := range strings.Split(val, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func joinPrefixes(ps []netip.Prefix) string {
	ss := make([]string, len(ps))
	for i, p := range ps {
		ss[i] = p.String()
	}
	return strings.Join(ss, ", ")
}
