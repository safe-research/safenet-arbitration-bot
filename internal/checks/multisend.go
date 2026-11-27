package checks

import (
	"context"
	"math/big"
	"slices"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/multisend"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// multiSend is a deployment of the MultiSend or MultiSendCallOnly contract of a
// Safe version that § 2.1 of the Charter lists.
type multiSend struct {
	version  string
	callOnly bool
}

// multiSends are the MultiSend and MultiSendCallOnly deployments of the Safe
// versions that § 2.1 lists, as listed in the safe-deployments repository at
// https://github.com/safe-global/safe-deployments, which lists each of them on
// every network that the Charter covers. They are deterministic deployments, so
// an address has either the contract's code or none at all.
var multiSends = map[ethrpc.Address]multiSend{
	ethrpc.MustParseAddress("0xA238CBeb142c10Ef7Ad8442C6D1f9E89e07e7761"): {version: "1.3.0"},
	ethrpc.MustParseAddress("0x998739BFdAAdde7C933B942a68053933098f9EDa"): {version: "1.3.0"},
	ethrpc.MustParseAddress("0x40A2aCCbd92BCA938b02010E17A5b8929b49130D"): {version: "1.3.0", callOnly: true},
	ethrpc.MustParseAddress("0xA1dabEF33b3B82c7814B6D82A79e50F4AC44102B"): {version: "1.3.0", callOnly: true},
	ethrpc.MustParseAddress("0x38869bf66a61cF6bDB996A6aE40D5853Fd43B526"): {version: "1.4.1"},
	ethrpc.MustParseAddress("0x9641d764fc13c8B624c04430C7356C1C7C8102e2"): {version: "1.4.1", callOnly: true},
	ethrpc.MustParseAddress("0x218543288004CD07832472D464648173c77D7eB7"): {version: "1.5.0"},
	ethrpc.MustParseAddress("0xA83c336B20401Af773B6219BA5027174338D1836"): {version: "1.5.0", callOnly: true},
}

// MultiSendCallOnly reports whether address is that of a MultiSendCallOnly of
// multiSends, which makes no delegate calls, so a delegate call to it can only
// change the Safe through calls to it.
func MultiSendCallOnly(address ethrpc.Address) bool {
	return multiSends[address].callOnly
}

// multiSendSelector is the selector of multiSend(bytes), the only function of
// MultiSend and MultiSendCallOnly.
var multiSendSelector = solabi.Selector("multiSend(bytes)")

// revertingMultiSend returns a placeholder for a call to the MultiSend or
// MultiSendCallOnly at to that reverts whatever the state it runs in: a call to
// it with no data, which reverts since the contract has no fallback.
func revertingMultiSend(to ethrpc.Address) call {
	return call{to: to, value: new(big.Int), data: ethrpc.Bytes{}, operation: safenet.OperationCall}
}

// emptyMultiSend returns a placeholder for a call to the MultiSend or
// MultiSendCallOnly at to that makes no calls: a delegate call to its multiSend
// with no transactions.
func emptyMultiSend(to ethrpc.Address) call {
	return call{
		to:        to,
		value:     new(big.Int),
		data:      solabi.Call(multiSendSelector, []byte{}),
		operation: safenet.OperationDelegateCall,
	}
}

// emptyMultiSendCheck finds a MultiSend that makes no calls secure. It has none
// of the effects that the rules of Article IV consider, whatever else the
// transaction does.
var emptyMultiSendCheck = check{
	verdict:     Secure,
	description: "MultiSend that makes no calls",
	fn: func(_ context.Context, _ *env, _ *safeID, c call) (bool, error) {
		_, ok := multiSends[c.to]
		return ok && c.equal(emptyMultiSend(c.to)), nil
	},
}

// invalidMultiSendCheck finds a MultiSend call that reverts whatever the state
// it runs in secure. Nothing that it does remains, so it has none of the
// effects that the rules of Article IV consider, whatever else the transaction
// does. The Safe may still pay a refund, which is a call of its own.
var invalidMultiSendCheck = check{
	verdict:     Secure,
	description: "MultiSend call that always reverts",
	fn: func(_ context.Context, _ *env, _ *safeID, c call) (bool, error) {
		_, ok := multiSends[c.to]
		return ok && c.equal(revertingMultiSend(c.to)), nil
	},
}

// identifier reports whether the contract that multiSends has at address is
// identified there, as CallReader.Account identifies it: on a network that the
// Charter covers, with code at the SafeBlock.
type identifier func(ctx context.Context, address ethrpc.Address) (bool, error)

// identifier returns an identifier of the contracts of multiSends on the chain
// with chainID at env's block. It reads each address once.
func (e *env) identifier(chainID *big.Int) identifier {
	hasCode := map[ethrpc.Address]bool{}
	return func(ctx context.Context, address ethrpc.Address) (bool, error) {
		if !chainID.IsUint64() || !slices.Contains(networks, chainID.Uint64()) {
			return false, nil
		}
		if has, ok := hasCode[address]; ok {
			return has, nil
		}
		client, err := e.dial(ctx, chainID.Uint64())
		if err != nil {
			return false, err
		}
		code, err := client.GetCode(ctx, address, ethrpc.BlockNumber(e.block))
		if err != nil {
			return false, err
		}
		hasCode[address] = len(code) > 0
		return hasCode[address], nil
	}
}

// maxNesting is how many levels of MultiSend batches expandTransactionCalls
// expands. A transaction that Safe's tools build has one, while each level of a
// crafted one takes 153 bytes and copies the data of the levels below it.
const maxNesting = 8

// outcome is what expandMultiSendCall finds that a call does.
type outcome uint8

const (
	// expanded means that the call makes the calls returned.
	expanded outcome = iota
	// reverts means that the call reverts whatever the state it runs in.
	reverts
	// unexpanded means that the call has a batch that expandMultiSendCall can't
	// expand.
	unexpanded
)

// expandTransactionCalls returns the calls that a Safe makes when it makes the
// call of tx, with delegate calls to MultiSend and MultiSendCallOnly replaced
// by the calls that they make, recursively. If the call reverts whatever the
// state it runs in, it returns revertingMultiSend of the call's target in place
// of them, and if it makes no calls, it returns emptyMultiSend of the call's
// target, so that there is always at least one call. It returns an error if
// identified does.
//
// It returns the call of tx as it is, and true, if it can't expand one of its
// batches: if the batches nest more than maxNesting levels deep, if their
// transactions run past the end of their batches by more than
// multisend.MaxPadding zero bytes in all, or if the arguments of a call to
// multiSend don't decode. It doesn't return the calls of the other batches,
// since that batch can make the call revert, but a call that reverts anyway,
// such as on another transaction of the batch, still reverts.
//
// It decodes the calls that a MultiSend makes as the contract does, with
// multisend.Decode, following the version of the contract:
//
//   - The contract has no fallback, so any call to it other than one to
//     multiSend reverts.
//   - MultiSend reverts unless it is delegate called. MultiSendCallOnly makes
//     its calls itself when it is called, rather than the Safe, so they are
//     left as a single call to it, unless the call reverts. A call to its
//     address reverts only if identified finds the contract there, since a call
//     to an address without code succeeds.
//   - MultiSendCallOnly reverts on a transaction that is a delegate call.
//   - Since 1.5.0, the zero address means the Safe itself.
//   - It reverts if any of its calls fails, so a call that reverts makes it
//     revert too.
func expandTransactionCalls(ctx context.Context, tx *safenet.SafeTransaction, identified identifier) ([]call, bool, error) {
	own := call{to: tx.To, value: tx.Value, data: tx.Data, operation: tx.Operation}
	padding := multisend.MaxPadding
	calls, result, err := expandMultiSendCall(ctx, tx.Safe, own, 0, &padding, identified)
	switch {
	case err != nil:
		return nil, false, err
	case result == reverts:
		return []call{revertingMultiSend(tx.To)}, false, nil
	case result == unexpanded:
		return []call{own}, true, nil
	case len(calls) == 0:
		return []call{emptyMultiSend(tx.To)}, false, nil
	}
	return calls, false, nil
}

// expandMultiSendCall returns the calls that the Safe at safe makes when it
// makes c, at nesting level depth, as expandTransactionCalls does, with padding
// the zero bytes that it can still add to batch transactions.
func expandMultiSendCall(
	ctx context.Context, safe ethrpc.Address, c call, depth int, padding *int, identified identifier,
) ([]call, outcome, error) {
	ms, ok := multiSends[c.to]
	if !ok {
		return []call{c}, expanded, nil
	}
	if len(c.data) < 4 || [4]byte(c.data[:4]) != multiSendSelector {
		return nil, reverts, nil
	}
	if c.operation == safenet.OperationCall && !ms.callOnly {
		return nil, reverts, nil
	}
	if c.operation != safenet.OperationCall && c.operation != safenet.OperationDelegateCall {
		return []call{c}, expanded, nil
	}
	if c.operation == safenet.OperationCall {
		there, err := identified(ctx, c.to)
		if err != nil {
			return nil, expanded, err
		}
		if !there {
			return []call{c}, expanded, nil
		}
	}
	if depth >= maxNesting {
		return nil, unexpanded, nil
	}

	d := solabi.NewDecoder(c.data[4:])
	batch := d.Bytes(0)
	if d.Err() != nil {
		return nil, unexpanded, nil
	}
	transactions, ok := multisend.Decode(batch)
	if !ok {
		return nil, reverts, nil
	}
	if ms.callOnly && slices.ContainsFunc(transactions, func(tx multisend.Transaction) bool {
		return tx.Operation != safenet.OperationCall
	}) {
		return nil, reverts, nil
	}
	if c.operation == safenet.OperationCall {
		return []call{c}, expanded, nil
	}
	var calls []call
	result := expanded
	for _, tx := range transactions {
		// A transaction with zeros runs past the end of the batch, so it is the last.
		data, ok := tx.PaddedData(padding)
		if !ok {
			return nil, unexpanded, nil
		}
		to := tx.To
		if to == (ethrpc.Address{}) && ms.version == "1.5.0" {
			to = safe
		}
		sub := call{to: to, value: tx.Value, data: data, operation: tx.Operation, kind: Batched}
		subCalls, subResult, err := expandMultiSendCall(ctx, safe, sub, depth+1, padding, identified)
		if err != nil {
			return nil, expanded, err
		}
		switch subResult {
		case reverts:
			return nil, reverts, nil
		case unexpanded:
			// A later transaction can still make the batch revert.
			result = unexpanded
		}
		calls = append(calls, subCalls...)
	}
	if result == unexpanded {
		return nil, unexpanded, nil
	}
	return calls, expanded, nil
}
