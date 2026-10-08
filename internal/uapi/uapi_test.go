package uapi

import (
	"encoding/base64"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dncore/wgtun/internal/wgconf"
)

// fakeServer answers one request per connection and records what it received.
type fakeServer struct {
	ln       net.Listener
	sockPath string
	got      chan string
	reply    string
}

func newFakeServer(t *testing.T, reply string) *fakeServer {
	t.Helper()
	dir := t.TempDir()
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeServer{ln: ln, sockPath: ln.Addr().String(), got: make(chan string, 4), reply: reply}
	go fs.serve()
	t.Cleanup(func() { ln.Close() })
	return fs
}

func (fs *fakeServer) serve() {
	for {
		c, err := fs.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			buf := make([]byte, 0, 4096)
			tmp := make([]byte, 4096)
			for {
				n, err := c.Read(tmp)
				if n > 0 {
					buf = append(buf, tmp[:n]...)
					// both get and set requests end with a blank line
					if endsReq(string(buf)) {
						break
					}
				}
				if err != nil {
					return
				}
			}
			fs.got <- string(buf)
			c.Write([]byte(fs.reply))
		}(c)
	}
}

func endsReq(s string) bool {
	return strings.HasSuffix(s, "\n\n")
}

const sampleGet = `errno=0
private_key=0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20
listen_port=51820
fwmark=0
public_key=a1a2a3a4a5a6a7a8a9aab1b2b3b4b5b6b7b8b9bac1c2c3c4c5c6c7c8c9cad0d1
preshared_key=0000000000000000000000000000000000000000000000000000000000000000
endpoint=203.0.113.7:51820
persistent_keepalive_interval=25
allowed_ip=203.0.113.0/24
allowed_ip=198.51.100.0/24
last_handshake_time_sec=1700000000
last_handshake_time_nsec=123456789
rx_bytes=1024
tx_bytes=2048
public_key=f1f2f3f4f5f6f7f8f9fafbfcfdfeff000102030405060708090a0b0c0d0e0f10
endpoint=198.51.100.9:51821
allowed_ip=10.42.0.0/16
rx_bytes=0
tx_bytes=0

`

func TestGetParsesDevice(t *testing.T) {
	fs := newFakeServer(t, sampleGet)
	dev, err := Get(fs.sockPath)
	if err != nil {
		t.Fatal(err)
	}
	if dev.ListenPort != 51820 {
		t.Fatalf("listen port = %d", dev.ListenPort)
	}
	if len(dev.Peers) != 2 {
		t.Fatalf("peers = %d", len(dev.Peers))
	}
	p0 := dev.Peers[0]
	if p0.Endpoint != "203.0.113.7:51820" {
		t.Errorf("endpoint = %s", p0.Endpoint)
	}
	if len(p0.AllowedIPs) != 2 {
		t.Errorf("allowed ips = %v", p0.AllowedIPs)
	}
	want := time.Unix(1700000000, 123456789)
	if !p0.LastHandshake.Equal(want) {
		t.Errorf("handshake = %v want %v", p0.LastHandshake, want)
	}
	if p0.RxBytes != 1024 || p0.TxBytes != 2048 {
		t.Errorf("bytes = %d/%d", p0.RxBytes, p0.TxBytes)
	}
	if dev.Peers[1].LastHandshake.IsZero() != true {
		t.Errorf("peer 1 handshake should be zero, got %v", dev.Peers[1].LastHandshake)
	}
	// Public keys must parse as base64 32-byte keys.
	if _, err := wgconf.ParseKey(p0.PublicKey); err != nil {
		t.Errorf("public key invalid base64 key: %v", err)
	}
}

