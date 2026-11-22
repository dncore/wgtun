package daemon

import (
	"strings"
	"testing"

	"github.com/dncore/wgtun/internal/paths"
)

// isolateStateDir keeps a test away from the machine's real wgtun state.
func isolateStateDir(t *testing.T) {
	t.Helper()
	old := paths.StateDir
	paths.StateDir = t.TempDir()
	t.Cleanup(func() { paths.StateDir = old })
}

func TestServiceEnvMergesSaved(t *testing.T) {
	isolateStateDir(t)
	keys := append([]string{}, serviceEnvKeys...)
	keys = append(keys, "SUDO_USER", "PKEXEC_UID")
	for _, k := range keys {
		t.Setenv(k, "")
	}

	t.Setenv("WGTUN_ENDPOINT_RESOLVER", "doh:https://dns.example/q")
	t.Setenv("WGTUN_RECONCILE_INTERVAL", "10s")
	persistServiceEnv(serviceEnv())

	// A repair runs without the original shell environment; the saved values
	// must survive it.
	t.Setenv("WGTUN_ENDPOINT_RESOLVER", "")
	t.Setenv("WGTUN_RECONCILE_INTERVAL", "")
	got := envMap(serviceEnv())
	if got["WGTUN_ENDPOINT_RESOLVER"] != "doh:https://dns.example/q" {
		t.Errorf("resolver not preserved: %q", got["WGTUN_ENDPOINT_RESOLVER"])
	}
	if got["WGTUN_RECONCILE_INTERVAL"] != "10s" {
		t.Errorf("interval not preserved: %q", got["WGTUN_RECONCILE_INTERVAL"])
	}

	// An explicit value wins over the saved one.
	t.Setenv("WGTUN_RECONCILE_INTERVAL", "20s")
	if got := envMap(serviceEnv()); got["WGTUN_RECONCILE_INTERVAL"] != "20s" {
		t.Errorf("current env should override saved: %q", got["WGTUN_RECONCILE_INTERVAL"])
	}
}

func envMap(pairs []string) map[string]string {
	m := map[string]string{}
	for _, kv := range pairs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}
