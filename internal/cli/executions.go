package cli

import (
	"context"
	"fmt"
	"math/big"
	"text/tabwriter"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/checks"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/executions"
)

// executionsReport is the JSON output of `arbot executions`.
type executionsReport struct {
	Safe    ethrpc.Address `json:"safe"`
	ChainID uint64         `json:"chainId"`
	// FromBlock and ToBlock are the blocks searched, both included. ToBlock is the
	// request's Safe block. FromBlock is left out with -all, which reads the blocks
	// that the service names rather than a range.
	FromBlock  *uint64     `json:"fromBlock,omitempty"`
	ToBlock    uint64      `json:"toBlock"`
	Executions []execution `json:"executions"`
	// History is how the executions were found with -all, and is left out without
	// it.
	History *historyReport `json:"history,omitempty"`
}

// historyReport is how `arbot executions -all` found the executions.
type historyReport struct {
	// Service is the Safe Transaction Service that listed the executions, an
	// offchain source (§ 3.4), and when it was observed.
	Service serviceSource `json:"service"`
	// Nonce is the Safe's nonce at the Safe block. Every execution uses the next
	// nonce, so it is the number of Safe transactions that the Safe executed up to
	// it, unless a delegate call changed it.
	Nonce *big.Int `json:"nonce"`
	// Complete is whether the executions account for every nonce below Nonce, and
	// nothing shows that a delegate call or a module changed it, and Missing are
	// the ranges of those that they don't use, or null if the nonce is too large to
	// list them.
	Complete bool                    `json:"complete"`
	Missing  []executions.NonceRange `json:"missingNonces"`
	// Scanned are the ranges of blocks whose logs were scanned for the executions
	// that the service doesn't list.
	Scanned []executions.BlockRange `json:"scanned"`
	// Rejected are the executions that the service lists but the chain doesn't
	// confirm.
	Rejected []executions.Rejected `json:"rejected"`
	// Creation is the creation of the Safe's account, if the service has it and the
	// chain confirms its block, or a scan needed its block, or null.
	Creation *executions.Creation `json:"creation"`
}

// serviceSource is a Safe Transaction Service that a command read, and when.
type serviceSource struct {
	URL        string    `json:"url"`
	ObservedAt time.Time `json:"observedAt"`
}

// execution is an execution in the output of `arbot executions`.
type execution struct {
	executions.Execution
	// Proposal is whether the Safe transaction is the request's own, whose effects
	// aren't evidence (§ 3.6). It is left out if it isn't.
	Proposal bool `json:"proposal,omitempty"`
	// Target and Calls are the account that the transaction's own call is to, and
	// the calls that the transaction makes, as arbot calls reports them, with
	// -calls. They are left out without -calls, and for an unknown transaction.
	Target *checks.Account `json:"target,omitempty"`
	Calls  []checks.Call   `json:"calls,omitempty"`
}

