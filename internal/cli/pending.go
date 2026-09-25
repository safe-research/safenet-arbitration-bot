package cli

import (
	"context"
	"fmt"
	"text/tabwriter"
)

// pending lists the disputed Safenet requests awaiting arbitration.
func pending(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("pending")
	asJSON := flags.Bool("json", false, "write the disputes as JSON")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s pending [flags]\n\n", e.progname)
		fmt.Fprintf(flags.Output(), "Lists the disputed Safenet requests that the arbitrator can still rule on, ordered by deadline.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}

	disputes, err := openSafenet(e.cfg).Pending(ctx)
	if err != nil {
		return err
	}

	if *asJSON {
		return writeJSON(e.stdout, disputes)
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REQUEST\tFROZEN\tDEADLINE")
	for _, d := range disputes {
		fmt.Fprintf(w, "%s\t%d\t%d\n", d.RequestID, d.FrozenBlock, d.Deadline)
	}
	return w.Flush()
}
