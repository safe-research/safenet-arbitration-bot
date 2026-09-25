package cli

import (
	"context"
	"fmt"

	"github.com/safe-research/safenet-arbitration-bot/internal/checks"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// classify classifies a Safenet request with the deterministic checks of the
// Charter's rules.
func classify(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("classify")
	asJSON := flags.Bool("json", false, "write the classification as JSON")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s classify [flags] <request-id>\n\n", e.progname)
		fmt.Fprintf(flags.Output(), "Classifies a Safenet request with the deterministic checks of the Charter's rules, as one of:\n\n")
		fmt.Fprintf(flags.Output(), "  unclassified\n  secure\n  insecure  <rule>  <description>\n  out-of-scope  <description>\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 1); err != nil {
		return err
	}
	id, err := ethrpc.ParseHash(flags.Arg(0))
	if err != nil {
		return usageError(fmt.Sprintf("request ID %q: %v", flags.Arg(0), err))
	}

	sn, at, err := openSafenet(ctx, e.cfg, 0)
	if err != nil {
		return err
	}
	request, err := sn.Request(ctx, id, at)
	if err != nil {
		return err
	}
	c, err := checks.Classify(ctx, request)
	if err != nil {
		return err
	}

	if *asJSON {
		return writeJSON(e.stdout, c)
	}
	_, err = fmt.Fprintln(e.stdout, c)
	return err
}
