package main

import (
	"cmp"
	"context"
	"encoding/json"
	"os"

	"github.com/safe-research/safenet-arbitration-bot/internal/config"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// openSafenet returns a Safenet that reads the configured SentinelOracle and
// Consensus contracts on Gnosis Chain, and the block to read at: block, or the
// latest block if block is zero.
func openSafenet(ctx context.Context, cfg config.Config, block uint64) (*safenet.Safenet, ethrpc.BlockNumber, error) {
	oracle := cmp.Or(cfg.Oracle, safenet.DefaultOracle)
	consensus := cmp.Or(cfg.Consensus, safenet.DefaultConsensus)

	eth, err := ethrpc.NewClient(ctx, ethrpc.Gnosis, cfg.RPCs[ethrpc.Gnosis])
	if err != nil {
		return nil, 0, err
	}
	at := ethrpc.BlockNumber(block)
	if at == 0 {
		if at, err = eth.BlockNumber(ctx); err != nil {
			return nil, 0, err
		}
	}
	return safenet.New(eth, oracle, consensus), at, nil
}

// writeJSON writes v to stdout as indented JSON.
func writeJSON(v any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}
