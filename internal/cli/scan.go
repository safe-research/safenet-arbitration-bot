package cli

import (
	"context"
	"fmt"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// defaultScanBlocks is how many blocks before the Safe block that the commands
// that read a Safe's history scan by default, which is about two weeks on
// Ethereum Mainnet. Public nodes answer a scan of a long history slowly, so it
// is bounded.
const defaultScanBlocks = 100_000

// scan is a range of a Safe's history that a command reads, as of the proposal
// of a request: the blocks of the Safe's chain up to the request's Safe block,
// without the transaction's own effects or anything later.
type scan struct {
	Safe    ethrpc.Address
	ChainID uint64
	// FromBlock and ToBlock are the first and last blocks of the range, which is
	// blocks long unless it starts at the first block of the chain. ToBlock is the
	// request's Safe block.
	FromBlock, ToBlock uint64
	eth                *ethrpc.Client
}

// openScan reads the request in the file at requestFile, as written by arbot
// info -json, and returns a scan of the last blocks up to its Safe block, with
// a client for the Safe's chain. An empty requestFile or zero blocks is a usage
// error.
func openScan(ctx context.Context, e *env, requestFile string, blocks uint64) (*scan, error) {
	if requestFile == "" {
		return nil, usageError("-request-file is required")
	}
	if blocks == 0 {
		return nil, usageError("-blocks must be at least 1")
	}
	request, err := readRequest(requestFile)
	if err != nil {
		return nil, err
	}
	tx := request.Proposal.Transaction
	// A zero block would scan the chain's first blocks instead of those before the
	// proposal.
	if tx.ChainID == nil || !tx.ChainID.IsUint64() || request.Proposal.SafeBlock == 0 {
		return nil, fmt.Errorf("request in %s has no Safe chain ID or block", requestFile)
	}
	s := &scan{Safe: tx.Safe, ChainID: tx.ChainID.Uint64(), ToBlock: request.Proposal.SafeBlock}
	s.FromBlock = s.ToBlock - min(blocks-1, s.ToBlock)
	if s.eth, err = ethrpc.NewDialer(e.cfg.RPCs)(ctx, s.ChainID); err != nil {
		return nil, err
	}
	return s, nil
}
