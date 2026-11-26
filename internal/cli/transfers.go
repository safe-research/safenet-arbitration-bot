package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/erc20"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/native"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

// transfersReport is the JSON output of `arbot transfers`.
type transfersReport struct {
	Safe    ethrpc.Address `json:"safe"`
	ChainID uint64         `json:"chainId"`
	// FromBlock and ToBlock are the blocks scanned, both included. ToBlock is the
	// request's Safe block. FromBlock is left out with -all, which reads the blocks
	// that the service names rather than a range.
	FromBlock *uint64 `json:"fromBlock,omitempty"`
	ToBlock   uint64  `json:"toBlock"`
	// Direction is "out" for the transfers out of the Safe, and "in" for those into
	// it.
	Direction string `json:"direction"`
	// From is the sender that the transfers into the Safe were limited to, if any.
	From *ethrpc.Address `json:"from,omitempty"`
	// To is the recipient that the transfers out of the Safe were limited to, if
	// any.
	To        *ethrpc.Address `json:"to,omitempty"`
	Transfers []transfer      `json:"transfers"`
	// Service is how the transfers were found with -all, and is left out without
	// it.
	Service *transfersService `json:"service,omitempty"`
}

// transfersService is how `arbot transfers -all` found the transfers, in the
// blocks of those that the Safe Transaction Service lists.
type transfersService struct {
	// serviceSource is the service, an offchain source (§ 3.4), and when it was
	// observed.
	serviceSource
	// Listed is the number of transfers of the kinds, the direction, and the sender
	// or recipient listed that the service lists up to the request's Safe block.
	Listed int `json:"listed"`
	// Blocks is the number of blocks that they are in, whose logs were read.
	Blocks int `json:"blocks"`
	// Unmatched is the number of them that the logs of their block don't have.
	Unmatched int `json:"unmatched"`
}

// transfer is a transfer in the output of `arbot transfers`. Type says what
// moved, "erc20" for an ERC-20 token, whose contract is Token, or "native" for
// the chain's native currency, which has no Token, so that other kinds can be
// added without changing the others.
type transfer struct {
	Type     string          `json:"type"`
	Block    uint64          `json:"block"`
	TxHash   ethrpc.Hash     `json:"txHash"`
	LogIndex uint64          `json:"logIndex"`
	Token    *ethrpc.Address `json:"token,omitempty"`
	From     ethrpc.Address  `json:"from"`
	To       ethrpc.Address  `json:"to"`
	// Value is the amount in the smallest unit of the token or the currency.
	Value *big.Int `json:"value"`
}

