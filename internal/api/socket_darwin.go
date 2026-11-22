//go:build darwin

package api

// defaultSocketGroups are tried after WGTUN_SOCKET_GROUP.
var defaultSocketGroups = []string{"admin"}
