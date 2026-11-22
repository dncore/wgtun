//go:build darwin

package daemon

// defaultServiceGroup leaves the socket group unset on macOS; the API falls
// back to the conventional admin group.
func defaultServiceGroup() string { return "" }
