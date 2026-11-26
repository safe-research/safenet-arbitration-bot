package cli

import (
	"context"
	"fmt"
	"strings"

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
}

// simulateCommand runs the Safe transaction of a request, in the Safe's
// context, on the state of the Safe's chain at its Safe block.
func simulateCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("simulate")
	asJSON := flags.Bool("json", false, "write the result as JSON")
	requestFile := flags.String("request-file", "", "read the request from the JSON file at `path`, as written by arbot info -json")
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

	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	printSimulation(e, &report, tx)
	return nil
}

// printSimulation writes the text output of arbot simulate of tx.
func printSimulation(e *env, report *simulateReport, tx *safenet.SafeTransaction) {
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
