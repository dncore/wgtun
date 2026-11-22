//go:build linux

package daemon

import (
	"os"
	"os/user"
)

// defaultServiceGroup derives the invoking user's primary group so the
// unprivileged TUI can reach the root daemon's socket. sudo sets SUDO_USER;
// the TUI's repair path elevates with pkexec, which sets PKEXEC_UID instead.
func defaultServiceGroup() string {
	if name := os.Getenv("WGTUN_SOCKET_GROUP"); name != "" {
		return name
	}
	var usr *user.User
	var err error
	if name := os.Getenv("SUDO_USER"); name != "" {
		usr, err = user.Lookup(name)
	} else if uid := os.Getenv("PKEXEC_UID"); uid != "" {
		usr, err = user.LookupId(uid)
	}
	if err != nil || usr == nil {
		return ""
	}
	if g, err := user.LookupGroupId(usr.Gid); err == nil {
		return g.Name
	}
	return ""
}
