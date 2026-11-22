//go:build linux

package daemon

import (
	"strings"
	"testing"
)

func TestSystemdUnit(t *testing.T) {
	unit := systemdUnit("/usr/bin/wgtun", []string{"WGTUN_ENDPOINT_RESOLVER=doh:https://dns.example/q"})
	for _, want := range []string{
		"ExecStart=/usr/bin/wgtun daemon",
		"Restart=always",
		"RestartSec=2",
		"StartLimitIntervalSec=0",
		"KillMode=process", // tunnels must survive a daemon restart for adoption
		"After=network-online.target",
		"WantedBy=multi-user.target",
		`Environment=WGTUN_ENDPOINT_RESOLVER=doh:https://dns.example/q`,
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestSystemdArg(t *testing.T) {
	if got := systemdArg("/opt/wgtun/wgtun"); got != "/opt/wgtun/wgtun" {
		t.Errorf("plain arg quoted: %q", got)
	}
	if got := systemdArg(`/opt/my wgtun/wgtun`); got != `"/opt/my wgtun/wgtun"` {
		t.Errorf("spaced arg = %q", got)
	}
	if got := systemdArg(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("escaped arg = %q", got)
	}
}
