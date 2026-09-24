// Command arbot is the Safenet arbitration bot's command-line tool.
package main

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"

	"github.com/safe-research/safenet-arbitration-bot/internal/config"
)

var progname = filepath.Base(os.Args[0])

// commands are the subcommands of arbot, by name.
var commands = map[string]struct {
	run         func(ctx context.Context, cfg config.Config, args []string) error
	description string
}{
	"charter": {charter, "fetch the Safenet Arbitration Charter"},
}

func main() {
	configPath := flag.String("config", "", "path to the configuration file (default: ./arbot.config.json, then $XDG_CONFIG_HOME/arbot/config.json)")
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "Usage: %s [flags] <command> [arguments]\n\nCommands:\n", progname)
		for _, name := range slices.Sorted(maps.Keys(commands)) {
			fmt.Fprintf(out, "  %-10s %s\n", name, commands[name].description)
		}
		fmt.Fprintf(out, "\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		die(1, "%v", err)
	}

	if flag.NArg() == 0 {
		die(2, "missing command")
	}
	command, ok := commands[flag.Arg(0)]
	if !ok {
		die(2, "unknown command %q", flag.Arg(0))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := command.run(ctx, cfg, flag.Args()[1:]); err != nil {
		die(1, "%v", err)
	}
}

// die prints an error message prefixed with the program name to stderr and
// exits with the given status code.
func die(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", progname, fmt.Sprintf(format, args...))
	os.Exit(code)
}
