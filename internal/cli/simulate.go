package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/safe-research/safenet-arbitration-bot/internal/checks"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/simulate"
)

// simulateReport is the JSON output of `arbot simulate`.
type simulateReport struct {
	ChainID   uint64           `json:"chainId"`
	SafeBlock uint64           `json:"safeBlock"`
	Safe      ethrpc.Address   `json:"safe"`
	Result    *simulate.Result `json:"result"`
	// Trace is the trace of the simulation, with -trace.
	Trace *simulate.Trace `json:"trace,omitempty"`
}

// simulateCommand runs the Safe transaction of a request, in the Safe's
// context, on the state of the Safe's chain at its Safe block.
func simulateCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("simulate")
	asJSON := flags.Bool("json", false, "write the result as JSON")
	requestFile := flags.String("request-file", "", "read the request from the JSON file at `path`, as written by arbot info -json")
	withTrace := flags.Bool("trace", false, "also trace the simulation with debug_traceCall and the callTracer")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s simulate [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(out, "Runs the Safe transaction of a request with eth_call, on the state of the Safe's chain at the\n")
		fmt.Fprintf(out, "request's Safe block, in the Safe's context and with no signatures: a call from the Safe, with the\n")
		fmt.Fprintf(out, "transaction's value and data, and a delegate call, such as a MultiSend batch, with the Safe's\n")
		fmt.Fprintf(out, "simulateAndRevert, which the Safe singletons of § 2.1 versions have, and which delegate calls the\n")
		fmt.Fprintf(out, "target from the Safe, so that a batch's calls run in order, each on the effects of those before it. It\n")
		fmt.Fprintf(out, "reports whether the transaction's call returned or reverted, the data that it returned or reverted\n")
		fmt.Fprintf(out, "with, and that data decoded as Error(string) or Panic(uint256), which Solidity reverts with. It runs\n")
		fmt.Fprintf(out, "as if the executor sends no value with execTransaction, so a call that sends more than the Safe's\n")
		fmt.Fprintf(out, "balance reverts, and a call from the Safe reports the shortfall, though the executor could send it, as\n")
		fmt.Fprintf(out, "execTransaction is payable.\n\n")
		fmt.Fprintf(out, "It doesn't simulate the gas refund, and doesn't check the signatures, nonce, guard, or safeTxGas.\n")
		fmt.Fprintf(out, "The Safe's nonce isn't incremented first, as execTransaction increments it, and a delegate call's\n")
		fmt.Fprintf(out, "msg.sender is the zero address rather than the account that executes the transaction. The\n")
		fmt.Fprintf(out, "simulation's tx.origin is the Safe for a call and the zero address for a delegate call, rather than\n")
		fmt.Fprintf(out, "the account that sends the transaction, which can't be the Safe, so a target that checks tx.origin,\n")
		fmt.Fprintf(out, "such as with require(msg.sender == tx.origin), can do otherwise when it is executed. The state is\n")
		fmt.Fprintf(out, "that of the Safe block, so the transaction can do otherwise when it is executed in a later block. It\n")
		fmt.Fprintf(out, "needs a node that serves eth_call at the Safe block. It fails for an account that isn't a Safe of a\n")
		fmt.Fprintf(out, "§ 2.1 version, and says why, as arbot safe does.\n\n")
		fmt.Fprintf(out, "With -trace, it also runs debug_traceCall with the callTracer on the same call, and lists every call\n")
		fmt.Fprintf(out, "nested in it, and every contract that it creates with CREATE or CREATE2, or says why the trace is\n")
		fmt.Fprintf(out, "unavailable, such as a node that doesn't serve traces, or refuses them, or a call that sends more\n")
		fmt.Fprintf(out, "than the Safe's balance, which isn't traced. A result that isn't a callTracer trace of the call, such\n")
		fmt.Fprintf(out, "as null, fails. For a delegate call, the top two frames, the Safe's simulateAndRevert and its delegate\n")
		fmt.Fprintf(out, "call to its singleton, always revert. The list of created contracts says which were rolled back by the\n")
		fmt.Fprintf(out, "failure of a frame that they are nested in, other than those two, even if the failure was caught.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}
	ref, err := openSafe(ctx, e, *requestFile)
	if err != nil {
		return err
	}
	tx := ref.Transaction
	block := ethrpc.BlockNumber(ref.SafeBlock)
	// Only a Safe of a § 2.1 version is known to make the transaction's call as the
	// simulation does, and only its singletons to have simulateAndRevert.
	safe, err := checks.NewCallReader(ref.eth, ref.Safe, block).Safe(ctx)
	if err != nil {
		return err
	}
	if err := notASafe(safe, ref.SafeBlock); err != nil {
		return fmt.Errorf("can't simulate the transaction, since only a Safe of a § 2.1 version is known to execute it as simulated: %w", err)
	}
	sim, err := simulate.TransactionOf(tx)
	if err != nil {
		return err
	}
	report := simulateReport{ChainID: ref.ChainID, SafeBlock: ref.SafeBlock, Safe: ref.Safe}
	if report.Result, err = simulate.Run(ctx, ref.eth, sim, block); err != nil {
		return err
	}
	switch {
	case *withTrace && report.Result.ValueShortfall != nil:
		// The simulation didn't run the call, and nodes reject it before tracing it.
		report.Trace = &simulate.Trace{Reason: "not traced: the call sends more than the Safe's balance", Calls: []simulate.Frame{}}
	case *withTrace:
		if report.Trace, err = simulate.TraceCall(ctx, ref.eth, sim, block); err != nil {
			return err
		}
	}

	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	return printSimulation(e, &report, tx)
}

// printSimulation writes the text output of arbot simulate of tx.
func printSimulation(e *env, report *simulateReport, tx *safenet.SafeTransaction) error {
	result := report.Result
	method := "eth_call from the Safe"
	if result.Method == simulate.MethodSimulateAndRevert {
		method = "simulateAndRevert of the Safe, which delegate calls the target in the Safe's context"
	}
	fmt.Fprintf(e.stdout, "The Safe transaction of %s on chain %d, simulated on the state at block %d\n", report.Safe, report.ChainID, report.SafeBlock)
	fmt.Fprintf(e.stdout, "  To         %s\n", tx.To)
	fmt.Fprintf(e.stdout, "  Value      %s\n", tx.Value)
	fmt.Fprintf(e.stdout, "  Operation  %s\n", tx.Operation)
	fmt.Fprintf(e.stdout, "  Data       %s\n", summarizeData(tx.Data))
	fmt.Fprintf(e.stdout, "  Method     %s\n", method)
	fmt.Fprintf(e.stdout, "\nResult       %s\n", result.Status)
	if result.RevertReason != "" {
		fmt.Fprintf(e.stdout, "  Reason     %q\n", result.RevertReason)
	}
	if result.ValueShortfall != nil {
		fmt.Fprintf(e.stdout, "  Shortfall  %s wei more than the Safe's balance\n", result.ValueShortfall)
	}
	if r := result.Error; r != nil {
		fmt.Fprintf(e.stdout, "  Error      %s\n", revertText(r))
		for _, a := range r.Args {
			fmt.Fprintf(e.stdout, "  error      %s %s = %s\n", a.Type, a.Name, formatValue(a.Value))
		}
	}
	fmt.Fprintf(e.stdout, "  Returned   %s\n", wordsText(result.ReturnData))
	if report.Trace != nil {
		return printTrace(e, report.Trace, result.Method)
	}
	return nil
}

// maxCellText is how many characters of text from the node or the called
// contracts a cell of the trace's tables has, so that one long value can't
// widen every row. The JSON output has all of it.
const maxCellText = 100

// cell returns text for a table cell, escaped as printable escapes it, and
// shortened to maxCellText characters.
func cell(text string) string {
	if r := []rune(text); len(r) > maxCellText {
		text = string(r[:maxCellText]) + "..."
	}
	return printable(text)
}

// printTrace writes the trace of a simulation with method, with a table of the
// frames that create contracts.
func printTrace(e *env, t *simulate.Trace, method string) error {
	if !t.Available {
		fmt.Fprintf(e.stdout, "\nTrace unavailable: %q\n", t.Reason)
		return nil
	}
	fmt.Fprintf(e.stdout, "\nTrace (debug_traceCall, callTracer)\n\n")
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tDEPTH\tTYPE\tFROM\tTO\tVALUE\tSELECTOR\tINPUT\tERROR")
	for i, f := range t.Calls {
		to, value, selector := "-", "-", "-"
		if f.To != nil {
			to = f.To.String()
		}
		if f.Value != nil {
			value = f.Value.String()
		}
		if f.Selector != nil {
			selector = f.Selector.String()
		}
		fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s\t%s\t%s\t%d bytes\t%s\n", i+1, f.Depth, cell(f.Type), f.From, to, value, selector, f.InputSize, errorText(f.Error, f.RevertReason))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "\nCreated contracts\n\n")
	// A creation is rolled back if a frame that it is nested in failed, even if the
	// failure was caught, though not by the two frames above the simulated call of
	// a delegate call, simulateAndRevert's, which always revert. undone[d] is the
	// number of the nearest such frame that the frame at depth d is or is nested
	// in, or 0 for none.
	first := 0
	if method == simulate.MethodSimulateAndRevert {
		first = 2
	}
	type creation struct {
		simulate.Frame
		undone int
	}
	var creations []creation
	var undone []int
	for i, f := range t.Calls {
		undone = undone[:f.Depth]
		parent := 0
		if f.Depth > 0 {
			parent = undone[f.Depth-1]
		}
		if f.InitCodeHash != nil {
			creations = append(creations, creation{f, parent})
		}
		if f.Error != "" && f.Depth >= first {
			parent = i + 1
		}
		undone = append(undone, parent)
	}
	if len(creations) == 0 {
		fmt.Fprintf(e.stdout, "  none\n")
		return nil
	}
	w = tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DEPTH\tTYPE\tADDRESS\tCREATOR\tINIT CODE\tERROR")
	for _, c := range creations {
		address := "-"
		if c.To != nil {
			address = c.To.String()
		}
		status := errorText(c.Error, "")
		if c.Error == "" && c.undone != 0 {
			status = fmt.Sprintf("rolled back: #%d failed", c.undone)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d bytes, hash %s\t%s\n", c.Depth, cell(c.Type), address, c.From, c.InputSize, *c.InitCodeHash, status)
	}
	return w.Flush()
}

// errorText describes the error of a trace frame, and the message that it
// reverted with, if any, or "-" for none.
func errorText(err, reason string) string {
	switch {
	case err == "":
		return "-"
	case reason != "":
		return cell(fmt.Sprintf("%s: %q", err, reason))
	}
	return cell(err)
}

// revertText describes a decoded revert error.
func revertText(r *simulate.RevertError) string {
	if r.Signature == "" {
		return r.Selector.String() + " (unknown error)"
	}
	return r.Selector.String() + " " + r.Signature
}

// wordsText formats data as its 32-byte words, one per line, or "-" for none.
func wordsText(data []byte) string {
	if len(data) == 0 {
		return "-"
	}
	var lines []string
	for i := 0; i < len(data); i += 32 {
		lines = append(lines, ethrpc.Bytes(data[i:min(i+32, len(data))]).String())
	}
	return strings.Join(lines, "\n             ")
}
