package cli

import (
	"context"
	"fmt"

	"github.com/safe-research/safenet-arbitration-bot/internal/checks"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// classify classifies a Safenet request with the deterministic checks of the
// Charter's rules.
func classify(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("classify")
	asJSON := flags.Bool("json", false, "write the classification as JSON")
	requestFile := flags.String("request-file", "", "read the request from the JSON file at `path`, as written by arbot info -json, instead of fetching it")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s classify [flags] <request-id>\n", e.progname)
		fmt.Fprintf(flags.Output(), "       %s classify [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(flags.Output(), "Classifies a Safenet request with the deterministic checks of the Charter's rules, as one of:\n\n")
		fmt.Fprintf(flags.Output(), "  unclassified\n  secure\n  insecure  <rule>  <description>\n  out-of-scope  <description>\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, -1); err != nil {
		return err
	}
	// A request file replaces the request ID.
	if n := flags.NArg(); n != 1 && *requestFile == "" || n != 0 && *requestFile != "" {
		flags.Usage()
		return usageError("")
	}

	var request *safenet.Request
	var err error
	if *requestFile != "" {
		request, err = readRequest(*requestFile)
	} else {
		var id ethrpc.Hash
		if id, err = ethrpc.ParseHash(flags.Arg(0)); err != nil {
			return usageError(fmt.Sprintf("request ID %q: %v", flags.Arg(0), err))
		}
		request, err = openSafenet(e.cfg).Request(ctx, id)
	}
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
