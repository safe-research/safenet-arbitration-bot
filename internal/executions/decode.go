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
	txs       map[ethrpc.Hash]*ethrpc.Transaction
	traces    map[ethrpc.Hash][]ethrpc.Bytes
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
	nonce, nonceErr := d.nonce(ctx, i)
	if nonce != nil {
		if tx := d.fromCalldata(ctx, e, nonce); tx != nil {
			return SourceCalldata, tx
		}
		if d.err != nil {
			return "", nil
		}
		if tx := d.fromTrace(ctx, e, nonce); tx != nil {
			return SourceTrace, tx
		}
	}
	if d.err != nil {
		return "", nil
	}

	if d.Service != nil {
		tx, err := d.Service.Transaction(ctx, e.SafeTxHash)
		// A service that fails says nothing about the transaction, as a node doesn't.
		if txservice.Unavailable(err) {
			d.fail(fmt.Errorf("getting Safe transaction %s from the Safe Transaction Service: %w", e.SafeTxHash, err))
			return "", nil
		}
		if err != nil && !errors.Is(err, txservice.ErrNotFound) {
			d.warn(fmt.Errorf("getting Safe transaction %s from the Safe Transaction Service: %w", e.SafeTxHash, err))
		}
		if err == nil && matches(tx, e.SafeTxHash) {
			return SourceService, tx
		}
	}
	if nonceErr != nil {
		d.fail(fmt.Errorf("the calldata and traces need the Safe's nonce, and no other source has the transaction: %w", nonceErr))
	}
	return "", nil
}

// nonce returns the nonce that executions[i] used, or nil if it is unknown,
// with the error of the read of the Safe's nonce if it got no answer. The
// Safe's nonce is read once for each block, with a warning if that fails. Every
// execution uses the Safe's next nonce, so the executions of a range of blocks
// that the search read in full used the nonces just below the Safe's nonce at
// its end: up to to for Find, and the execution's own block for FindAll. The
// executions are in the order of their logs.
func (d *decoder) nonce(ctx context.Context, i int) (*big.Int, error) {
	block, later := uint64(d.to), len(d.executions)-i-1
	if d.byBlock {
		block, later = d.executions[i].Block, 0
		for _, e := range d.executions[i+1:] {
			if e.Block != block {
				break
			}
			later++
		}
	}
	next, ok := d.nexts[block]
	if !ok {
		var err error
		next, err = d.readNonce(ctx, d.safe, ethrpc.BlockNumber(block))
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
	if next == nil {
		return nil, d.nonceErrs[block]
	}
	nonce := new(big.Int).Sub(next, big.NewInt(int64(later+1)))
	if nonce.Sign() < 0 {
		return nil, nil
	}
	return nonce, nil
}

// fromCalldata decodes the Safe transaction of e from the calldata of its
// onchain transaction, if that calls the Safe's execTransaction with a
// transaction that used nonce and has the hash that e has.
func (d *decoder) fromCalldata(ctx context.Context, e Execution, nonce *big.Int) *safenet.SafeTransaction {
	tx, ok := d.txs[e.TxHash]
	if !ok {
		var err error
		result, err := d.Eth.GetTransactionByHash(ctx, e.TxHash)
		// The node logged the transaction, so the only error that is an answer is not
		// having it, from a node that only indexes recent transactions.
		if err != nil && !errors.Is(err, ethrpc.ErrNotFound) {
			d.fail(fmt.Errorf("getting transaction %s: %w", e.TxHash, err))
			return nil
		}
		if err != nil {
			d.warn(fmt.Errorf("getting transaction %s: %w", e.TxHash, err))
		} else {
			tx = &result
		}
		if d.txs == nil {
			d.txs = make(map[ethrpc.Hash]*ethrpc.Transaction)
		}
		d.txs[e.TxHash] = tx
	}
	if tx == nil || tx.To == nil || *tx.To != d.safe {
		return nil
	}
	return d.candidate(tx.Input, e, nonce)
}

// fromTrace decodes the Safe transaction of e from the calls to the Safe's
// execTransaction in a trace of its onchain transaction.
func (d *decoder) fromTrace(ctx context.Context, e Execution, nonce *big.Int) *safenet.SafeTransaction {
	inputs, ok := d.traces[e.TxHash]
	if !ok {
		var root callFrame
		// Most public nodes don't serve traces, so a JSON-RPC error, such as for a
		// method that the node doesn't serve, or a trace that timed out, is not a
		// warning, and the next source may have the transaction. Some nodes refuse the
		// method with an HTTP error status rather than a JSON-RPC error, such as a 401
		// or 406 page or a 503, which is only a warning for that reason. A rate limit
		// or a dropped connection is a failure, though, since the node may serve
		// traces.
		err := d.Eth.RawRequest(ctx, &root, "debug_traceTransaction", e.TxHash, map[string]any{"tracer": "callTracer"})
		if httpErr, ok := errors.AsType[*ethrpc.HTTPError](err); ok && httpErr.StatusCode != http.StatusTooManyRequests && ethrpc.Transient(err) {
			d.warn(fmt.Errorf("tracing transaction %s: %w", e.TxHash, err))
		} else if ethrpc.Transient(err) && !ethrpc.TimedOut(err) {
			d.fail(fmt.Errorf("tracing transaction %s: %w", e.TxHash, err))
			return nil
		}
		if err == nil {
			inputs = root.callsTo(d.safe, nil)
		}
		if d.traces == nil {
			d.traces = make(map[ethrpc.Hash][]ethrpc.Bytes)
		}
		d.traces[e.TxHash] = inputs
	}
	for _, input := range inputs {
		if tx := d.candidate(input, e, nonce); tx != nil {
			return tx
		}
	}
	return nil
}

// candidate returns the Safe transaction that the calldata of a call to
// execTransaction has, given that it used nonce, if it has the hash that e has.
func (d *decoder) candidate(input []byte, e Execution, nonce *big.Int) *safenet.SafeTransaction {
	tx := decodeCall(d.safe, d.Eth.ChainID(), input)
	if tx == nil {
		return nil
	}
	tx.Nonce = nonce
	if !matches(tx, e.SafeTxHash) {
		return nil
	}
	return tx
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

// callFrame is a call in the output of geth's callTracer.
type callFrame struct {
	Type  string          `json:"type"`
	To    *ethrpc.Address `json:"to"`
	Input ethrpc.Bytes    `json:"input"`
	Error string          `json:"error"`
	Calls []callFrame     `json:"calls"`
}

// callsTo appends to inputs the calldata of the calls to safe in the frame and
// the calls it makes, in the order that they start. A call that reverted, and
// the calls in it, are left out, since their effects were undone.
func (f *callFrame) callsTo(safe ethrpc.Address, inputs []ethrpc.Bytes) []ethrpc.Bytes {
	if f.Error != "" {
		return inputs
	}
	if f.Type == "CALL" && f.To != nil && *f.To == safe {
		inputs = append(inputs, f.Input)
	}
	for i := range f.Calls {
		inputs = f.Calls[i].callsTo(safe, inputs)
	}
	return inputs
}
