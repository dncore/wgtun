// wgtun — wireguard-go orchestrator: TUI + root daemon.
package main

import (
	"fmt"
	"os"

	"github.com/dncore/wgtun/internal/daemon"
	"github.com/dncore/wgtun/internal/tui"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "daemon":
			daemon.Version = version
			daemon.Main(os.Args[2:])
			return
		case "version":
			fmt.Println("wgtun", version)
			return
		case "import":
			runImport(os.Args[2:])
			return
		case "help", "-h", "--help":
			usage()
			return
		default:
			fmt.Fprintf(os.Stderr, "wgtun: unknown command %q\n\n", os.Args[1])
			usage()
			os.Exit(1)
		}
	}
	tui.Run()
}

var version = "dev"

func usage() {
	fmt.Print(`wgtun — wireguard-go orchestrator

Usage:
  wgtun                 start the TUI (requires the daemon to be running)
  wgtun daemon          run the root daemon (foreground; launchd runs this)
  wgtun daemon --install    install the LaunchDaemon plist (sudo)
  wgtun daemon --uninstall  remove the LaunchDaemon plist (sudo)
  wgtun import <file.conf>  import a WireGuard config as an instance
  wgtun version         print version
`)
}
