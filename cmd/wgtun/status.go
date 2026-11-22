package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dncore/wgtun/internal/api"
	"github.com/dncore/wgtun/internal/paths"
)

// runStatus prints daemon health and, per running instance, the last DNS
// observation for hostname endpoints. It is the headless answer to "is DDNS
// tracking actually working?", independent of the TUI.
func runStatus() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := api.Connect(paths.SocketPath)
	st, err := c.State(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wgtun status: daemon unreachable at %s: %v\n", paths.SocketPath, err)
		fmt.Fprintln(os.Stderr, "  start or repair it with: sudo wgtun daemon --install")
		os.Exit(1)
	}
	exitCode := 0
	health := "healthy"
	switch {
	case st.LastReconcile.IsZero():
		health = "unknown (daemon is older than this CLI)"
	case !st.Healthy:
		health = "STALLED"
		exitCode = 2 // usable as a health probe from scripts
	}
	fmt.Printf("daemon %s  pid %d  up %s  backend %s", st.Version, st.Pid, dur(time.Duration(st.UpSec)*time.Second), st.Backend)
	if st.BackendDetail != "" {
		fmt.Printf(" (%s)", st.BackendDetail)
	}
	fmt.Println()
	if st.LastReconcile.IsZero() {
		fmt.Printf("  reconcile  %s\n", health)
	} else {
		fmt.Printf("  reconcile  %s (last %.0fs ago, every %ds, pass %.0fms)\n",
			health, st.ReconcileAgeSec, st.ReconcileIntervalSec, st.ReconcileLastSec*1000)
	}
	fmt.Printf("  resolver   %s\n", st.EndpointResolver)
	fmt.Printf("  socket     %s\n", st.Socket)

	views, err := c.Instances(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wgtun status: instances: %v\n", err)
		os.Exit(1)
	}
	for _, v := range views {
		state := "stopped"
		if v.Running {
			state = "up"
		}
		fmt.Printf("\n%s: %s  %s  peers %d/%d online\n", v.Name, state, v.Tun, v.OnlinePeers, v.PeerCount)
		if !v.Running {
			continue
		}
		resp, err := c.Status(ctx, v.Name)
		if err != nil {
			fmt.Printf("  status: %v\n", err)
			continue
		}
		if resp.Status == nil {
			fmt.Printf("  status: %s\n", resp.Error)
			continue
		}
		for _, p := range resp.Status.Peers {
			age := "never"
			if !p.LastHandshake.IsZero() {
				age = dur(time.Since(p.LastHandshake)) + " ago"
			}
			fmt.Printf("  peer %s  endpoint %s  handshake %s\n", shortKey(p.PublicKey), orDash(p.Endpoint), age)
		}
		for _, d := range resp.Endpoints {
			fmt.Printf("  dns %s -> %s via %s", d.Config, orDash(strings.Join(d.Candidates, ",")), orDash(d.Resolver))
			if d.TTLSec > 0 {
				fmt.Printf(" ttl=%ds", d.TTLSec)
			}
			if d.FakeIP != "" {
				fmt.Printf(" fake-IP=%s", d.FakeIP)
			}
			if d.Decision != "" {
				fmt.Printf(" [%s]", d.Decision)
			}
			if d.Error != "" {
				fmt.Printf(" error=%s", d.Error)
			}
			fmt.Println()
		}
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func shortKey(k string) string {
	if len(k) > 12 {
		return k[:6] + "…" + k[len(k)-6:]
	}
	return k
}

func dur(d time.Duration) string {
	return d.Round(time.Second).String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
