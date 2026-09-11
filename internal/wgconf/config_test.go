package wgconf

import (
	"bytes"
	"strings"
	"testing"
)

const sampleConf = `# wg0 — office
[Interface]
PrivateKey = 6HG1a7c1234567890123456789012345678901234567=
Address = 203.0.113.4/24, 203.0.113.5/24
DNS = 203.0.113.1
ListenPort = 51820
MTU = 1420

[Peer]
PublicKey = 7HG1a7c1234567890123456789012345678901234567=
AllowedIPs = 203.0.113.0/24, 198.51.100.0/24
Endpoint = vpn.example.com:51820
PersistentKeepalive = 25

[Peer]
PublicKey = 8HG1a7c1234567890123456789012345678901234567=
AllowedIPs = 10.42.0.0/16
`

// validKey and mustPrefixes live in helpers_test.go.

func TestParseRoundtrip(t *testing.T) {
	orig := &Config{
		Interface: Interface{
			PrivateKey: validKey(1),
			Addresses:  mustPrefixes(t, "203.0.113.4/24", "203.0.113.5/24"),
			DNS:        []string{"203.0.113.1"},
			ListenPort: 51820,
			MTU:        1420,
		},
		Peers: []Peer{
			{
				PublicKey:           validKey(2),
				AllowedIPs:          mustPrefixes(t, "203.0.113.0/24", "198.51.100.0/24"),
				Endpoint:            "vpn.example.com:51820",
				PersistentKeepalive: 25,
			},
			{PublicKey: validKey(3), AllowedIPs: mustPrefixes(t, "10.42.0.0/16")},
		},
	}
	var buf bytes.Buffer
	if err := orig.Serialize(&buf); err != nil {
		t.Fatal(err)
	}
	first := buf.String()
	got, err := Parse(strings.NewReader(first))
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	var buf2 bytes.Buffer
	got.Serialize(&buf2)
	if first != buf2.String() {
		t.Fatalf("roundtrip mismatch:\n--- first ---\n%s\n--- second ---\n%s", first, buf2.String())
	}
	if len(got.Peers) != 2 || got.Peers[0].Endpoint != "vpn.example.com:51820" {
		t.Fatalf("bad peers: %+v", got.Peers)
	}
}

func TestParseMultiLineAddress(t *testing.T) {
	in := "[Interface]\n" +
		"PrivateKey = " + validKey(9) + "\n" +
		"Address = 10.0.0.1/24\n" +
		"Address = fd00::1/64\n"
	cfg, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Interface.Addresses) != 2 {
		t.Fatalf("want 2 addresses, got %v", cfg.Interface.Addresses)
	}
}

func TestParseRejectsUnknownKey(t *testing.T) {
	in := "[Interface]\nPrivateKey = " + validKey(9) + "\nBogusKey = 1\n"
	if _, err := Parse(strings.NewReader(in)); err == nil {
		t.Fatal("want error for unknown key")
	}
}

func TestParsePeerBeforeInterface(t *testing.T) {
	if _, err := Parse(strings.NewReader("[Peer]\nPublicKey = " + validKey(9) + "\n")); err == nil {
		t.Fatal("want error for [Peer] before [Interface]")
	}
}

func TestValidate(t *testing.T) {
	cfg := &Config{Interface: Interface{PrivateKey: "short"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("want error for bad private key")
	}
	cfg = &Config{Interface: Interface{PrivateKey: validKey(1)}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("want error for missing address")
	}
	cfg.Interface.Addresses = mustPrefixes(t, "10.0.0.1/24")
	cfg.Peers = []Peer{{PublicKey: validKey(2), Endpoint: "no-port"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("want error for bad endpoint")
	}
}

func TestDetectListenPortConflicts(t *testing.T) {
	a := &Config{Interface: Interface{ListenPort: 51820}}
	b := &Config{Interface: Interface{ListenPort: 51820}}
	c := &Config{Interface: Interface{ListenPort: 0}}
	others := map[string]*Config{"b": b, "c": c}
	cs := DetectListenPortConflicts("a", a, others)
	if len(cs) != 1 || cs[0].Other != "b" {
		t.Fatalf("want conflict with b, got %+v", cs)
	}
	if got := DetectListenPortConflicts("c", c, others); got != nil {
		t.Fatalf("auto port must not conflict, got %+v", got)
	}
}

func TestDetectDuplicatePeers(t *testing.T) {
	a := &Config{Peers: []Peer{{PublicKey: validKey(5)}}}
	b := &Config{Peers: []Peer{{PublicKey: validKey(5)}, {PublicKey: validKey(6)}}}
	dup := DetectDuplicatePeers("a", a, map[string]*Config{"b": b})
	if len(dup) != 1 {
		t.Fatalf("want 1 duplicate peer, got %v", dup)
	}
}

func TestKeyGen(t *testing.T) {
	priv, err := GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseKey(priv); err != nil {
		t.Fatalf("generated key invalid: %v", err)
	}
	pub, err := PublicKeyOf(priv)
	if err != nil {
		t.Fatal(err)
	}
	if pub == priv {
		t.Fatal("public key equals private key")
	}
	// Determinism: same private key always derives same public key.
	pub2, _ := PublicKeyOf(priv)
	if pub != pub2 {
		t.Fatal("public key derivation not deterministic")
	}
	// RFC 7748 Diffie-Hellman test vector as sanity check of X25519 usage.
	aPriv := "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a"
	wantPub := "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a"
	got, err := PublicKeyOf(hexToB64(aPriv))
	if err != nil {
		t.Fatal(err)
	}
	if got != hexToB64(wantPub) {
		t.Fatalf("X25519 vector mismatch: got %s want %s", got, hexToB64(wantPub))
	}
}

func TestHexKeyRoundtrip(t *testing.T) {
	k := validKey(7)
	h, err := KeyHex(k)
	if err != nil {
		t.Fatal(err)
	}
	back, err := HexToKey(h)
	if err != nil {
		t.Fatal(err)
	}
	if back != k {
		t.Fatalf("hex roundtrip mismatch: %s != %s", back, k)
	}
}

func TestValidEndpoint(t *testing.T) {
	for _, ok := range []string{"1.2.3.4:51820", "vpn.example.com:53", "[::1]:51820"} {
		if !ValidEndpoint(ok) {
			t.Errorf("want valid: %s", ok)
		}
	}
	for _, bad := range []string{"1.2.3.4", "host:notaport", ":0"} {
		if ValidEndpoint(bad) {
			t.Errorf("want invalid: %s", bad)
		}
	}
}
