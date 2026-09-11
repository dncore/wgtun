// wgs — wireguard-go orchestrator: TUI + root daemon.
package main

import (
	"fmt"
	"os"

	"github.com/dncore/wg-service/internal/daemon"
	"github.com/dncore/wg-service/internal/tui"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "daemon":
			daemon.Version = version
			daemon.Main(os.Args[2:])
			return
		case "version":
			fmt.Println("wgs", version)
			return
		case "help", "-h", "--help":
			usage()
			return
		default:
			fmt.Fprintf(os.Stderr, "wgs: unknown command %q\n\n", os.Args[1])
			usage()
			os.Exit(1)
		}
	}
	tui.Run()
}

var version = "dev"

func usage() {
	fmt.Print(`wgs — wireguard-go orchestrator

Usage:
  wgs                 start the TUI (requires the daemon to be running)
  wgs daemon          run the root daemon (foreground; launchd runs this)
  wgs daemon --install    install the LaunchDaemon plist (sudo)
  wgs daemon --uninstall  remove the LaunchDaemon plist (sudo)
  wgs version         print version
`)
}
