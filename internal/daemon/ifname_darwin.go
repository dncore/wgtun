//go:build darwin

package daemon

// userspaceIfName always asks the kernel to pick a utun interface; the real
// name is read back from WG_TUN_NAME_FILE.
func userspaceIfName(string) string { return "utun" }
