package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/safe-research/safenet-arbitration-bot/internal/erc20"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// defaultTransferBlocks is how many blocks before the Safe block that transfers
// scans by default, which is about two weeks on Ethereum Mainnet. Public nodes
// answer a scan of a long history slowly, so it is bounded.
const defaultTransferBlocks = 100_000

// transfersReport is the JSON output of `arbot transfers`.
type transfersReport struct {
	Safe    ethrpc.Address `json:"safe"`
	ChainID uint64         `json:"chainId"`
	// FromBlock and ToBlock are the blocks scanned, both included. ToBlock is the
	// request's Safe block.
	FromBlock uint64 `json:"fromBlock"`
	ToBlock   uint64 `json:"toBlock"`
	// To is the recipient that the transfers were limited to, if any.
	To        *ethrpc.Address `json:"to,omitempty"`
	Transfers []transfer      `json:"transfers"`
}

// transfer is an ERC-20 transfer, in the output of `arbot transfers`. Type says
// what kind of token moved, so that other kinds can be added without changing
// the others.
type transfer struct {
	Type string `json:"type"`
	erc20.Transfer
}

// transfers lists the token transfers out of a Safe's balance before the
// proposal of a request.
func transfers(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("transfers")
	asJSON := flags.Bool("json", false, "write the transfers as JSON")
	requestFile := flags.String("request-file", "", "read the Safe, its chain, and the block from the request in the JSON file at `path`, as written by arbot info -json")
	toFlag := flags.String("to", "", "only list transfers to this `address`")
	blocks := flags.Uint64("blocks", defaultTransferBlocks, "scan this number of blocks up to the request's Safe block")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s transfers [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(flags.Output(), "Lists the ERC-20 transfers out of the Safe of a request, from the Safe's chain, as of the request's\n")
		fmt.Fprintf(flags.Output(), "Safe block: the Transfer events of any token with the Safe as sender, in the last -blocks blocks up to\n")
		fmt.Fprintf(flags.Output(), "it, without the transaction's own effects or anything later. It scans that range only, so a transfer\n")
		fmt.Fprintf(flags.Output(), "before it isn't listed. The events show that a token moved, not why: an account with an allowance can\n")
		fmt.Fprintf(flags.Output(), "move tokens out of the Safe without it making a transaction. Native and ERC-721 transfers aren't listed.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}
	if *requestFile == "" {
		return usageError("-request-file is required")
	}
	if *blocks == 0 {
		return usageError("-blocks must be at least 1")
	}
	var to *ethrpc.Address
	if *toFlag != "" {
		address, err := ethrpc.ParseAddress(*toFlag)
		if err != nil {
			return usageError(fmt.Sprintf("-to %q: %v", *toFlag, err))
		}
		to = &address
	}

	request, err := readRequest(*requestFile)
	if err != nil {
		return err
	}
	tx := request.Proposal.Transaction
	// A zero block would scan the chain's first blocks instead of those before the
	// proposal.
	if tx.ChainID == nil || !tx.ChainID.IsUint64() || request.Proposal.SafeBlock == 0 {
		return fmt.Errorf("request in %s has no Safe chain ID or block", *requestFile)
	}
	chainID := tx.ChainID.Uint64()
	eth, err := ethrpc.NewDialer(e.cfg.RPCs)(ctx, chainID)
	if err != nil {
		return err
	}

	report := transfersReport{Safe: tx.Safe, ChainID: chainID, To: to}
	report.ToBlock = request.Proposal.SafeBlock
	report.FromBlock = report.ToBlock - min(*blocks-1, report.ToBlock)
	found, err := erc20.TransfersFrom(ctx, eth, tx.Safe, to, ethrpc.BlockNumber(report.FromBlock), ethrpc.BlockNumber(report.ToBlock))
	if err != nil {
		return err
	}
	report.Transfers = []transfer{}
	for _, t := range found {
		report.Transfers = append(report.Transfers, transfer{Type: "erc20", Transfer: t})
	}

	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	fmt.Fprintf(e.stdout, "ERC-20 transfers from %s on chain %d, in blocks %d to %d", report.Safe, chainID, report.FromBlock, report.ToBlock)
	if to != nil {
		fmt.Fprintf(e.stdout, ", to %s", to)
	}
	fmt.Fprintf(e.stdout, "\n\n")
	if len(report.Transfers) == 0 {
		_, err = fmt.Fprintln(e.stdout, "none")
		return err
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BLOCK\tTRANSACTION\tTOKEN\tTO\tVALUE")
	for _, t := range report.Transfers {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", t.Block, t.TxHash, t.Token, t.To, t.Value)
	}
	return w.Flush()
}
