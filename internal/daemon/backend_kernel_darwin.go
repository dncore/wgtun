//go:build darwin

package daemon

import (
	"errors"

	"github.com/dncore/wgtun/internal/uapi"
)

// Darwin has no in-kernel WireGuard; the dispatchers never select this
// backend, but the symbols must exist so the shared supervisor compiles.
var errNoKernel = errors.New("kernel WireGuard is only available on Linux")

func (s *Supervisor) startKernelLocked(*inst) error { return errNoKernel }
func (s *Supervisor) pushConfKernelLocked(*inst) ([]string, error) {
	return nil, errNoKernel
}
func kernelStatus(string) (*uapi.DeviceStatus, error) { return nil, errNoKernel }
func kernelSetEndpoint(string, string, string) error  { return errNoKernel }
func kernelUpsertPeer(string, uapi.SetPeer) error     { return errNoKernel }
func (s *Supervisor) teardownKernelLocked(*inst)      {}
func (s *Supervisor) adoptKernelLocked(*inst) bool    { return false }
