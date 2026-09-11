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
	Version  string    `json:"version"`
	Started  time.Time `json:"started"`
	Socket   string    `json:"socket"`
	ConfDir  string    `json:"confDir"`
	RunDir   string    `json:"runDir"`
	LogDir   string    `json:"logDir"`
	UpSec    int64     `json:"uptimeSec"`
}

// StatusResp is the live status of one running instance.
type StatusResp struct {
	View      InstanceView       `json:"view"`
	Status    *uapi.DeviceStatus `json:"status,omitempty"`
	StatusAge float64            `json:"statusAgeSec,omitempty"`
	// DevicePublicKey is derived by the daemon from the config's private
	// key (get=1 does not expose it). Public data, safe to show.
	DevicePublicKey string `json:"devicePublicKey,omitempty"`
	Error           string `json:"error,omitempty"`
}
