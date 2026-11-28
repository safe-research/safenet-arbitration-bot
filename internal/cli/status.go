package cli

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/status"
)

// statusCommand reports what became of the Safe transaction of a request:
// whether its nonce was below the Safe's at the Safe block, and whether the
// Safe executed it, or another Safe transaction with its nonce.
func statusCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("status")
	asJSON := flags.Bool("json", false, "write the status as JSON")
	requestFile := flags.String("request-file", "", "read the request from the JSON file at `path`, as written by arbot info -json")
	blocks := flags.Uint64("blocks", defaultScanBlocks, "scan this number of blocks of the Safe's execution logs, from the Safe block on, or up to it for a stale nonce, and before it for the transaction itself, or after it for a stale nonce")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s status [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(out, "Reports what became of the Safe transaction of a request, up to the latest block of the Safe's chain:\n")
		fmt.Fprintf(out, "whether its nonce was below the Safe's when it was proposed, and whether the Safe executed it, or\n")
		fmt.Fprintf(out, "another Safe transaction with its nonce, confirmed onchain. Nothing on chain after the request's Safe\n")
		fmt.Fprintf(out, "block is onchain data (§ 2.8), which the text output says after its heading and -json in its first\n")
		fmt.Fprintf(out, "note, so these are facts about the proposal, and the command never classifies a request by them.\n\n")
		fmt.Fprintf(out, "The nonce is stale if it is below the Safe's nonce at the Safe block, so that the Safe couldn't\n")
		fmt.Fprintf(out, "execute the transaction on that state. The status of the execution is executed, other-transaction if\n")
		fmt.Fprintf(out, "the Safe executed another Safe transaction with the nonce, not-executed, or unknown, with the reason,\n")
		fmt.Fprintf(out, "and the first of these sources that settles it:\n\n")
		fmt.Fprintf(out, "  service  the Safe Transaction Service names the onchain transaction that executed it, and its\n")
		fmt.Fprintf(out, "           receipt has the Safe's ExecutionSuccess or ExecutionFailure log of its hash\n")
		fmt.Fprintf(out, "  logs     a scan of the Safe's execution logs, decoded as arbot executions decodes them, in -blocks\n")
		fmt.Fprintf(out, "           blocks from the Safe block on, up to the latest block, or for a stale nonce, up to the Safe\n")
		fmt.Fprintf(out, "           block, and for the transaction itself the -blocks blocks before it too, or after it for a\n")
		fmt.Fprintf(out, "           stale nonce, has the execution of the transaction, or of another one whose decoded nonce is\n")
		fmt.Fprintf(out, "           the transaction's\n")
		fmt.Fprintf(out, "  nonce    the Safe's nonce at the latest block isn't above the transaction's, and the scan has no\n")
		fmt.Fprintf(out, "           execution of the transaction\n\n")
		fmt.Fprintf(out, "A delegate call or a module can set the Safe's nonce back, so the nonce alone never shows that the\n")
		fmt.Fprintf(out, "transaction wasn't executed, and an execution before or after the blocks scanned that set it back\n")
		fmt.Fprintf(out, "isn't ruled out. Another transaction only counts if a source decoded it with the nonce, which its hash\n")
		fmt.Fprintf(out, "proves. The status is unknown if none of the sources settles it, such as for a scan, or a request to\n")
		fmt.Fprintf(out, "decode another transaction, that the node refuses with a JSON-RPC error, as over its range limit, or a\n")
		fmt.Fprintf(out, "nonce that the Safe passed without such an execution in the blocks scanned, as for an unknown\n")
		fmt.Fprintf(out, "transaction there, or a use outside them.\n\n")
		fmt.Fprintf(out, "The service is an offchain source (§ 3.4), reported with its URL and when it was observed. What it\n")
		fmt.Fprintf(out, "says that the chain doesn't confirm, or that it doesn't have the transaction, is a note. An error of\n")
		fmt.Fprintf(out, "the service other than a failure to answer is a warning and a note, after which the chain is read\n")
		fmt.Fprintf(out, "without it. A request to the node or the service that gets no answer, such as for a rate limit, or a\n")
		fmt.Fprintf(out, "scan whose result isn't logs, such as null or a log without its block number, fails the command.\n\n")
		fmt.Fprintf(out, "The execution has the time of its block. The text output has the transaction, the nonce, the execution\n")
		fmt.Fprintf(out, "with its source and reason, and the notes, and -json has all of it.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}
	if *blocks == 0 {
		return usageError("-blocks must be at least 1")
	}
	ref, err := openSafe(ctx, e, *requestFile)
	if err != nil {
		return err
	}

	checker := &status.Checker{
		Safe:    ref.eth,
		Service: txService(e, ref.ChainID),
		Blocks:  *blocks,
		Warn:    func(err error) { fmt.Fprintf(e.stderr, "%s: warning: %v\n", e.progname, err) },
	}
	report, err := checker.Check(ctx, ref.request)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	return writeStatus(e, report)
}

// writeStatus writes the text output of `arbot status`. The reason and the
// notes can quote a node's or the service's errors, so the characters that a
// terminal doesn't print are escaped.
func writeStatus(e *env, r *status.Report) error {
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Status of the Safe transaction of request %s\n", r.Request)
	fmt.Fprintf(w, "%s\n", status.NotEvidence)

	fmt.Fprintf(w, "\nSafe transaction\n")
	fmt.Fprintf(w, "  Safe\t%s on chain %d\n", r.Safe, r.ChainID)
	fmt.Fprintf(w, "  Safe tx hash\t%s\n", r.SafeTxHash)
	fmt.Fprintf(w, "  Safe block\t%d\n", r.SafeBlock)

	n := r.Nonce
	stale := "no"
	if n.Stale {
		stale = "yes"
	}
	fmt.Fprintf(w, "\nNonce\n")
	fmt.Fprintf(w, "  Transaction\t%s\n", n.Transaction)
	fmt.Fprintf(w, "  Safe at the Safe block\t%s\n", n.SafeAtSafeBlock)
	fmt.Fprintf(w, "  Safe at the latest block\t%s (block %d)\n", n.SafeAtLatest, n.LatestBlock)
	fmt.Fprintf(w, "  Stale\t%s\n", stale)

	x := r.Execution
	fmt.Fprintf(w, "\nExecution\n")
	fmt.Fprintf(w, "  Status\t%s\n", x.Status)
	if x.Source != "" {
		fmt.Fprintf(w, "  Source\t%s\n", x.Source)
	}
	if x.Block != nil {
		result := "failure"
		if *x.Success {
			result = "success"
		}
		fmt.Fprintf(w, "  Executed\tblock %d (%s), transaction %s, %s\n", *x.Block, x.BlockTime.Format(time.RFC3339), x.TxHash, result)
	}
	if x.SafeTxHash != nil && *x.SafeTxHash != r.SafeTxHash {
		fmt.Fprintf(w, "  Other Safe tx hash\t%s\n", x.SafeTxHash)
	}
	fmt.Fprintf(w, "  Reason\t%s\n", printable(x.Reason))
	if s := x.Service; s != nil {
		fmt.Fprintf(w, "  Service\t%q, observed %s\n", s.URL, s.ObservedAt.Format(time.RFC3339))
	} else {
		fmt.Fprintf(w, "  Service\tnone for chain %d\n", r.ChainID)
	}
	if s := x.Scanned; s != nil {
		fmt.Fprintf(w, "  Scanned\tblocks %d to %d\n", s.FromBlock, s.ToBlock)
	}

	fmt.Fprintf(w, "\nNotes\n")
	for _, note := range r.Notes[1:] {
		fmt.Fprintf(w, "  - %s\n", printable(note))
	}
	if len(r.Notes) == 1 {
		fmt.Fprintf(w, "  none\n")
	}
	return w.Flush()
}
