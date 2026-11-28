package executions

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

// decoder decodes the Safe transactions of executions, caching what several of
// them need: the Safe's nonce, and the transaction and trace of an onchain
// transaction that executes more than one.
type decoder struct {
	*Finder
	safe ethrpc.Address
	// to is the last block that the search covers.
	to         ethrpc.BlockNumber
	executions []Execution
	events     map[ethrpc.Hash][]*safenet.SafeTransaction
	// listed are the transactions that the service lists, by Safe transaction hash,
	// each of which has its hash, so that the service isn't asked for them again.
	listed map[ethrpc.Hash]*safenet.SafeTransaction

	// byBlock is whether an execution's nonce is worked out from the Safe's nonce
	// at the end of its own block rather than at to, for a search that doesn't read
	// every block up to to.
	byBlock bool
	// nexts are the Safe's nonces at the end of the blocks that they were read at,
	// or nil for one that couldn't be read, and nonceErrs the errors of the reads
	// that got no answer.
	nexts     map[uint64]*big.Int
	nonceErrs map[uint64]error
	// nonceLists are the nonces that the executions of each block may have used, by
	// block (see nonces).
	nonceLists map[uint64]nonceList
	// calldata and traces are the Safe transactions that the calldata and a trace
	// of an onchain transaction have, by hash (see byHash).
	calldata map[ethrpc.Hash]map[ethrpc.Hash]*safenet.SafeTransaction
	traces   map[ethrpc.Hash]map[ethrpc.Hash]*safenet.SafeTransaction
	// untraced is whether the node refused a trace, or doesn't serve them, so that
	// it isn't asked for more.
	untraced bool
	// err is the first request to the node or the service that failed without an
	// answer. Such a failure says nothing about the execution, so decoding stops
	// rather than report it as unknown.
	err error
}

// decodeAll decodes the Safe transaction of each execution, and sets its
// source.
func (d *decoder) decodeAll(ctx context.Context) error {
	for i := range d.executions {
		e := &d.executions[i]
		var err error
		if e.Source, e.Transaction, err = d.decode(ctx, i); err != nil {
			return err
		}
	}
	return nil
}

// decode returns the Safe transaction of executions[i] and where it is from, or
// nothing if no source has it. It returns an error if a request to the node or
// the service failed without an answer, since the transaction could then be in
// a source that it couldn't read, unless the request was for the Safe's nonce
// and another source has the transaction.
func (d *decoder) decode(ctx context.Context, i int) (Source, *safenet.SafeTransaction, error) {
	source, tx := d.decodeSources(ctx, i)
	if d.err != nil {
		return "", nil, fmt.Errorf("decoding the Safe transaction %s: %w", d.executions[i].SafeTxHash, d.err)
	}
	return source, tx, nil
}

func (d *decoder) decodeSources(ctx context.Context, i int) (Source, *safenet.SafeTransaction) {
	e := d.executions[i]
	for _, tx := range d.events[e.TxHash] {
		if matches(tx, e.SafeTxHash) {
			return SourceEvent, tx
		}
	}
	// A transaction that the service listed has its hash already, which proves it
	// is the one executed, so it needs no request to the node.
	if tx := d.listed[e.SafeTxHash]; tx != nil {
		return SourceService, tx
	}

	// The calldata and traces have no nonce, so it is worked out from the Safe's. A
	// nonce that the node failed to read leaves them out, which only fails the
	// decoding if no other source has the transaction.
	nonces, nonceErr := d.nonces(ctx, i)
	if len(nonces) > 0 {
		if tx := d.fromCalldata(ctx, i, nonces); tx != nil {
			return SourceCalldata, tx
		}
		if d.err != nil {
			return "", nil
		}
		if tx := d.fromTrace(ctx, i, nonces); tx != nil {
			return SourceTrace, tx
		}
	}
	if d.err != nil {
		return "", nil
	}

	if d.Service != nil {
		x, err := d.Service.Transaction(ctx, e.SafeTxHash)
		// A service that fails says nothing about the transaction, as a node doesn't.
		if txservice.Unavailable(err) {
			d.fail(fmt.Errorf("getting Safe transaction %s from the Safe Transaction Service: %w", e.SafeTxHash, err))
			return "", nil
		}
		if err != nil && !errors.Is(err, txservice.ErrNotFound) {
			d.warn(fmt.Errorf("getting Safe transaction %s from the Safe Transaction Service: %w", e.SafeTxHash, err))
		}
		if err == nil && matches(x.Transaction, e.SafeTxHash) {
			return SourceService, x.Transaction
		}
	}
	if nonceErr != nil {
		d.fail(fmt.Errorf("the calldata and traces need the Safe's nonce, and no other source has the transaction: %w", nonceErr))
	}
	return "", nil
}

