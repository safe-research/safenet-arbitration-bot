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
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s charter [flags]\n\n", e.progname)
		fmt.Fprintf(flags.Output(), "Fetches the Safenet Arbitration Charter referenced by %s and writes it to stdout.\n\n", charterName)
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
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

	url, err := resolver.Resolve(ctx, charterName, at)
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
