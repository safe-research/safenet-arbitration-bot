package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/safe-research/safenet-arbitration-bot/internal/config"
	"github.com/safe-research/safenet-arbitration-bot/internal/ens"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/ipfs"
)

// charterName is the ENS name whose content hash record references the
// effective Safenet Arbitration Charter.
const charterName = "charter.safenet-gov.eth"

// charter fetches the Safenet Arbitration Charter and writes it to stdout.
func charter(ctx context.Context, cfg config.Config, args []string) error {
	flags := flag.NewFlagSet("charter", flag.ExitOnError)
	block := flags.Uint64("block", 0, "Ethereum Mainnet block to read the Charter version at (default: latest)")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s charter [flags]\n\n", progname)
		fmt.Fprintf(flags.Output(), "Fetches the Safenet Arbitration Charter referenced by %s and writes it to stdout.\n\n", charterName)
		flags.PrintDefaults()
	}
	flags.Parse(args)
	if flags.NArg() != 0 {
		flags.Usage()
		os.Exit(2)
	}

	eth, err := ethrpc.NewClient(ctx, ethrpc.Mainnet, cfg.RPCs[ethrpc.Mainnet])
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
	content, err := ipfs.NewClient(cfg.IPFS).Fetch(ctx, cid)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(content)
	return err
}
