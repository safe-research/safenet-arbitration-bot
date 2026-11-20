package executions

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

// History is every Safe transaction that a Safe executed up to a block, as
// FindAll finds them.
type History struct {
	// Nonce is the Safe's nonce at the last block. Every execution uses the next
	// nonce, so the Safe executed this number of Safe transactions up to it, unless
	// a delegate call changed its nonce.
	Nonce *big.Int
	// Executions are the executions found, in the order of their logs.
	Executions []Execution
	// Missing are the ranges of nonces below Nonce that no execution found used, in
	// order. It is nil if Nonce is too large to list them.
	Missing []NonceRange
	// Rejected are the executions that the service lists up to the last block but
	// that the chain doesn't confirm, in the order that the service lists them.
	Rejected []Rejected
}

// Complete reports whether the executions found account for every nonce below
// the Safe's nonce.
func (h *History) Complete() bool {
	return h.Missing != nil && len(h.Missing) == 0
}

// NonceRange is a range of nonces, From to To, including both.
type NonceRange struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
}

// Rejected is an execution that the Safe Transaction Service lists but the
// chain doesn't confirm, with the reason.
type Rejected struct {
	SafeTxHash ethrpc.Hash `json:"safeTxHash"`
	// TxHash is the onchain transaction that the service says executed it, which is
	// the zero hash if it gives none.
	TxHash ethrpc.Hash `json:"txHash"`
	// Nonce is the nonce that the service gives the transaction, or nil if it gives
	// none.
	Nonce  *big.Int `json:"nonce"`
	Reason string   `json:"reason"`
}

// FindAll returns the Safe transactions that the Safe executed up to the block
// to, in order, in the blocks that the Safe Transaction Service names.
//
// A Safe's whole history is too long to scan the logs of on public nodes, so it
// lists the Safe's executions from the Safe Transaction Service, which must be
// set, and uses them to find the blocks to read. It reads the Safe's logs in
// each block that the service names up to to, and keeps the executions that the
// Safe logged there, whether the service lists them rightly or not, which it
// decodes like Find, taking a transaction that the service lists with its hash
// from the listing. A block whose logs have no execution of the Safe is read
// once more, since a node behind a load balancer now and then answers for a
// block that it hasn't caught up with.
//
// It only confirms an execution that the service lists if its fields hash to
// the Safe transaction hash that the service reports, and the Safe logged the
// execution of that hash in the block and the onchain transaction that the
// service reports. It rejects any other, with the reason. One that the service
// lists after to isn't evidence of the history up to to, so it is left out.
//
// The Safe's nonce at to is the number of executions up to it, so the history
// is complete if the executions found use every nonce below it. A Safe that the
// service doesn't know is a warning, since its nonce still shows what the
// history leaves out.
//
// Executions by modules don't use nonces, and the service doesn't list them, so
// they aren't found. Any request to the node that fails, such as for a rate
// limit, fails FindAll, since it says nothing about the history.
func (f *Finder) FindAll(ctx context.Context, safe ethrpc.Address, to ethrpc.BlockNumber) (*History, error) {
	if f.Service == nil {
		return nil, errors.New("finding every execution needs the Safe Transaction Service")
	}
	nonce, err := f.readNonce(ctx, safe, to)
	if err != nil {
		return nil, err
	}
	listed, err := f.Service.Executions(ctx, safe)
	switch {
	case errors.Is(err, txservice.ErrNotFound):
		f.warn(fmt.Errorf("the Safe Transaction Service doesn't know %s", safe))
	case err != nil:
		return nil, fmt.Errorf("listing the executions of %s from the Safe Transaction Service: %w", safe, err)
	}

	// The transactions that the service lists, by hash, if their hash checks out,
	// whatever it says about their execution, and the blocks up to to that it
	// names.
	transactions := make(map[ethrpc.Hash]*safenet.SafeTransaction)
	var blocks []uint64
	for _, x := range listed {
		if x.Transaction.Safe == safe && matches(x.Transaction, x.SafeTxHash) {
			transactions[x.SafeTxHash] = x.Transaction
		}
		if x.Block != 0 && x.Block <= uint64(to) {
			blocks = append(blocks, x.Block)
		}
	}
	slices.Sort(blocks)
	c := newCollector(f, safe)
	for _, block := range slices.Compact(blocks) {
		read, err := c.scan(ctx, ethrpc.BlockNumber(block), ethrpc.BlockNumber(block))
		if err == nil && read == 0 {
			_, err = c.scan(ctx, ethrpc.BlockNumber(block), ethrpc.BlockNumber(block))
		}
		if err != nil {
			return nil, err
		}
	}

	// The blocks are read in order, so the executions are in the order of their
	// logs. The nonce is already read, and is that of the last execution, plus one,
	// if the history is complete.
	d := &decoder{
		Finder: f, safe: safe, to: to, executions: c.executions, events: c.events, listed: transactions,
		nonceRead: true, nonceNext: nonce,
	}
	if err := d.decodeAll(ctx); err != nil {
		return nil, err
	}
	h := &History{Nonce: nonce, Executions: c.executions, Rejected: []Rejected{}}

	for _, x := range listed {
		if x.Block > uint64(to) {
			continue
		}
		if reason := confirm(x, safe, h.Executions); reason != "" {
			h.Rejected = append(h.Rejected, Rejected{SafeTxHash: x.SafeTxHash, TxHash: x.TxHash, Nonce: x.Transaction.Nonce, Reason: reason})
		}
	}
	if nonce.IsUint64() {
		h.Missing = missing(h.Executions, nonce.Uint64())
	} else {
		f.warn(fmt.Errorf("the Safe's nonce %s is too large to list the nonces that no execution used", nonce))
	}
	return h, nil
}

// confirm returns why the chain doesn't confirm the execution x that the
// service lists, given the executions of the Safe found, or "" if it does.
func confirm(x txservice.Execution, safe ethrpc.Address, executions []Execution) string {
	switch {
	case x.Block == 0 || x.TxHash == (ethrpc.Hash{}):
		return "the service gives no onchain transaction or block"
	case x.Transaction.Safe != safe:
		return fmt.Sprintf("the transaction is for the Safe %s", x.Transaction.Safe)
	case !matches(x.Transaction, x.SafeTxHash):
		return "the transaction's fields don't hash to its Safe transaction hash"
	}
	var in []Execution
	for _, e := range executions {
		if e.SafeTxHash == x.SafeTxHash && e.Block == x.Block {
			if e.TxHash == x.TxHash {
				return ""
			}
			in = append(in, e)
		}
	}
	if len(in) > 0 {
		return fmt.Sprintf("the Safe logged its execution in block %d in the transaction %s, not the one that the service reports", x.Block, in[0].TxHash)
	}
	return fmt.Sprintf("the Safe logged no execution of it in block %d", x.Block)
}

// missing returns the ranges of the nonces below next that none of executions
// used.
func missing(executions []Execution, next uint64) []NonceRange {
	var used []uint64
	for _, e := range executions {
		if tx := e.Transaction; tx != nil && tx.Nonce.IsUint64() && tx.Nonce.Uint64() < next {
			used = append(used, tx.Nonce.Uint64())
		}
	}
	slices.Sort(used)
	ranges := []NonceRange{}
	from := uint64(0)
	for _, n := range slices.Compact(used) {
		if n > from {
			ranges = append(ranges, NonceRange{From: from, To: n - 1})
		}
		from = n + 1
	}
	if from < next {
		ranges = append(ranges, NonceRange{From: from, To: next - 1})
	}
	return ranges
}