// executionsCommand lists the Safe transactions that the Safe of a request
// executed before its proposal.
func executionsCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("executions")
	asJSON := flags.Bool("json", false, "write the executions as JSON")
	requestFile := flags.String("request-file", "", "read the Safe, its chain, and the block from the request in the JSON file at `path`, as written by arbot info -json")
	blocks := flags.Uint64("blocks", defaultScanBlocks, "search this number of blocks up to the request's Safe block")
	withCalls := flags.Bool("calls", false, "also list the calls that each transaction makes, as arbot calls lists them")
	all := flags.Bool("all", false, "list the executions up to the request's Safe block in the blocks that the Safe Transaction Service names, instead of searching the last -blocks blocks")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s executions [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(out, "Lists the Safe transactions that the Safe of a request executed, from the Safe's chain, as of the\n")
		fmt.Fprintf(out, "request's Safe block: the ExecutionSuccess and ExecutionFailure events of the Safe in the last -blocks\n")
		fmt.Fprintf(out, "blocks up to it, without the transaction's own effects or anything later. It searches that range\n")
		fmt.Fprintf(out, "only, so an execution before it isn't listed. Executions by modules aren't listed.\n\n")
		fmt.Fprintf(out, "The events only have the hash of each Safe transaction, so the command decodes the transaction from\n")
		fmt.Fprintf(out, "the first source that has one with that hash, and reports the source:\n\n")
		fmt.Fprintf(out, "  event     the SafeMultiSigTransaction event that SafeL2 logs\n")
		fmt.Fprintf(out, "  calldata  the calldata of the onchain transaction, if it calls execTransaction on the Safe\n")
		fmt.Fprintf(out, "  trace     a trace of the onchain transaction, which needs a node with debug_traceTransaction\n")
		fmt.Fprintf(out, "  service   the Safe Transaction Service (see the txServices setting, where an empty URL leaves it\n")
		fmt.Fprintf(out, "            out), an offchain source\n\n")
		fmt.Fprintf(out, "A transaction is only reported if its EIP-712 hash is the one that the Safe logged, whichever the\n")
		fmt.Fprintf(out, "source, in the domain of Safe 1.3.0 and later, or in the older one of 1.1.1 and 1.2.0, without the\n")
		fmt.Fprintf(out, "chain ID. Safe 1.0.0 logs nothing when a transaction succeeds, so its executions aren't listed. The\n")
		fmt.Fprintf(out, "calldata and traces have no nonce, so it is worked out from the Safe's nonce at the last block. An\n")
		fmt.Fprintf(out, "execution nested in another logs first, so the nonces of the other executions of its block are tried\n")
		fmt.Fprintf(out, "too. A nonce that the node fails to read, such as at a block whose state it doesn't keep, leaves them\n")
		fmt.Fprintf(out, "out with a warning, and fails the command only if no other source has the transaction. Otherwise, a\n")
		fmt.Fprintf(out, "transaction that no source has is an unknown transaction, or null with -json.\n")
		fmt.Fprintf(out, "The text output shows the selector and length of the data, and -json has all of it. The execution\n")
		fmt.Fprintf(out, "of the request's own Safe transaction, if it was executed by the Safe block, is marked, since its\n")
		fmt.Fprintf(out, "effects aren't evidence (§ 3.6).\n\n")
		fmt.Fprintf(out, "With -calls, it also lists the calls that each known transaction makes, as arbot calls lists those\n")
		fmt.Fprintf(out, "of a request's transaction, with the accounts that they are to as of the request's Safe block: delegate\n")
		fmt.Fprintf(out, "calls to MultiSend contracts decoded into their calls, and each call's data decoded as a well-known\n")
		fmt.Fprintf(out, "function, if it is one, such as an ERC-20 transfer, or a Safe's execTransaction and the call it makes.\n")
		fmt.Fprintf(out, "The refund call is the largest refund that a transaction allows, and the payment is what it paid. The\n")
		fmt.Fprintf(out, "calls of a failed execution had no effect. With -json, a call to a CreateCall of a § 2.1 version has\n")
		fmt.Fprintf(out, "the address of the contract that it deploys with CREATE2, as arbot calls has it, but not with CREATE,\n")
		fmt.Fprintf(out, "since that depends on the deployer's nonce when the transaction was executed, not at the Safe block.\n\n")
		fmt.Fprintf(out, "With -all, it lists the executions up to the request's Safe block instead, which a scan of the Safe's\n")
		fmt.Fprintf(out, "logs can't reach on public nodes. It needs the Safe Transaction Service, an offchain source that it\n")
		fmt.Fprintf(out, "reports with its URL and when it was observed, and lists the Safe's executed transactions from it. It\n")
		fmt.Fprintf(out, "reads the Safe's logs in each block that the service names, and keeps the executions that the Safe\n")
		fmt.Fprintf(out, "logged there, whether the service lists them rightly or not, which it decodes as above, taking a\n")
		fmt.Fprintf(out, "transaction that the service lists with its hash from the listing. Not every block is read, so the\n")
		fmt.Fprintf(out, "nonce of an execution is worked out from the Safe's nonce at the end of its block. A block whose logs\n")
		fmt.Fprintf(out, "have no execution of the Safe is read again once. An execution that the service lists is confirmed\n")
		fmt.Fprintf(out, "only if its fields hash to its Safe transaction hash, and the Safe logged the execution of that hash in\n")
		fmt.Fprintf(out, "the block and transaction that the service reports. Any other is rejected, with the reason.\n\n")
		fmt.Fprintf(out, "Every execution uses the next nonce, so the Safe's nonce at the Safe block is the number of executions\n")
		fmt.Fprintf(out, "up to it, unless a delegate call changed it, and a node that fails to read it, such as one without the\n")
		fmt.Fprintf(out, "Safe block's state, fails the command. For the nonces that no execution found used, it scans the\n")
		fmt.Fprintf(out, "blocks between the executions before and after them, from the Safe's creation, or up to the Safe\n")
		fmt.Fprintf(out, "block, and reports the ranges, then the nonces still missing. A scan that fails, such as one that a\n")
		fmt.Fprintf(out, "public node times out, is a warning, and leaves its nonces missing. The history is complete if none\n")
		fmt.Fprintf(out, "are, the nonces of the executions found don't show that a delegate call set the nonce back, and the\n")
		fmt.Fprintf(out, "Safe's nonce at the end of each block with a delegate call, other than to MultiSendCallOnly, which\n")
		fmt.Fprintf(out, "only makes calls, with an execution by Safe 1.1.1 or 1.2.0, where a call to the Safe itself can\n")
		fmt.Fprintf(out, "replace the singleton, or with an execution by a module, is one above the highest nonce of the\n")
		fmt.Fprintf(out, "executions found up to it, with an official Safe singleton, of a § 2.1 version or of 1.0.0, 1.1.1, or\n")
		fmt.Fprintf(out, "1.2.0, and so is its nonce at the Safe block, as a delegate call or a module can set the nonce, and a\n")
		fmt.Fprintf(out, "delegate call the singleton, to anything. An unknown execution, which may be such a delegate call,\n")
		fmt.Fprintf(out, "leaves it incomplete, and so does that nonce or singleton if the node can't read it, such as at a\n")
		fmt.Fprintf(out, "block whose state it doesn't keep, though a rate limit fails the command. A Safe that the service\n")
		fmt.Fprintf(out, "doesn't know is a warning, and the scans look for all of its executions. Executions by modules don't\n")
		fmt.Fprintf(out, "use nonces, and the service doesn't list them, so they aren't listed, and only those in the blocks\n")
		fmt.Fprintf(out, "that it reads are checked. A module, which a Safe transaction can enable and call, can change the\n")
		fmt.Fprintf(out, "nonce in another block, so a complete history doesn't rule out that a module did.\n\n")
		fmt.Fprintf(out, "It also reports the block that created the Safe's account, if the service names the transaction and the\n")
		fmt.Fprintf(out, "chain confirms that the account got its code in that block, which needs a node that serves the code at\n")
		fmt.Fprintf(out, "old blocks, or else, if a scan starts at it, the first block with the Safe's code. The transaction\n")
		fmt.Fprintf(out, "itself is only reported if it is bound to the account: its receipt names the account as the contract\n")
		fmt.Fprintf(out, "that it created, or has the account's ProxyCreation event from an official proxy factory.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}
	ref, fromBlock, err := openRange(ctx, e, flags, *requestFile, *blocks, *all)
	if err != nil {
		return err
	}

	finder := &executions.Finder{
		Eth:     ref.eth,
		Service: txService(e, ref.ChainID),
		Warn:    func(err error) { fmt.Fprintf(e.stderr, "%s: warning: %v\n", e.progname, err) },
	}
	report := executionsReport{Safe: ref.Safe, ChainID: ref.ChainID, FromBlock: fromBlock, ToBlock: ref.SafeBlock}
	var found []executions.Execution
	if *all {
		if finder.Service == nil {
			return errNoTxService("-all", ref.ChainID)
		}
		observed := time.Now().UTC().Truncate(time.Second)
		history, err := finder.FindAll(ctx, ref.Safe, ethrpc.BlockNumber(ref.SafeBlock))
		if err != nil {
			return err
		}
		report.History = &historyReport{
			Service: serviceSource{URL: finder.Service.URL(), ObservedAt: observed},
			Nonce:   history.Nonce, Complete: history.Complete(), Missing: history.Missing,
			Scanned: history.Scanned, Rejected: history.Rejected, Creation: history.Creation,
		}
		found = history.Executions
		// FindAll warns about anything else that leaves the history incomplete.
		if len(history.Missing) > 0 {
			finder.Warn(fmt.Errorf("the executions found don't account for every nonce below the Safe's nonce %s at block %d", history.Nonce, ref.SafeBlock))
		}
	} else {
		var err error
		if found, err = finder.Find(ctx, ref.Safe, ethrpc.BlockNumber(*fromBlock), ethrpc.BlockNumber(ref.SafeBlock)); err != nil {
			return err
		}
	}
	report.Executions = make([]execution, len(found))
	var reader *checks.CallReader
	if *withCalls {
		reader = checks.NewCallReader(ref.eth, ref.Safe, ethrpc.BlockNumber(ref.SafeBlock))
	}
	var proposal *execution
	for i, x := range found {
		report.Executions[i].Execution = x
		if x.SafeTxHash == ref.SafeTxHash {
			report.Executions[i].Proposal = true
			proposal = &report.Executions[i]
		}
		if reader == nil || x.Transaction == nil {
			continue
		}
		calls, err := reader.Calls(ctx, x.Transaction)
		if err != nil {
			return fmt.Errorf("decoding the calls of Safe transaction %s: %w", x.SafeTxHash, err)
		}
		report.Executions[i].Target, report.Executions[i].Calls = &calls.Target, calls.Calls
	}

	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	if report.History != nil {
		writeHistory(e, &report)
	} else {
		fmt.Fprintf(e.stdout, "Safe transactions executed by %s on chain %d, in blocks %d to %d\n\n", report.Safe, report.ChainID, *report.FromBlock, report.ToBlock)
	}
	if len(found) == 0 {
		fmt.Fprintln(e.stdout, "none")
	} else if err := writeExecutions(e, found); err != nil {
		return err
	}
	if proposal != nil {
		fmt.Fprintf(e.stdout, "\nThe execution in block %d, transaction %s, is of the request's own Safe transaction, whose effects aren't evidence (§ 3.6).\n", proposal.Block, proposal.TxHash)
	}
	if h := report.History; h != nil && len(h.Rejected) > 0 {
		fmt.Fprintf(e.stdout, "\nRejected\n\n")
		w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NONCE\tSAFE TX HASH\tTRANSACTION\tREASON")
		for _, r := range h.Rejected {
			nonce := "-"
			if r.Nonce != nil {
				nonce = r.Nonce.String()
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", nonce, r.SafeTxHash, r.TxHash, r.Reason)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	if reader == nil || len(found) == 0 {
		return nil
	}

	fmt.Fprintf(e.stdout, "\nCalls\n\n")
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NONCE\t#\tKIND\tTO\tACCOUNT\tOPERATION\tVALUE\tFUNCTION")
	for _, x := range report.Executions {
		for i, c := range x.Calls {
			fmt.Fprintf(w, "%s\t%d\t%s\n", x.Transaction.Nonce, i+1, callText(c))
		}
	}
	return w.Flush()
}

// writeHistory writes the header of the text output of `arbot executions -all`:
// the service, the nonce, the missing nonces, the scans, and the creation.
func writeHistory(e *env, report *executionsReport) {
	h := report.History
	fmt.Fprintf(e.stdout, "Safe transactions executed by %s on chain %d, up to block %d\n", report.Safe, report.ChainID, report.ToBlock)
	fmt.Fprintf(e.stdout, "Listed by the Safe Transaction Service %q, observed %s, and confirmed by the Safe's logs\n\n", h.Service.URL, h.Service.ObservedAt.Format(time.RFC3339))
	completeness := "complete"
	if !h.Complete {
		completeness = "incomplete"
	}
	fmt.Fprintf(e.stdout, "Nonce at block %d: %s (%s)\n", report.ToBlock, h.Nonce, completeness)
	if h.Missing != nil {
		fmt.Fprintf(e.stdout, "Missing nonces: %s\n", nonceRanges(h.Missing))
	}
	for _, r := range h.Scanned {
		fmt.Fprintf(e.stdout, "Scanned blocks %d to %d for executions that the service doesn't list\n", r.FromBlock, r.ToBlock)
	}
	switch c := h.Creation; {
	case c == nil:
		fmt.Fprintf(e.stdout, "Creation: unknown\n")
	case c.TxHash == nil:
		fmt.Fprintf(e.stdout, "Created in block %d, by a transaction that arbot doesn't bind to the account\n", c.Block)
	case c.Factory != nil:
		fmt.Fprintf(e.stdout, "Created in block %d, transaction %s (ProxyCreation event of %s)\n", c.Block, *c.TxHash, c.Factory)
	default:
		fmt.Fprintf(e.stdout, "Created in block %d, transaction %s (contract creation)\n", c.Block, *c.TxHash)
	}
	fmt.Fprintln(e.stdout)
}

// writeExecutions writes a table of executions.
func writeExecutions(e *env, found []executions.Execution) error {
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BLOCK\tTRANSACTION\tRESULT\tSAFE TX HASH\tSOURCE\tNONCE\tTO\tVALUE\tOPERATION\tDATA")
	for _, x := range found {
		result := "failure"
		if x.Success {
			result = "success"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t", x.Block, x.TxHash, result, x.SafeTxHash)
		if tx := x.Transaction; tx != nil {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", x.Source, tx.Nonce, tx.To, tx.Value, tx.Operation, summarizeData(tx.Data))
		} else {
			fmt.Fprintf(w, "-\tunknown transaction\n")
		}
	}
	return w.Flush()
}

// nonceRanges formats ranges of nonces, such as "0-3, 7", or "none".
func nonceRanges(ranges []executions.NonceRange) string {
	if len(ranges) == 0 {
		return "none"
	}
	s := ""
	for i, r := range ranges {
		if i > 0 {
			s += ", "
		}
		if r.From == r.To {
			s += fmt.Sprint(r.From)
		} else {
			s += fmt.Sprintf("%d-%d", r.From, r.To)
		}
	}
	return s
}

// summarizeData returns the selector and length of call data, such as
// "0xa9059cbb (68 bytes)", or "-" if there is none.
func summarizeData(data []byte) string {
	if len(data) == 0 {
		return "-"
	}
	return fmt.Sprintf("%s (%d bytes)", ethrpc.Bytes(data[:min(len(data), 4)]), len(data))
}
