//go:build darwin

package daemon

// removeTunLink is a no-op on darwin: utun devices disappear with their
// owning process.
func removeTunLink(string) {}
