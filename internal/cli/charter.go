package cli

import (
	"context"
	"fmt"

	"github.com/safe-research/safenet-arbitration-bot/internal/ens"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/ipfs"
)

// charterName is the ENS name whose content hash record references the
// effective Safenet Arbitration Charter.
const charterName = "charter.safenet-gov.eth"

// charter fetches the Safenet Arbitration Charter and writes it to stdout.
func charter(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("charter")
	block := flags.Uint64("block", 0, "Ethereum Mainnet block to read the Charter version at (default: latest)")
	requestFile := flags.String("request-file", "", "fetch the Charter that applies to the request in the JSON file at `path`, as written by arbot info -json")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s charter [flags]\n\n", e.progname)
		fmt.Fprintf(flags.Output(), "Fetches the Safenet Arbitration Charter referenced by %s and writes it to stdout.\n", charterName)
		fmt.Fprintf(flags.Output(), "With -request-file, it fetches the version that the request's Charter name references at its proposal's ethereumBlock.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}
	name := charterName
	if *requestFile != "" {
		if *block != 0 {
			return usageError("-block and -request-file are mutually exclusive")
		}
		request, err := readRequest(*requestFile)
		if err != nil {
			return err
		}
		// A zero block would read the latest Charter version instead.
		if request.Proposal.EthereumBlock == 0 || request.Charter == "" {
			return fmt.Errorf("request in %s has no Charter name or Ethereum Mainnet block", *requestFile)
		}
		name, *block = request.Charter, request.Proposal.EthereumBlock
	}

	eth, err := ethrpc.NewClient(ctx, ethrpc.Mainnet, e.cfg.RPCs[ethrpc.Mainnet])
	if err != nil {
		return err
	}
	resolver, err := ens.NewClient(eth)
	if err != nil {
		return err
	}
	at := ethrpc.BlockNumber(*block)
	if at == 0 {
		if at, err = eth.BlockNumber(ctx); err != nil {
			return err
		}
	}

	url, err := resolver.Resolve(ctx, name, at)
	if err != nil {
		return err
	}
	cid, err := ipfs.ParseCIDFromURL(url)
	if err != nil {
		return err
	}
	content, err := ipfs.NewClient(e.cfg.IPFS).Fetch(ctx, cid)
	if err != nil {
		return err
	}
	_, err = e.stdout.Write(content)
	return err
}