// nonces returns the nonces that executions[i] may have used, or none if they
// are unknown, with the error of the read of the Safe's nonce if it got no
// answer. Every execution uses the Safe's next nonce, so the executions of a
// range of blocks that the search read in full used the nonces just below the
// Safe's nonce at its end: up to to for Find, and the execution's own block for
// FindAll. The executions are in the order of their logs, so the nonce of
// executions[i] is the one that its position gives, which comes first, then
// those above it and those below. But an execution nested in another logs
// before it, so executions[i] may have used any of the nonces of its block's
// executions, and the hash check proves which. The nonces are worked out once
// for each block, and those of its executions are windows of one list.
func (d *decoder) nonces(ctx context.Context, i int) ([]*big.Int, error) {
	block := d.executions[i].Block
	list, ok := d.nonceLists[block]
	if !ok {
		first, last := d.block(i)
		at, after := uint64(d.to), len(d.executions)-last-1
		if d.byBlock {
			at, after = block, 0
		}
		next, err := d.nonceAt(ctx, at)
		list.first, list.err = first, err
		if next != nil {
			// The block's executions used as many nonces just below top as there are of
			// them, and the position of executions[first] gives the lowest.
			top := new(big.Int).Sub(next, big.NewInt(int64(after)))
			for below := last - first + 1; below > 0; below-- {
				if nonce := new(big.Int).Sub(top, big.NewInt(int64(below))); nonce.Sign() >= 0 {
					list.nonces = append(list.nonces, nonce)
				} else {
					list.first++
				}
			}
			list.nonces = append(list.nonces, list.nonces...)
		}
		if d.nonceLists == nil {
			d.nonceLists = make(map[uint64]nonceList)
		}
		d.nonceLists[block] = list
	}
	n := len(list.nonces) / 2
	if n == 0 {
		return nil, list.err
	}
	start := max(0, i-list.first)
	return list.nonces[start : start+n], nil
}

// nonceList is the nonces that the executions of a block may have used, in the
// order that their positions give them, twice, so that those from any position
// on, followed by those before it, are a window of it. first is the index of
// the execution whose position gives the first of them, and err the error of
// the read of the Safe's nonce if it got no answer.
type nonceList struct {
	nonces []*big.Int
	first  int
	err    error
}

// block returns the indexes of the first and last executions in the block of
// executions[i].
func (d *decoder) block(i int) (first, last int) {
	first, last = i, i
	for first > 0 && d.executions[first-1].Block == d.executions[i].Block {
		first--
	}
	for last+1 < len(d.executions) && d.executions[last+1].Block == d.executions[i].Block {
		last++
	}
	return first, last
}

