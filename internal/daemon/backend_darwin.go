//go:build darwin

package daemon

// kernelAvailable is always false on darwin: there is no in-kernel WireGuard.
func kernelAvailable() bool { return false }
