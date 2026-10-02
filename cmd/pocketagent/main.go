// Command pocketagent drives coding agents from a chat app.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

// Set by goreleaser; `go install` builds fall back to the module version.
var version = "dev"

func main() {
	if version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			version = bi.Main.Version
		}
	}

	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	sub := ""
	if cmd == "service" && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("pocketagent", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	force := fs.Bool("force", false, "init: overwrite an existing config")
	fs.Usage = usage
	fs.Parse(args)
	home := filepath.Dir(*cfgPath)

	var err error
	switch cmd {
	case "run":
		err = run(*cfgPath)
	case "init":
		err = initConfig(*cfgPath, *force)
	case "doctor":
		err = doctor(*cfgPath)
	case "start":
		err = start(*cfgPath, home)
	case "stop":
		err = stop(home)
	case "restart":
		if err = stop(home); err == nil {
			err = start(*cfgPath, home)
		}
	case "status":
		err = status(home)
	case "logs":
		err = logs(home)
	case "service":
		switch sub {
		case "install":
			err = serviceInstall(*cfgPath, home)
		case "uninstall":
			err = serviceUninstall()
		default:
			err = fmt.Errorf("usage: pocketagent service install|uninstall")
		}
	case "version":
		fmt.Println("pocketagent", version)
	case "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `pocketagent: drive coding agents from your phone

Setup:
  pocketagent init               interactive setup: bot token, your user id, agents, voice
  pocketagent doctor             check config, agents, voice and the bot connection

Run:
  pocketagent start              run in the background
  pocketagent stop               stop it
  pocketagent restart
  pocketagent status
  pocketagent logs               follow the log
  pocketagent run                run in the foreground
  pocketagent service install    start at login and restart on crashes (launchd/systemd)
  pocketagent service uninstall

  pocketagent version

Flags:
  --config PATH                  config file (default ~/.pocketagent/config.yaml,
                                 or $POCKETAGENT_HOME/config.yaml)
`)
}
