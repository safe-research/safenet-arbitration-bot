package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/safe-research/safenet-arbitration-bot/internal/checks"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// callsCommand lists the calls that the Safe of a request makes, as the
// deterministic checks judge them.
func callsCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("calls")
	asJSON := flags.Bool("json", false, "write the calls as JSON")
	requestFile := flags.String("request-file", "", "read the request from the JSON file at `path`, as written by arbot info -json")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s calls [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(out, "Lists the calls that the Safe of a request makes, as arbot classify judges them, read from the Safe's\n")
		fmt.Fprintf(out, "chain as of the request's Safe block, without anything later.\n\n")
		fmt.Fprintf(out, "It shows the Safe's code hash and the Safe proxy version of Charter § 2.1 whose official proxy factory\n")
		fmt.Fprintf(out, "deploys a proxy with that code on its network, if any, and its singleton, the address in storage slot\n")
		fmt.Fprintf(out, "0, with the § 2.1 singleton version that it is an official deployment of, if any.\n\n")
		fmt.Fprintf(out, "Delegate calls to the MultiSend and MultiSendCallOnly contracts of § 2.1 versions are decoded into\n")
		fmt.Fprintf(out, "the calls that they make, as the contracts do. A root call that reverts whatever the state is replaced\n")
		fmt.Fprintf(out, "by a \"reverting\" placeholder, a call to the MultiSend with no data, and one that makes no calls by an\n")
		fmt.Fprintf(out, "\"empty\" placeholder, a delegate call to its multiSend with no transactions. A call to\n")
		fmt.Fprintf(out, "MultiSendCallOnly, rather than a delegate call, makes the calls itself, so it stays one call, unless\n")
		fmt.Fprintf(out, "it reverts, such as on a delegate call in its batch, which needs the contract at its address: a call\n")
		fmt.Fprintf(out, "to the address without code succeeds. A transaction whose batches nest more than 8 levels deep, whose\n")
		fmt.Fprintf(out, "batch transactions run past the end of their batches by more than 64 KiB in all, or whose multiSend\n")
		fmt.Fprintf(out, "arguments don't decode, is left as its own call, marked as an \"unexpanded\" placeholder: the calls that\n")
		fmt.Fprintf(out, "it makes are unknown, and no check finds it secure. A last \"refund\" call pays the largest gas refund\n")
		fmt.Fprintf(out, "that the transaction allows, if any, where the zero address as its recipient means the account that\n")
		fmt.Fprintf(out, "executes the transaction. Each account that the Safe calls has its code hash and size, and the\n")
		fmt.Fprintf(out, "contract of a § 2.1 version that it is, identified by its address on the networks that the Charter\n")
		fmt.Fprintf(out, "covers if it has code, and CreateCall by its code hash too. The text output shortens long data; -json\n")
		fmt.Fprintf(out, "has it all.\n\n")
		fmt.Fprintf(out, "Each call's data is decoded as a well-known function, if it is one: ERC-20 transfer, transferFrom,\n")
		fmt.Fprintf(out, "and approve, the Safe's execTransaction and setup, MultiSend's multiSend, the functions of the Safe\n")
		fmt.Fprintf(out, "proxy factories that create a proxy, such as createProxyWithNonce, and CreateCall's performCreate and\n")
		fmt.Fprintf(out, "performCreate2, with the data of the calls that each makes, such as the initializer of a proxy or the\n")
		fmt.Fprintf(out, "transactions of a batch, decoded the same way. Data is decoded by its selector and ABI layout only, so\n")
		fmt.Fprintf(out, "what a call does depends on the account that it is to. The text output names at most 16 functions for\n")
		fmt.Fprintf(out, "each call.\n\n")
		fmt.Fprintf(out, "With -json, the batch of a multiSend argument has its transactions, each with its operation, to,\n")
		fmt.Fprintf(out, "value, data, dataLength, and function. Its to is an address, where, since 1.5.0, the zero address\n")
		fmt.Fprintf(out, "means the account that runs the batch, its data is the data that the batch has, and its dataLength the\n")
		fmt.Fprintf(out, "length of the data that the contracts call with, which is longer only for a transaction that runs past\n")
		fmt.Fprintf(out, "the end of the batch, where they read zeros. Its function is decoded from its data followed by those\n")
		fmt.Fprintf(out, "zeros, as the contracts call it, but is null if that would bring the zeros added for the batches in\n")
		fmt.Fprintf(out, "one call's data over 64 KiB. A batch that the MultiSend format itself makes revert, with an unknown\n")
		fmt.Fprintf(out, "operation or a data length over 2^24 bytes, isn't decoded, so the function of the call that has it is\n")
		fmt.Fprintf(out, "null. A decoded batch describes the call data only, so the contract that runs it can still revert,\n")
		fmt.Fprintf(out, "such as MultiSendCallOnly on a delegate call, or a MultiSend whose nested batch reverts.\n\n")
		fmt.Fprintf(out, "A call to the CreateCall contract of a § 2.1 version has the address of the contract that it would\n")
		fmt.Fprintf(out, "deploy, derived from its data: with CREATE2, from the deployer, salt, and init code, and with CREATE,\n")
		fmt.Fprintf(out, "from the deployer's nonce at the Safe block and the deployments of the transaction's earlier calls to\n")
		fmt.Fprintf(out, "a CreateCall, but not those that other calls make, such as a contract that the Safe delegate calls, or\n")
		fmt.Fprintf(out, "the init code of a contract that a CreateCall deploys. The deployer is the CreateCall for a call,\n")
		fmt.Fprintf(out, "whose nonce anyone's deployment through it can change before the transaction is executed, and the Safe\n")
		fmt.Fprintf(out, "for a delegate call. A call that sends value has no address, since CreateCall's functions aren't\n")
		fmt.Fprintf(out, "payable, so it reverts. The function's value argument is what the deployed contract gets, from the\n")
		fmt.Fprintf(out, "deployer's balance.\n\n")
		fmt.Fprintf(out, "The text output prints the Safe, the target of the Safe transaction's own call, and a table of the\n")
		fmt.Fprintf(out, "calls with their functions, such as execTransaction > transfer, then the contracts that the calls\n")
		fmt.Fprintf(out, "would deploy.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}
	if *requestFile == "" {
		return usageError("-request-file is required")
	}
	request, err := readRequest(*requestFile)
	if err != nil {
		return err
	}
	report, err := checks.Calls(ctx, ethrpc.NewDialer(e.cfg.RPCs), request)
	if err != nil {
		return err
	}

	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	safe := report.Safe
	fmt.Fprintf(e.stdout, "Safe %s on chain %d, as of block %d\n", safe.Address, report.ChainID, report.SafeBlock)
	fmt.Fprintf(e.stdout, "  Code       %s (%d bytes), %s\n", safe.CodeHash, safe.CodeSize, versionText("Safe proxy", safe.ProxyVersion))
	fmt.Fprintf(e.stdout, "  Singleton  %s, %s\n\n", safe.Singleton, versionText("Safe singleton", safe.SingletonVersion))
	fmt.Fprintf(e.stdout, "Target %s, %s\n\n", report.Target.Address, accountText(report.Target))

	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tKIND\tTO\tACCOUNT\tOPERATION\tVALUE\tFUNCTION\tDATA")
	for i, c := range report.Calls {
		data := "-"
		if len(c.Data) > 0 {
			data = formatValue(c.Data)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\n", i+1, callText(c), data)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	printCreations(e, report.Calls)
	return nil
}

// printCreations lists the contracts that calls would deploy, with the inputs
// that their addresses are derived from, if any.
func printCreations(e *env, calls []checks.Call) {
	var creations []string
	for i, c := range calls {
		cr := c.Creates
		if cr == nil {
			continue
		}
		var inputs string
		if cr.Salt != nil {
			inputs = "salt " + cr.Salt.String()
		} else {
			inputs = fmt.Sprintf("nonce %d", *cr.Nonce)
		}
		creations = append(creations, fmt.Sprintf("  call %d  %s (%s by %s, %s, init code hash %s)",
			i+1, cr.Address, strings.ToUpper(cr.Method), cr.Deployer, inputs, cr.InitCodeHash))
	}
	if len(creations) > 0 {
		fmt.Fprintf(e.stdout, "\nContracts that the calls would deploy, derived from their data\n\n%s\n", strings.Join(creations, "\n"))
	}
}

// callText describes a call in the KIND, TO, ACCOUNT, OPERATION, VALUE, and
// FUNCTION columns of a table of calls.
func callText(c checks.Call) string {
	kind := c.Kind
	if c.Placeholder != "" {
		kind += " (" + c.Placeholder + " placeholder)"
	}
	function := "-"
	if c.Function != nil {
		function = c.Function.String()
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s", kind, c.To.Address, accountText(c.To), c.Operation, c.Value, function)
}

// versionText describes a § 2.1 version of a kind of contract, or its absence.
func versionText(kind string, version *string) string {
	if version == nil {
		return "not a " + kind + " of a § 2.1 version"
	}
	return kind + " " + *version
}

// accountText describes an account that a Safe calls.
func accountText(a checks.Account) string {
	switch {
	case a.Self:
		return "the Safe itself"
	case a.Contract != nil:
		return a.Contract.String()
	case a.CodeSize == 0:
		return "no code"
	}
	return fmt.Sprintf("code %s (%d bytes)", a.CodeHash, a.CodeSize)
}