// transfers lists the token transfers out of a Safe's balance, or into it,
// before the proposal of a request.
func transfers(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("transfers")
	asJSON := flags.Bool("json", false, "write the transfers as JSON")
	requestFile := flags.String("request-file", "", "read the Safe, its chain, and the block from the request in the JSON file at `path`, as written by arbot info -json")
	in := flags.Bool("in", false, "list the transfers into the Safe instead of out of it")
	fromFlag := flags.String("from", "", "with -in, only list transfers from this `address`")
	toFlag := flags.String("to", "", "without -in, only list transfers to this `address`")
	blocks := flags.Uint64("blocks", defaultScanBlocks, "scan this number of blocks up to the request's Safe block")
	all := flags.Bool("all", false, "read the blocks up to the request's Safe block that have transfers that the Safe Transaction Service lists, instead of scanning the last -blocks blocks")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s transfers [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(flags.Output(), "Lists the ERC-20 transfers out of the Safe of a request, from the Safe's chain, as of the request's\n")
		fmt.Fprintf(flags.Output(), "Safe block: the Transfer events of any token with the Safe as sender, in the last -blocks blocks up to\n")
		fmt.Fprintf(flags.Output(), "it, without the transaction's own effects or anything later. It scans that range only, so a transfer\n")
		fmt.Fprintf(flags.Output(), "before it isn't listed. The events show that a token moved, not why: an account with an allowance can\n")
		fmt.Fprintf(flags.Output(), "move tokens out of the Safe without it making a transaction. Native and ERC-721 transfers out of the\n")
		fmt.Fprintf(flags.Output(), "Safe aren't listed.\n\n")
		fmt.Fprintf(flags.Output(), "With -in, it lists the transfers into the Safe instead, in the same range: the Transfer events of any\n")
		fmt.Fprintf(flags.Output(), "contract with the Safe as recipient, and the native currency that the Safe's SafeReceived events show,\n")
		fmt.Fprintf(flags.Output(), "with the caller that sent it. Any contract can log a Transfer event to the Safe, such as a fake token\n")
		fmt.Fprintf(flags.Output(), "in an address poisoning attack, so the token that logged it matters. Native currency that reaches the\n")
		fmt.Fprintf(flags.Output(), "Safe other than through a call with no data, such as the value of an execTransaction call, a block\n")
		fmt.Fprintf(flags.Output(), "reward, or a self-destruct, has no event and isn't listed, and a delegate call that the Safe makes can\n")
		fmt.Fprintf(flags.Output(), "log a SafeReceived event. ERC-721 transfers aren't listed.\n\n")
		fmt.Fprintf(flags.Output(), "With -all, it reads the blocks of the Safe's whole history up to the request's Safe block that have\n")
		fmt.Fprintf(flags.Output(), "transfers that the Safe Transaction Service lists, which it needs, instead of scanning a range, which\n")
		fmt.Fprintf(flags.Output(), "public nodes time out on for long histories. It reports the service, an offchain source, with its URL\n")
		fmt.Fprintf(flags.Output(), "and when it was observed. It lists the transfers that the logs of those blocks have, as above, so the\n")
		fmt.Fprintf(flags.Output(), "service only says which blocks to read. A block whose logs have no such transfer is read again once.\n")
		fmt.Fprintf(flags.Output(), "It warns about each transfer that the service lists that the logs don't have, such as native currency\n")
		fmt.Fprintf(flags.Output(), "without a SafeReceived event. A transfer in a block that the service doesn't name isn't listed, so the\n")
		fmt.Fprintf(flags.Output(), "list is only as complete as the service's index, and unlike with executions -all, nothing shows that\n")
		fmt.Fprintf(flags.Output(), "one is missing. It asks the service only for the transfers of the direction, sender or recipient, and\n")
		fmt.Fprintf(flags.Output(), "kinds listed, and fails if it lists more than 100,000 of them, such as spam transfers into the Safe\n")
		fmt.Fprintf(flags.Output(), "with -in, or more at one time than it lists at once, or doesn't know the Safe.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}
	if *in && *toFlag != "" {
		return usageError("-to only applies to transfers out of the Safe, without -in")
	}
	if !*in && *fromFlag != "" {
		return usageError("-from only applies to transfers into the Safe, with -in")
	}
	from, err := addressFlag("from", *fromFlag)
	if err != nil {
		return err
	}
	to, err := addressFlag("to", *toFlag)
	if err != nil {
		return err
	}

	ref, fromBlock, err := openRange(ctx, e, flags, *requestFile, *blocks, *all)
	if err != nil {
		return err
	}
	report := transfersReport{Safe: ref.Safe, ChainID: ref.ChainID, FromBlock: fromBlock, ToBlock: ref.SafeBlock, From: from, To: to, Transfers: []transfer{}}
	report.Direction = "out"
	if *in {
		report.Direction = "in"
	}
	// read appends the transfers in the blocks from first to last to the report.
	read := func(first, last uint64) error {
		var tokens []erc20.Transfer
		var err error
		if *in {
			if tokens, err = erc20.TransfersTo(ctx, ref.eth, ref.Safe, from, ethrpc.BlockNumber(first), ethrpc.BlockNumber(last)); err != nil {
				return err
			}
			received, err := native.ReceivedBy(ctx, ref.eth, ref.Safe, from, ethrpc.BlockNumber(first), ethrpc.BlockNumber(last))
			if err != nil {
				return err
			}
			for _, t := range received {
				report.Transfers = append(report.Transfers, transfer{
					Type: "native", Block: t.Block, TxHash: t.TxHash, LogIndex: t.LogIndex, From: t.From, To: t.To, Value: t.Value,
				})
			}
		} else if tokens, err = erc20.TransfersFrom(ctx, ref.eth, ref.Safe, to, ethrpc.BlockNumber(first), ethrpc.BlockNumber(last)); err != nil {
			return err
		}
		for _, t := range tokens {
			report.Transfers = append(report.Transfers, transfer{
				Type: "erc20", Block: t.Block, TxHash: t.TxHash, LogIndex: t.LogIndex, Token: &t.Token, From: t.From, To: t.To, Value: t.Value,
			})
		}
		return nil
	}

	if *all {
		service := txService(e, ref.ChainID)
		if service == nil {
			return errNoTxService("-all", ref.ChainID)
		}
		observed := time.Now().UTC().Truncate(time.Second)
		// The service filters the transfers by direction and kind, which keeps those of
		// the other direction, such as spam into the Safe, from filling the listing.
		filters := []txservice.TransferFilter{{Kind: "erc20", From: &ref.Safe, To: to, ToBlock: ref.SafeBlock}}
		if *in {
			filters = []txservice.TransferFilter{
				{Kind: "erc20", From: from, To: &ref.Safe, ToBlock: ref.SafeBlock},
				{Kind: "ether", From: from, To: &ref.Safe, ToBlock: ref.SafeBlock},
			}
		}
		var listed []txservice.Transfer
		for _, filter := range filters {
			found, err := service.Transfers(ctx, ref.Safe, filter)
			if errors.Is(err, txservice.ErrNotFound) {
				return fmt.Errorf("-all needs the Safe Transaction Service, which doesn't know the Safe %s", ref.Safe)
			}
			if err != nil {
				return fmt.Errorf("listing the transfers of %s from the Safe Transaction Service, which -all needs: %w", ref.Safe, err)
			}
			// The service isn't trusted to filter rightly, and the cap is on the transfers
			// of all the kinds listed together that it was asked for.
			found = slices.DeleteFunc(found, func(t txservice.Transfer) bool {
				return t.Block > ref.SafeBlock || !report.lists(t)
			})
			if listed = append(listed, found...); len(listed) > txservice.MaxListed {
				return fmt.Errorf("listing the transfers of %s from the Safe Transaction Service, which -all needs: the service lists more than %d transfers", ref.Safe, txservice.MaxListed)
			}
		}
		var blocks []uint64
		for _, t := range listed {
			blocks = append(blocks, t.Block)
		}
		slices.Sort(blocks)
		blocks = slices.Compact(blocks)
		for _, block := range blocks {
			before := len(report.Transfers)
			if err := read(block, block); err != nil {
				return err
			}
			// Public nodes behind load balancers now and then answer for a block that they
			// haven't caught up with, without its logs, so a block without transfers is
			// read once more before the service's are unmatched.
			if len(report.Transfers) == before {
				if err := read(block, block); err != nil {
					return err
				}
			}
		}
		report.Service = &transfersService{serviceSource: serviceSource{URL: service.URL(), ObservedAt: observed}, Listed: len(listed), Blocks: len(blocks)}
		found := make(map[ethrpc.Hash][]transfer)
		for _, x := range report.Transfers {
			found[x.TxHash] = append(found[x.TxHash], x)
		}
		for _, t := range listed {
			// Each transfer in the logs matches at most one that the service lists.
			if i := slices.IndexFunc(found[t.TxHash], func(x transfer) bool { return x.matches(t) }); i >= 0 {
				found[t.TxHash] = slices.Delete(found[t.TxHash], i, i+1)
			} else {
				report.Service.Unmatched++
				fmt.Fprintf(e.stderr, "%s: warning: the Safe Transaction Service lists a transfer of %s from %s to %s in transaction %s, in block %d, that the logs of the block don't have\n",
					e.progname, t.Value, t.From, t.To, t.TxHash, t.Block)
			}
		}
	} else if err := read(*fromBlock, ref.SafeBlock); err != nil {
		return err
	}
	slices.SortStableFunc(report.Transfers, func(a, b transfer) int {
		return cmp.Or(cmp.Compare(a.Block, b.Block), cmp.Compare(a.LogIndex, b.LogIndex))
	})

	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	var blockRange string
	if s := report.Service; s != nil {
		blockRange = fmt.Sprintf("up to block %d, in the blocks with transfers that the Safe Transaction Service lists: %d", report.ToBlock, s.Blocks)
	} else {
		blockRange = fmt.Sprintf("in blocks %d to %d", *report.FromBlock, report.ToBlock)
	}
	if *in {
		fmt.Fprintf(e.stdout, "Transfers to %s on chain %d, %s", report.Safe, report.ChainID, blockRange)
		if from != nil {
			fmt.Fprintf(e.stdout, ", from %s", from)
		}
	} else {
		fmt.Fprintf(e.stdout, "ERC-20 transfers from %s on chain %d, %s", report.Safe, report.ChainID, blockRange)
		if to != nil {
			fmt.Fprintf(e.stdout, ", to %s", to)
		}
	}
	fmt.Fprintf(e.stdout, "\n\n")
	if s := report.Service; s != nil {
		fmt.Fprintf(e.stdout, "Listed by the Safe Transaction Service %q, observed %s: %d, not in the logs of their blocks: %d\n\n",
			s.URL, s.ObservedAt.Format(time.RFC3339), s.Listed, s.Unmatched)
	}
	if len(report.Transfers) == 0 {
		_, err = fmt.Fprintln(e.stdout, "none")
		return err
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	if *in {
		fmt.Fprintln(w, "BLOCK\tTRANSACTION\tTYPE\tTOKEN\tFROM\tVALUE")
		for _, t := range report.Transfers {
			token := "-"
			if t.Token != nil {
				token = t.Token.String()
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", t.Block, t.TxHash, t.Type, token, t.From, t.Value)
		}
	} else {
		fmt.Fprintln(w, "BLOCK\tTRANSACTION\tTOKEN\tTO\tVALUE")
		for _, t := range report.Transfers {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", t.Block, t.TxHash, t.Token, t.To, t.Value)
		}
	}
	return w.Flush()
}

// lists reports whether the report lists transfers of the kind of t, which the
// Safe Transaction Service lists: in its direction, between its addresses, if
// any, and ERC-20 transfers, or into the Safe, native ones.
func (r *transfersReport) lists(t txservice.Transfer) bool {
	if r.Direction == "in" {
		return t.To == r.Safe && (r.From == nil || t.From == *r.From) &&
			(t.Type == "ERC20_TRANSFER" && t.Token != nil || t.Type == "ETHER_TRANSFER" && t.Token == nil)
	}
	return t.From == r.Safe && (r.To == nil || t.To == *r.To) && t.Type == "ERC20_TRANSFER" && t.Token != nil
}

// serviceTypes are the Safe Transaction Service's types of the transfers, by
// their type in the output.
var serviceTypes = map[string]string{"erc20": "ERC20_TRANSFER", "native": "ETHER_TRANSFER"}

// matches reports whether x is the transfer t that the Safe Transaction Service
// lists.
func (x *transfer) matches(t txservice.Transfer) bool {
	return serviceTypes[x.Type] == t.Type && x.Block == t.Block && x.TxHash == t.TxHash && (t.LogIndex == nil || x.LogIndex == *t.LogIndex) &&
		x.From == t.From && x.To == t.To && (x.Token == nil) == (t.Token == nil) && (x.Token == nil || *x.Token == *t.Token) &&
		t.Value != nil && x.Value.Cmp(t.Value) == 0
}

// addressFlag parses the value of the address flag name, which is nil if the
// flag isn't set.
func addressFlag(name, value string) (*ethrpc.Address, error) {
	if value == "" {
		return nil, nil
	}
	address, err := ethrpc.ParseAddress(value)
	if err != nil {
		return nil, usageError(fmt.Sprintf("-%s %q: %v", name, value, err))
	}
	return &address, nil
}
