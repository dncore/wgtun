// Package wire holds the DTOs shared between the daemon API and the TUI
// client, breaking what would otherwise be an import cycle.
package wire

import (
	"time"

	"github.com/dncore/wgtun/internal/uapi"
)

// InstanceView is the API/TUI-facing snapshot of one instance.
type InstanceView struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Running     bool   `json:"running"`
	Tun         string `json:"tun,omitempty"`
	Pid         int    `json:"pid,omitempty"`
	Adopted     bool   `json:"adopted,omitempty"`
	UptimeSec   int64  `json:"uptimeSec,omitempty"`
	ListenPort  int    `json:"listenPort"`
	PeerCount   int    `json:"peerCount"`
	OnlinePeers int    `json:"onlinePeers"` // handshake within the last 3 min
	LastErr     string `json:"lastErr,omitempty"`
}

// StateInfo describes the daemon itself.
type StateInfo struct {
	Version string    `json:"version"`
	Started time.Time `json:"started"`
	Socket  string    `json:"socket"`
	ConfDir string    `json:"confDir"`
	RunDir  string    `json:"runDir"`
	LogDir  string    `json:"logDir"`
	UpSec   int64     `json:"uptimeSec"`
	// WireGuardGo reports the resolved userspace implementation the daemon
	// orchestrates (detected once at daemon start).
	WireGuardGoPath    string `json:"wireguardGoPath"`
	WireGuardGoVersion string `json:"wireguardGoVersion,omitempty"`
	WireGuardGoOK      bool   `json:"wireguardGoOK"`
	// EndpointResolver is the resolver used for peer endpoint hostnames:
	// system, udp:host:port or doh:https://...
	EndpointResolver string `json:"endpointResolver"`
	// Backend is the WireGuard implementation: "kernel" or "userspace".
	Backend       string `json:"backend"`
	BackendDetail string `json:"backendDetail,omitempty"`
	// Health of the reconcile loop. A daemon whose socket answers but whose
	// loop stopped progressing is just as broken as a missing one.
	Pid                  int       `json:"pid"`
	LastReconcile        time.Time `json:"lastReconcile"`
	ReconcileAgeSec      float64   `json:"reconcileAgeSec"`
	ReconcileLastSec     float64   `json:"reconcileLastSec"`
	ReconcileIntervalSec int64     `json:"reconcileIntervalSec"`
	Healthy              bool      `json:"healthy"`
}

// StatusResp is the live status of one running instance.
type StatusResp struct {
	View      InstanceView       `json:"view"`
	Status    *uapi.DeviceStatus `json:"status,omitempty"`
	StatusAge float64            `json:"statusAgeSec,omitempty"`
	// DevicePublicKey is derived by the daemon from the config's private
	// key (get=1 does not expose it). Public data, safe to show.
	DevicePublicKey string `json:"devicePublicKey,omitempty"`
	// Endpoints carries the last DNS check for every hostname endpoint, so the
	// UI can answer "is DDNS tracking actually running, and what did it see?"
	// without digging through the event log.
	Endpoints []EndpointDiag `json:"endpoints,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// EndpointDiag is the daemon's last endpoint-resolution observation for one
// peer whose configured endpoint is a hostname.
type EndpointDiag struct {
	PeerKey    string    `json:"peerKey"`
	Config     string    `json:"config"`               // endpoint as written in the config
	Live       string    `json:"live,omitempty"`       // ip:port the device currently uses
	CheckedAt  time.Time `json:"checkedAt,omitempty"`  // when DNS was last consulted
	Candidates []string  `json:"candidates,omitempty"` // ip:port answers from that lookup
	TTLSec     int64     `json:"ttlSec,omitempty"`     // smallest answer TTL, 0 = unknown
	Resolver   string    `json:"resolver,omitempty"`   // system / udp:... / doh:...
	FakeIP     string    `json:"fakeIp,omitempty"`     // fake-IP range the answer fell into
	Decision   string    `json:"decision,omitempty"`   // unchanged / held / retargeted / rotated / filled
	Error      string    `json:"error,omitempty"`
}