func h2b(t *testing.T, h string) string {
	t.Helper()
	raw, err := hex.DecodeString(h)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestSetSendsFullConfig(t *testing.T) {
	fs := newFakeServer(t, "errno=0\n\n")
	priv := h2b(t, "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	pub := h2b(t, "a1a2a3a4a5a6a7a8a9aab1b2b3b4b5b6b7b8b9bac1c2c3c4c5c6c7c8c9cad0d1")
	err := Set(fs.sockPath, SetRequest{
		PrivateKey: priv,
		ListenPort: 51820,
		Peers: []SetPeer{{
			PublicKey:           pub,
			Endpoint:            "203.0.113.7:51820",
			AllowedIPs:          []string{"203.0.113.0/24"},
			PersistentKeepalive: 25,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := <-fs.got
	for _, want := range []string{
		"set=1\n",
		"private_key=0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\n",
		"listen_port=51820\n",
		"replace_peers=true\n",
		"public_key=a1a2a3a4a5a6a7a8a9aab1b2b3b4b5b6b7b8b9bac1c2c3c4c5c6c7c8c9cad0d1\n",
		"endpoint=203.0.113.7:51820\n",
		"persistent_keepalive_interval=25\n",
		"replace_allowed_ips=true\n",
		"allowed_ip=203.0.113.0/24\n",
	} {
		if !strings.Contains(req, want) {
			t.Errorf("request missing %q\ngot:\n%s", want, req)
		}
	}
	if !strings.HasSuffix(req, "\n\n") {
		t.Error("request must end with blank line")
	}
}

func TestSetPeerEndpointIsSurgical(t *testing.T) {
	fs := newFakeServer(t, "errno=0\n\n")
	pub := h2b(t, "a1a2a3a4a5a6a7a8a9aab1b2b3b4b5b6b7b8b9bac1c2c3c4c5c6c7c8c9cad0d1")
	if err := SetPeerEndpoint(fs.sockPath, pub, "198.51.100.7:51820"); err != nil {
		t.Fatal(err)
	}
	req := <-fs.got
	for _, want := range []string{
		"set=1\n",
		"public_key=a1a2a3a4a5a6a7a8a9aab1b2b3b4b5b6b7b8b9bac1c2c3c4c5c6c7c8c9cad0d1\n",
		"endpoint=198.51.100.7:51820\n",
	} {
		if !strings.Contains(req, want) {
			t.Errorf("request missing %q\ngot:\n%s", want, req)
		}
	}
	// The whole point of this call is that nothing else is touched: a peer
	// update must not reset sessions or clobber other peers.
	for _, bad := range []string{
		"replace_peers", "replace_allowed_ips", "private_key=", "listen_port=",
		"persistent_keepalive_interval", "allowed_ip=", "preshared_key",
	} {
		if strings.Contains(req, bad) {
			t.Errorf("request must not contain %q\ngot:\n%s", bad, req)
		}
	}
	if !strings.HasSuffix(req, "\n\n") {
		t.Error("request must end with blank line")
	}
}

func TestSetPeerEndpointBadKey(t *testing.T) {
	fs := newFakeServer(t, "errno=0\n\n")
	if err := SetPeerEndpoint(fs.sockPath, "not-a-key", "198.51.100.7:51820"); err == nil {
		t.Fatal("want error on malformed public key")
	}
}

func TestSetPeerEndpointErrno(t *testing.T) {
	fs := newFakeServer(t, "errno=1\n\n")
	pub := h2b(t, "a1a2a3a4a5a6a7a8a9aab1b2b3b4b5b6b7b8b9bac1c2c3c4c5c6c7c8c9cad0d1")
	if err := SetPeerEndpoint(fs.sockPath, pub, "198.51.100.7:51820"); err == nil {
		t.Fatal("want error on errno=1")
	}
}

// A peer that never handshaked reports 0 seconds; that must stay the zero
// Time, otherwise IsZero() is false for "never" and the daemon cannot tell an
// idle peer from a dead one (regression: the 15s DNS fast lane never closed).
func TestGetNeverHandshakeIsZeroTime(t *testing.T) {
	fs := newFakeServer(t, `errno=0
public_key=a1a2a3a4a5a6a7a8a9aab1b2b3b4b5b6b7b8b9bac1c2c3c4c5c6c7c8c9cad0d1
last_handshake_time_sec=0
last_handshake_time_nsec=0

`)
	dev, err := Get(fs.sockPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(dev.Peers) != 1 {
		t.Fatalf("peers = %d", len(dev.Peers))
	}
	if !dev.Peers[0].LastHandshake.IsZero() {
		t.Fatalf("never-handshaked peer must report the zero Time, got %v", dev.Peers[0].LastHandshake)
	}
}

func TestGetErrno(t *testing.T) {
	fs := newFakeServer(t, "errno=1\n\n")
	if _, err := Get(fs.sockPath); err == nil {
		t.Fatal("want error on errno=1")
	}
}

func TestDialMissing(t *testing.T) {
	if _, err := Get(filepath.Join(os.TempDir(), "definitely-missing-wgtun.sock")); err == nil {
		t.Fatal("want dial error")
	}
}
