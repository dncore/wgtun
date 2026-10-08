// Package uapi speaks the WireGuard userspace configuration protocol
// (the unix-socket protocol exposed by wireguard-go) natively, so the daemon
// never needs to shell out to the wg binary.
package uapi

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/dncore/wgtun/internal/wgconf"
)

// PeerStatus is the live state of one peer, from get=1.
type PeerStatus struct {
	PublicKey           string    // base64
	Endpoint            string    // ip:port, empty if none
	AllowedIPs          []string  // cidrs
	PersistentKeepalive int       // seconds
	LastHandshake       time.Time // zero if never
	RxBytes, TxBytes    uint64
	HasPSK              bool // a preshared key is configured (all-zero means none)
	ProtocolVersion     int  // 0 when the device does not report one
}

// DeviceStatus is the live state of one device, from get=1.
type DeviceStatus struct {
	PublicKey  string // base64
	ListenPort int
	Peers      []PeerStatus
}

// SetPeer is one peer to configure in a set request.
type SetPeer struct {
	PublicKey           string // base64
	PresharedKey        string // base64, optional
	Endpoint            string // ip:port, optional (resolve DNS before calling)
	AllowedIPs          []string
	PersistentKeepalive int
}

// SetRequest replaces the full device configuration (replace_peers).
type SetRequest struct {
	PrivateKey string // base64
	ListenPort int    // 0 keeps auto/random
	Peers      []SetPeer
}

const ioTimeout = 5 * time.Second

// Get queries a device's live state over its UAPI socket.
func Get(sockPath string) (*DeviceStatus, error) {
	resp, err := roundTrip(sockPath, "get=1\n\n")
	if err != nil {
		return nil, err
	}
	return parseGet(resp)
}

// Set applies a full configuration, replacing all peers.
func Set(sockPath string, req SetRequest) error {
	var b strings.Builder
	b.WriteString("set=1\n")
	if req.PrivateKey != "" {
		hex, err := wgconf.KeyHex(req.PrivateKey)
		if err != nil {
			return fmt.Errorf("private key: %v", err)
		}
		fmt.Fprintf(&b, "private_key=%s\n", hex)
	}
	if req.ListenPort != 0 {
		fmt.Fprintf(&b, "listen_port=%d\n", req.ListenPort)
	}
	b.WriteString("replace_peers=true\n")
	for _, p := range req.Peers {
		hex, err := wgconf.KeyHex(p.PublicKey)
		if err != nil {
			return fmt.Errorf("peer public key: %v", err)
		}
		fmt.Fprintf(&b, "public_key=%s\n", hex)
		if p.PresharedKey != "" {
			hex, err := wgconf.KeyHex(p.PresharedKey)
			if err != nil {
				return fmt.Errorf("peer preshared key: %v", err)
			}
			fmt.Fprintf(&b, "preshared_key=%s\n", hex)
		}
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "endpoint=%s\n", p.Endpoint)
		}
		if p.PersistentKeepalive != 0 {
			fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", p.PersistentKeepalive)
		}
		b.WriteString("replace_allowed_ips=true\n")
		for _, ip := range p.AllowedIPs {
			fmt.Fprintf(&b, "allowed_ip=%s\n", ip)
		}
	}
	b.WriteString("\n")
	return applySet(sockPath, b.String())
}

// SetPeerEndpoint retargets a single peer's endpoint in place. Unlike Set it
// does not send replace_peers, so the peer keeps its session keys, allowed
// IPs and keepalive and only the destination address changes. endpoint must
// already be a literal ip:port (resolve DNS first): the protocol rejects
// hostnames.
func SetPeerEndpoint(sockPath, publicKey, endpoint string) error {
	hexKey, err := wgconf.KeyHex(publicKey)
	if err != nil {
		return fmt.Errorf("peer public key: %v", err)
	}
	var b strings.Builder
	b.WriteString("set=1\n")
	fmt.Fprintf(&b, "public_key=%s\n", hexKey)
	fmt.Fprintf(&b, "endpoint=%s\n", endpoint)
	b.WriteString("\n")
	return applySet(sockPath, b.String())
}

