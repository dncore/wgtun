//go:build linux

package daemon

import (
	"strings"
	"testing"
)

func TestLinuxIfName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"wg1", "wgtun-wg1"},
		{"home", "wgtun-home"},
		{"averylonginstancename", ""}, // checked below
	} {
		got := linuxIfName(tc.in)
		if len(got) > 15 {
			t.Errorf("linuxIfName(%q) = %q (%d chars) exceeds IFNAMSIZ", tc.in, got, len(got))
		}
		if tc.want != "" && got != tc.want {
			t.Errorf("linuxIfName(%q) = %q want %q", tc.in, got, tc.want)
		}
		if strings.ContainsAny(got, " /:") {
			t.Errorf("linuxIfName(%q) = %q contains invalid characters", tc.in, got)
		}
		if got != linuxIfName(tc.in) {
			t.Errorf("linuxIfName(%q) is not deterministic", tc.in)
		}
	}
	// Two long names sharing a prefix must not collapse to one interface.
	a := linuxIfName("customer-production-east")
	b := linuxIfName("customer-production-west")
	if a == b {
		t.Errorf("long names collided: %q", a)
	}
}
