// Package daemon runs the privileged supervisor that orchestrates
// wireguard-go instances and serves the control socket.
package daemon

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dncore/wgtun/internal/api"
	"github.com/dncore/wgtun/internal/logs"
	"github.com/dncore/wgtun/internal/paths"
	"github.com/dncore/wgtun/internal/state"
)

// Version is the binary version, set by main at startup.
var Version = "dev"

// Main is the entry point for `wgtun daemon`.
func Main(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	install := fs.Bool("install", false, "install the LaunchDaemon and start it (sudo)")
	uninstall := fs.Bool("uninstall", false, "remove the LaunchDaemon (sudo)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	switch {
	case *install:
		if err := InstallLaunchd(); err != nil {
			fmt.Fprintln(os.Stderr, "wgtun daemon --install:", err)
			os.Exit(1)
		}
		return
	case *uninstall:
		if err := UninstallLaunchd(); err != nil {
			fmt.Fprintln(os.Stderr, "wgtun daemon --uninstall:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wgtun daemon:", err)
		os.Exit(1)
	}
}

// markerFile marks a clean shutdown so the next daemon start can tell a
// boot/crash (apply autostart for enabled instances) from a mere restart
// (respect persisted desired state).
func markerFile() string { return filepath.Join(paths.RunDir, ".clean-shutdown") }

func run() error {
	if err := os.MkdirAll(paths.RunDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.LogDir, 0o755); err != nil {
		return err
	}
	ev, err := logs.New(paths.EventsFile(), 10000, 5<<20)
	if err != nil {
		return err
	}
	st, err := state.Load(paths.StateFile())
	if err != nil {
		return err
	}
	freshBoot := true
	if _, err := os.Stat(markerFile()); err == nil {
		freshBoot = false
		os.Remove(markerFile())
	}

	sup := New(st, ev)
	if err := sup.LoadConfigs(); err != nil {
		return err
	}
	sup.Adopt() // take over instances still alive from a previous daemon
	sup.BootInit(freshBoot)

	api.SetVersion(Version)
	wgPath, wgOK, wgVer := detectWireGuardGo(ev)
	api.SetWireGuardGoInfo(wgPath, wgOK, wgVer)
	srv, err := api.Serve(sup, ev, paths.SocketPath)
	if err != nil {
		return err
	}
	ev.Info("", "wgtun daemon started (socket %s, configs %s)", paths.SocketPath, paths.ConfDir)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	ticker := time.NewTicker(reconcileInterval(ev))
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			sup.Reconcile()
		case s := <-sig:
			ev.Info("", "shutting down on %v", s)
			os.WriteFile(markerFile(), nil, 0o644)
			srv.Close()
			return nil
		}
	}
}

// reconcileInterval is how often the supervisor probes instances and picks up
// config changes. An untouched config is no longer re-read and re-parsed each
// tick, so the default tick is cheap; the knob exists for hosts running many
// instances, where status freshness matters less than idle cost. It must stay
// well below the 15s endpoint fast lane, or that lane silently degrades to the
// tick granularity.
func reconcileInterval(ev *logs.Store) time.Duration {
	const def = 5 * time.Second
	v := os.Getenv("WGTUN_RECONCILE_INTERVAL")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Second {
		ev.Warn("", "ignoring WGTUN_RECONCILE_INTERVAL=%q: want a duration of at least 1s", v)
		return def
	}
	if d > endpointFastRecheck {
		ev.Warn("", "WGTUN_RECONCILE_INTERVAL=%s is above the %s fast-lane floor, so endpoint rechecks lose resolution", v, endpointFastRecheck)
	}
	return d
}
