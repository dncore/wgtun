package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dncore/wgtun/internal/api"
	"github.com/dncore/wgtun/internal/paths"
	"github.com/dncore/wgtun/internal/wgconf"
)

// runImport implements `wgtun import <file.conf> [--name NAME] [--force]`.
// The config is validated locally first (friendly errors), then handed to
// the daemon, which imports it, marks it autostart-enabled and starts it.
func runImport(args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	name := fs.String("name", "", "instance name (default: the config file's base name)")
	force := fs.Bool("force", false, "save even if the listen port conflicts")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: wgtun import <file.conf> [--name NAME] [--force]")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	file := fs.Arg(0)

	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wgtun import: %v\n", err)
		os.Exit(1)
	}
	conf, err := wgconf.Parse(strings.NewReader(string(data)))
	if err == nil {
		err = conf.Validate()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "wgtun import: %s is not a valid WireGuard config: %v\n", file, err)
		os.Exit(1)
	}

	instName := *name
	if instName == "" {
		instName = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := api.Connect(paths.SocketPath)
	if err := client.Create(ctx, instName, string(data), *force); err != nil {
		if strings.Contains(err.Error(), "dial unix") {
			fmt.Fprintf(os.Stderr, "wgtun import: daemon is not running (socket %s)\n  start it with: sudo wgtun daemon --install\n", paths.SocketPath)
		} else {
			fmt.Fprintf(os.Stderr, "wgtun import: %v\n", err)
		}
		os.Exit(1)
	}
	// an import is meant to be managed long-term: enable boot autostart too
	// (creation alone starts it but leaves autostart off, matching the TUI)
	if err := client.SetEnabled(ctx, instName, true); err != nil {
		fmt.Printf("imported %s as instance %q — starting now\n", file, instName)
		fmt.Fprintf(os.Stderr, "wgtun import: warning: could not enable boot autostart: %v\n", err)
		return
	}
	fmt.Printf("imported %s as instance %q — autostart enabled, starting now\n", file, instName)
}
