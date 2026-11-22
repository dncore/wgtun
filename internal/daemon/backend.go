package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dncore/wgtun/internal/api"
	"github.com/dncore/wgtun/internal/logs"
	"github.com/dncore/wgtun/internal/paths"
	"github.com/dncore/wgtun/internal/uapi"
)

// backendKind is the WireGuard implementation the daemon drives.
type backendKind int

const (
	backendUserspace backendKind = iota // wireguard-go over UAPI
	backendKernel                       // Linux kernel module over wgctrl
)

func (b backendKind) String() string {
	if b == backendKernel {
		return "kernel"
	}
	return "userspace"
}

var backend = backendUserspace

// backendDetail is the human-readable implementation (binary path/version or
// "Linux kernel module"), surfaced in /state and Settings.
var backendDetail string

// selectBackend decides which implementation to use. WGTUN_BACKEND forces
// one (kernel/userspace/auto); the default prefers the kernel module on
// Linux because it is the official implementation, and falls back to
// wireguard-go when the module is unavailable.
func selectBackend(ev *logs.Store) {
	spec := strings.ToLower(strings.TrimSpace(os.Getenv("WGTUN_BACKEND")))
	switch spec {
	case "", "auto":
		if kernelAvailable() {
			backend = backendKernel
		} else {
			backend = backendUserspace
		}
	case "kernel":
		if kernelAvailable() {
			backend = backendKernel
		} else {
			ev.Error("", "WGTUN_BACKEND=kernel but the kernel WireGuard module is unavailable; falling back to wireguard-go")
			backend = backendUserspace
		}
	case "userspace":
		backend = backendUserspace
	default:
		ev.Warn("", "ignoring WGTUN_BACKEND=%q (want auto, kernel or userspace)", spec)
		if kernelAvailable() {
			backend = backendKernel
		} else {
			backend = backendUserspace
		}
	}

	if backend == backendKernel {
		backendDetail = "Linux kernel module"
		api.SetBackendInfo(backend.String(), backendDetail)
		api.SetWireGuardGoInfo("", false, "")
		ev.Info("", "backend: %s (%s)", backend, backendDetail)
		return
	}

	path, ok, ver := detectWireGuardGo(ev)
	backendDetail = path
	api.SetBackendInfo(backend.String(), backendDetail)
	api.SetWireGuardGoInfo(path, ok, ver)
}

// backendSummary formats the backend for logs/CLI.
func backendSummary() string {
	if backendDetail == "" {
		return backend.String()
	}
	return fmt.Sprintf("%s (%s)", backend, backendDetail)
}

// The following dispatchers keep the supervisor's lifecycle logic backend
// agnostic. Userspace devices live in a child process reached over UAPI;
// kernel devices live in the kernel and are reached over netlink (wgctrl).

func (s *Supervisor) startDeviceLocked(i *inst) error {
	if backend == backendKernel {
		return s.startKernelLocked(i)
	}
	return s.startUserspaceLocked(i)
}

func (s *Supervisor) configureDeviceLocked(i *inst) ([]string, error) {
	if backend == backendKernel {
		return s.pushConfKernelLocked(i)
	}
	return s.pushConfLocked(i)
}

// statusDevice probes one device by interface name. Called without s.mu held,
// so it must not touch supervisor state.
func (s *Supervisor) statusDevice(ifname string) (*uapi.DeviceStatus, error) {
	if backend == backendKernel {
		return kernelStatus(ifname)
	}
	return uapi.Get(filepath.Join(paths.WireGuardSockDir, ifname+".sock"))
}

func (s *Supervisor) setDeviceEndpoint(i *inst, publicKey, endpoint string) error {
	if backend == backendKernel {
		return kernelSetEndpoint(i.tun, publicKey, endpoint)
	}
	return uapi.SetPeerEndpoint(i.uapiPath(), publicKey, endpoint)
}

func (s *Supervisor) upsertDevicePeer(i *inst, p uapi.SetPeer) error {
	if backend == backendKernel {
		return kernelUpsertPeer(i.tun, p)
	}
	return uapi.UpsertPeer(i.uapiPath(), p)
}

func (s *Supervisor) teardownDeviceLocked(i *inst) {
	if backend == backendKernel {
		s.teardownKernelLocked(i)
		return
	}
	s.teardownUserspaceLocked(i)
}

func (s *Supervisor) adoptDeviceLocked(i *inst) bool {
	if backend == backendKernel {
		return s.adoptKernelLocked(i)
	}
	return s.adoptUserspaceLocked(i)
}
