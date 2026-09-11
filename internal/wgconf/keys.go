package wgconf

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
)

// GeneratePrivateKey returns a fresh clamped Curve25519 private key, base64.
func GeneratePrivateKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	clampPrivateKey(b[:])
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

// GeneratePresharedKey returns a fresh random preshared key, base64.
func GeneratePresharedKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

// PublicKeyOf derives the base64 public key of a base64 private key.
func PublicKeyOf(privB64 string) (string, error) {
	if _, err := ParseKey(privB64); err != nil {
		return "", err
	}
	raw, _ := base64.StdEncoding.DecodeString(privB64)
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// ParseKey validates a base64 32-byte key and returns its raw bytes.
func ParseKey(b64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("not valid base64: %v", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("want 32 bytes, got %d", len(raw))
	}
	return raw, nil
}

// ValidEndpoint reports whether s is a host:port endpoint.
func ValidEndpoint(s string) bool {
	_, port, err := net.SplitHostPort(s)
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

func clampPrivateKey(b []byte) {
	// Same clamping as wireguard-go / wg keygen.
	b[0] &= 248
	b[31] &= 127
	b[31] |= 64
}

// KeyHex encodes a base64 key as lowercase hex for the UAPI protocol.
func KeyHex(b64 string) (string, error) {
	raw, err := ParseKey(b64)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// HexToKey decodes lowercase hex into a base64 key.
func HexToKey(h string) (string, error) {
	raw, err := hex.DecodeString(h)
	if err != nil {
		return "", err
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("want 32 bytes, got %d", len(raw))
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
