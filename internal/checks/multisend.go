package checks

import (
	"context"
	"math/big"

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

// expandTransactionCalls returns the calls that a Safe makes when it makes the
// call of tx, with delegate calls to MultiSend and MultiSendCallOnly replaced
// by the calls that they make, recursively. If the call reverts whatever the
// state it runs in, it returns revertingMultiSend of the call's target in place
// of them, and if it makes no calls, it returns emptyMultiSend of the call's
// target, so that there is always at least one call.
//
// Calls that it can't decode are left as they are. It decodes the calls that a
// MultiSend makes as the contract does, with multisend.Decode, following the
// version of the contract:
//
//   - The contract has no fallback, so any call to it other than one to
//     multiSend reverts.
//   - MultiSend reverts unless it is delegate called. MultiSendCallOnly makes
//     its calls itself when it is called, rather than the Safe, so they are
//     left as a single call to it.
//   - MultiSendCallOnly reverts on a transaction that is a delegate call.
//   - Since 1.5.0, the zero address means the Safe itself.
//   - It reverts if any of its calls fails, so a call that reverts makes it
//     revert too.
func expandTransactionCalls(tx *safenet.SafeTransaction) []call {
	calls, reverts := expandMultiSendCall(tx.Safe, call{to: tx.To, value: tx.Value, data: tx.Data, operation: tx.Operation})
	if reverts {
		return []call{revertingMultiSend(tx.To)}
	}
	if len(calls) == 0 {
		return []call{emptyMultiSend(tx.To)}
	}
	return calls
}

// expandMultiSendCall returns the calls that the Safe at safe makes when it
// makes c, as expandTransactionCalls does, or true if c reverts.
func expandMultiSendCall(safe ethrpc.Address, c call) ([]call, bool) {
	ms, ok := multiSends[c.to]
	if !ok {
		return []call{c}, false
	}
	if len(c.data) < 4 || [4]byte(c.data[:4]) != multiSendSelector {
		return nil, true
	}
	if c.operation == safenet.OperationCall && !ms.callOnly {
		return nil, true
	}
	if c.operation != safenet.OperationDelegateCall {
		return []call{c}, false
	}

	d := solabi.NewDecoder(c.data[4:])
	batch := d.Bytes(0)
	if d.Err() != nil {
		return []call{c}, false
	}
	transactions, ok := multisend.Decode(batch)
	if !ok {
		return nil, true
	}
	var calls []call
	for _, tx := range transactions {
		if ms.callOnly && tx.Operation != safenet.OperationCall {
			return nil, true
		}
		to := tx.To
		if to == (ethrpc.Address{}) && ms.version == "1.5.0" {
			to = safe
		}
		sub := call{to: to, value: tx.Value, data: tx.FullData(), operation: tx.Operation, kind: Batched}
		subCalls, reverts := expandMultiSendCall(safe, sub)
		if reverts {
			return nil, true
		}
		calls = append(calls, subCalls...)
	}
	return calls, false
}