// nonceAt returns the Safe's nonce at the end of block, or nil if it can't be
// read, with the error of the read if it got no answer. It reads it once for
// each block, with a warning if that fails.
func (d *decoder) nonceAt(ctx context.Context, block uint64) (*big.Int, error) {
	next, ok := d.nexts[block]
	if !ok {
		var err error
		next, err = d.ReadNonce(ctx, d.safe, ethrpc.BlockNumber(block))
		if err != nil {
			d.warn(err)
		}
		// The account logged executions, so the only answer that isn't a nonce is its
		// code's, such as after a delegate call replaced its singleton. Any other error
		// is about the node, such as a block whose state it doesn't have.
		if _, answer := errors.AsType[notNonce](err); err != nil && !answer && !ethrpc.Reverted(err) && !ethrpc.Halted(err) {
			if d.nonceErrs == nil {
				d.nonceErrs = make(map[uint64]error)
			}
			d.nonceErrs[block] = err
		}
		if d.nexts == nil {
			d.nexts = make(map[uint64]*big.Int)
		}
		d.nexts[block] = next
	}
	return next, d.nonceErrs[block]
}

// fromCalldata decodes the Safe transaction of executions[i] from the calldata
// of its onchain transaction, if that calls the Safe's execTransaction with a
// transaction that used one of nonces and has the hash that it logged.
func (d *decoder) fromCalldata(ctx context.Context, i int, nonces []*big.Int) *safenet.SafeTransaction {
	e := d.executions[i]
	txs, ok := d.calldata[e.TxHash]
	if !ok {
		tx, err := d.Eth.GetTransactionByHash(ctx, e.TxHash)
		// The node logged the transaction, so the only error that is an answer is not
		// having it, from a node that only indexes recent transactions.
		if err != nil && !errors.Is(err, ethrpc.ErrNotFound) {
			d.fail(fmt.Errorf("getting transaction %s: %w", e.TxHash, err))
			return nil
		}
		if err != nil {
			d.warn(fmt.Errorf("getting transaction %s: %w", e.TxHash, err))
		} else if tx.To != nil && *tx.To == d.safe {
			txs = d.byHash(i, []ethrpc.Bytes{tx.Input}, nonces)
		}
		if d.calldata == nil {
			d.calldata = make(map[ethrpc.Hash]map[ethrpc.Hash]*safenet.SafeTransaction)
		}
		d.calldata[e.TxHash] = txs
	}
	return txs[e.SafeTxHash]
}

// fromTrace decodes the Safe transaction of executions[i] from the calls to the
// Safe's execTransaction in a trace of its onchain transaction.
func (d *decoder) fromTrace(ctx context.Context, i int, nonces []*big.Int) *safenet.SafeTransaction {
	e := d.executions[i]
	txs, ok := d.traces[e.TxHash]
	if !ok && !d.untraced {
		var root *ethrpc.CallFrame
		// Most public nodes don't serve traces, so the next source may have the
		// transaction. A node that says that it doesn't serve the method, or refuses it
		// with an HTTP error status rather than a JSON-RPC error, such as a 401 or 406
		// page, which is a warning, would answer every trace the same way, so it isn't
		// asked for more. Another error, such as a server error or a 408 Request
		// Timeout, which may be brief, or for a trace that timed out or of a block
		// whose state the node doesn't keep, is a warning about this transaction only.
		// A rate limit or a dropped connection is a failure, though, since the node may
		// serve traces, and so is a result that isn't a callTracer trace, such as null.
		err := d.Eth.RawRequest(ctx, &root, "debug_traceTransaction", e.TxHash, map[string]any{"tracer": "callTracer"})
		// An HTTP rate limit is one whatever JSON-RPC error its body has.
		httpErr, refused := errors.AsType[*ethrpc.HTTPError](err)
		limited := refused && httpErr.StatusCode == http.StatusTooManyRequests
		refused = refused && !limited && ethrpc.Transient(err)
		switch {
		case err == nil && root == nil:
			d.fail(fmt.Errorf("tracing transaction %s: debug_traceTransaction: null result", e.TxHash))
			return nil
		case err == nil:
			if err := root.Check(); err != nil {
				d.fail(fmt.Errorf("tracing transaction %s: debug_traceTransaction: %w", e.TxHash, err))
				return nil
			}
			txs = d.byHash(i, callsTo(root, d.safe, nil), nonces)
		case unserved(err):
			d.untraced = true
		case refused && httpErr.StatusCode < http.StatusInternalServerError && httpErr.StatusCode != http.StatusRequestTimeout:
			d.warn(fmt.Errorf("the node refused a trace, so no more are asked for: tracing transaction %s: %w", e.TxHash, err))
			d.untraced = true
		case limited, !refused && ethrpc.Transient(err) && !ethrpc.TimedOut(err):
			d.fail(fmt.Errorf("tracing transaction %s: %w", e.TxHash, err))
			return nil
		default:
			d.warn(fmt.Errorf("tracing transaction %s: %w", e.TxHash, err))
		}
		if d.traces == nil {
			d.traces = make(map[ethrpc.Hash]map[ethrpc.Hash]*safenet.SafeTransaction)
		}
		d.traces[e.TxHash] = txs
	}
	return txs[e.SafeTxHash]
}