// applySet sends a set=1 request and checks the errno reply.
func applySet(sockPath, req string) error {
	resp, err := roundTrip(sockPath, req)
	if err != nil {
		return err
	}
	// set responses carry only errno
	line, _ := bufio.NewReader(strings.NewReader(resp)).ReadString('\n')
	if line != "errno=0\n" {
		return fmt.Errorf("uapi set rejected: %s", strings.TrimSpace(line))
	}
	return nil
}

// roundTrip writes a request and reads the response up to the terminating
// empty line (inclusive of the leading errno line).
func roundTrip(sockPath, req string) (string, error) {
	c, err := net.Dial("unix", sockPath)
	if err != nil {
		return "", fmt.Errorf("dial %s: %v", sockPath, err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(ioTimeout))
	if _, err := ioWriteString(c, req); err != nil {
		return "", fmt.Errorf("write: %v", err)
	}
	var sb strings.Builder
	br := bufio.NewReader(c)
	for {
		line, err := br.ReadString('\n')
		sb.WriteString(line)
		if err != nil {
			return "", fmt.Errorf("read: %v", err)
		}
		if line == "\n" || line == "" {
			break
		}
	}
	return sb.String(), nil
}

func ioWriteString(c net.Conn, s string) (int, error) {
	return c.Write([]byte(s))
}

func parseGet(resp string) (*DeviceStatus, error) {
	sc := bufio.NewScanner(strings.NewReader(resp))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	dev := &DeviceStatus{}
	var peer *PeerStatus
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "errno":
			if n, _ := strconv.Atoi(val); n != 0 {
				return nil, fmt.Errorf("uapi errno=%s", val)
			}
		case "private_key":
			// not exposed
		case "public_key":
			b64, err := wgconf.HexToKey(val)
			if err != nil {
				return nil, fmt.Errorf("peer public_key: %v", err)
			}
			dev.Peers = append(dev.Peers, PeerStatus{PublicKey: b64})
			peer = &dev.Peers[len(dev.Peers)-1]
		case "listen_port":
			dev.ListenPort, _ = strconv.Atoi(val)
		case "endpoint":
			if peer != nil {
				peer.Endpoint = val
			}
		case "allowed_ip":
			if peer != nil {
				peer.AllowedIPs = append(peer.AllowedIPs, val)
			}
		case "persistent_keepalive_interval":
			if peer != nil {
				peer.PersistentKeepalive, _ = strconv.Atoi(val)
			}
		case "last_handshake_time_sec":
			if peer != nil {
				// The protocol reports 0 for "never". Keep the zero Time in that
				// case: callers must be able to tell "no handshake yet" apart from
				// a real timestamp, and time.Unix(0,0) is 1970 — not the zero
				// Time, so IsZero() would lie.
				if sec, _ := strconv.ParseInt(val, 10, 64); sec != 0 {
					peer.LastHandshake = time.Unix(sec, 0)
				}
			}
		case "last_handshake_time_nsec":
			if peer != nil {
				nsec, _ := strconv.ParseInt(val, 10, 64)
				if !peer.LastHandshake.IsZero() {
					peer.LastHandshake = time.Unix(peer.LastHandshake.Unix(), nsec)
				}
			}
		case "rx_bytes":
			if peer != nil {
				peer.RxBytes, _ = strconv.ParseUint(val, 10, 64)
			}
		case "tx_bytes":
			if peer != nil {
				peer.TxBytes, _ = strconv.ParseUint(val, 10, 64)
			}
		case "preshared_key":
			if peer != nil {
				// the protocol always reports the field; all zeros = no PSK
				peer.HasPSK = strings.Trim(val, "0") != ""
			}
		case "protocol_version":
			if peer != nil {
				peer.ProtocolVersion, _ = strconv.Atoi(val)
			}
		case "fwmark":
			// parsed for forward-compat, not exposed
		}
	}
	return dev, sc.Err()
}
