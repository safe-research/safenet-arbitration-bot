package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/safe-research/safenet-arbitration-bot/internal/config"
)

// pending lists the disputed Safenet requests awaiting arbitration.
func pending(ctx context.Context, cfg config.Config, args []string) error {
	flags := flag.NewFlagSet("pending", flag.ExitOnError)
	block := flags.Uint64("block", 0, "Gnosis Chain block to read at (default: latest)")
	asJSON := flags.Bool("json", false, "write the disputes as JSON")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s pending [flags]\n\n", progname)
		fmt.Fprintf(flags.Output(), "Lists the disputed Safenet requests that the arbitrator can still rule on, ordered by deadline.\n\n")
		flags.PrintDefaults()
	}
	flags.Parse(args)
	if flags.NArg() != 0 {
		flags.Usage()
		os.Exit(2)
	}

	sn, at, err := openSafenet(ctx, cfg, *block)
	if err != nil {
		return err
	}
	disputes, err := sn.Pending(ctx, at)
	if err != nil {
		return err
	}

	if *asJSON {
		return writeJSON(disputes)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REQUEST\tFROZEN\tDEADLINE")
	for _, d := range disputes {
		fmt.Fprintf(w, "%s\t%d\t%d\n", d.RequestID, d.FrozenBlock, d.Deadline)
	}
	return w.Flush()
}
