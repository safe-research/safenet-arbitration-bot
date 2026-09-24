// Command arbot is the Safenet arbitration bot's command-line tool.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/safe-research/safenet-arbitration-bot/internal/config"
)

var progname = filepath.Base(os.Args[0])

func main() {
	configPath := flag.String("config", "", "path to the configuration file (default: ./arbot.config.json, then $XDG_CONFIG_HOME/arbot/config.json)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] <command> [arguments]\n", progname)
		flag.PrintDefaults()
	}
	flag.Parse()

	if _, err := config.Load(*configPath); err != nil {
		die(1, "%v", err)
	}

	if flag.NArg() == 0 {
		die(2, "missing command")
	}

	die(2, "unknown command %q", flag.Arg(0))
}

// die prints an error message prefixed with the program name to stderr and
// exits with the given status code.
func die(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", progname, fmt.Sprintf(format, args...))
	os.Exit(code)
}
