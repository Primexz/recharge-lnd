package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/primexz/recharge-lnd/internal/app"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "", "path to config file (default: config.yaml in ., ~/.lnd-fees, /etc/lnd-fees)")
	showVersion := flag.Bool("version", false, "print version and exit")
	dryRun := flag.Bool("dry-run", false, "show fee changes without applying them")
	flag.Parse()

	if *showVersion {
		fmt.Println("lnd-fees", version)
		os.Exit(0)
	}

	if err := app.Run(*configPath, version, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