// byHash returns the Safe transactions that the calldata of calls to
// execTransaction in the onchain transaction of executions[i] have, given that
// they used one of nonces, by hash, if the Safe logged their executions in it.
// A trace may have a call for each execution, so each is decoded once for all
// of them. A Safe transaction takes its nonce when its execution starts, so the
// calls, in the order that they start, use consecutive nonces, the first of
// them usually the first of nonces: each call tries the nonce after the last
// match first, and stops at its match. Calls with the same calldata may have
// used other nonces, so a call skips the hashes that earlier calls matched.
func (d *decoder) byHash(i int, inputs []ethrpc.Bytes, nonces []*big.Int) map[ethrpc.Hash]*safenet.SafeTransaction {
	logged := make(map[ethrpc.Hash]bool)
	first, last := d.block(i)
	for _, e := range d.executions[first : last+1] {
		if e.TxHash == d.executions[i].TxHash {
			logged[e.SafeTxHash] = true
		}
	}
	txs := make(map[ethrpc.Hash]*safenet.SafeTransaction)
	next := 0
	for _, input := range inputs {
		decoded := decodeCall(d.safe, d.Eth.ChainID(), input)
		if decoded == nil {
			continue
		}
		for j := range nonces {
			tx := *decoded
			tx.Nonce = nonces[(next+j)%len(nonces)]
			// The fields are ABI words, so they can be hashed before matches checks them. A
			// Safe before 1.3.0 logs the hash in a domain without the chain ID.
			hash := tx.Hash()
			if !logged[hash] {
				hash = tx.LegacyHash()
			}
			if logged[hash] && txs[hash] == nil && matches(&tx, hash) {
				txs[hash], next = &tx, next+j+1
				break
			}
		}
	}
	return txs
}

// unserved reports whether err is a node's answer that it doesn't serve the
// method: JSON-RPC's "Method not found", or "Invalid Request", which some nodes
// answer for a method that they don't allow, or EIP-1474's
// "Method not supported". A transient error is never one, even if its body has
// one of those, since the node didn't answer.
func unserved(err error) bool {
	rpcErr, ok := errors.AsType[*ethrpc.Error](err)
	return ok && !ethrpc.Transient(err) && (rpcErr.Code == -32601 || rpcErr.Code == -32600 || rpcErr.Code == -32004)
}

func (d *decoder) fail(err error) {
	if d.err == nil {
		d.err = err
	}
}

func (f *Finder) warn(err error) {
	if f.Warn != nil {
		f.Warn(err)
	}
}

// callsTo appends to inputs the calldata of the calls to safe in the frame and
// the calls it makes, in the order that they start. A call that reverted, and
// the calls in it, are left out, since their effects were undone.
func callsTo(f *ethrpc.CallFrame, safe ethrpc.Address, inputs []ethrpc.Bytes) []ethrpc.Bytes {
	if f.Error != "" {
		return inputs
	}
	if f.Type == "CALL" && f.To != nil && *f.To == safe {
		inputs = append(inputs, f.Input)
	}
	for i := range f.Calls {
		inputs = callsTo(&f.Calls[i], safe, inputs)
	}
	return inputs
}
