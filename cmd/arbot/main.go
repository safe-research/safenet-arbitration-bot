// Command arbot is the Safenet arbitration bot's command-line tool. It is
// implemented by internal/cli.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/safe-research/safenet-arbitration-bot/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Run(ctx, os.Args, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
