package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/dncore/wgtun/internal/paths"
)

// serviceEnvKeys are the environment variables baked into the service
// definition. --install regenerates the plist/unit from scratch, so the
// effective values are also saved to StateDir/service-env.json: a later
// repair (e.g. the TUI's R key, which runs without the original shell
// environment) must not silently drop the endpoint resolver or tick interval.
var serviceEnvKeys = []string{
	"WGTUN_RECONCILE_INTERVAL",
	"WGTUN_ENDPOINT_RESOLVER",
	"WGTUN_BACKEND",
	"WGTUN_SOCKET_GROUP",
}

// serviceEnv returns the KEY=VALUE pairs for the service, in a stable order:
// current environment first, then the values saved by the previous install,
// then a platform-derived socket group.
func serviceEnv() []string {
	saved := loadServiceEnv()
	var pairs []string
	for _, k := range serviceEnvKeys {
		v := os.Getenv(k)
		if v == "" {
			v = saved[k]
		}
		if v == "" && k == "WGTUN_SOCKET_GROUP" {
			v = defaultServiceGroup()
		}
		if v != "" {
			pairs = append(pairs, k+"="+v)
		}
	}
	return pairs
}

func serviceEnvFile() string { return filepath.Join(paths.StateDir, "service-env.json") }

func loadServiceEnv() map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(serviceEnvFile())
	if err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

// persistServiceEnv records the effective environment after a successful
// install. Best effort: failing to write it must not fail the install.
func persistServiceEnv(pairs []string) {
	m := map[string]string{}
	for _, kv := range pairs {
		if k, v, ok := strings.Cut(kv, "="); ok && v != "" {
			m[k] = v
		}
	}
	if err := os.MkdirAll(paths.StateDir, 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	tmp := serviceEnvFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, serviceEnvFile())
}
