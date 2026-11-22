//go:build linux

package api

// defaultSocketGroups are tried after WGTUN_SOCKET_GROUP. The installer bakes
// the invoking sudo user's primary group into the unit, so this is only a
// fallback for foreground runs.
var defaultSocketGroups = []string{"wheel", "sudo"}
