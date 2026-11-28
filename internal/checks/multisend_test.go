package checks

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/multisend"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// equalCalls reports whether a and b are the same calls.
func equalCalls(a, b []call) bool {
	return slices.EqualFunc(a, b, call.equal)
}

// identifiedExcept returns an identifier that identifies the contracts of
// multiSends at every address but those of unidentified.
func identifiedExcept(unidentified ...ethrpc.Address) identifier {
	return func(_ context.Context, address ethrpc.Address) (bool, error) {
		return !slices.Contains(unidentified, address), nil
	}
}

// pack packs calls as MultiSend's transactions.
func pack(calls ...call) []byte {
	var packed []byte
	for _, c := range calls {
		packed = append(packed, byte(c.operation))
		packed = append(packed, c.to[:]...)
		value := solabi.Uint(c.value)
		length := solabi.Uint64(uint64(len(c.data)))
		packed = append(packed, value[:]...)
		packed = append(packed, length[:]...)
		packed = append(packed, c.data...)
	}
	return packed
}

func TestExpandTransactionCalls(t *testing.T) {
	safe := ethrpc.Address{19: 0x5a}
	multiSend130 := ethrpc.MustParseAddress("0xA238CBeb142c10Ef7Ad8442C6D1f9E89e07e7761")
	multiSend141 := ethrpc.MustParseAddress("0x38869bf66a61cF6bDB996A6aE40D5853Fd43B526")
	multiSend150 := ethrpc.MustParseAddress("0x218543288004CD07832472D464648173c77D7eB7")
	callOnly141 := ethrpc.MustParseAddress("0x9641d764fc13c8B624c04430C7356C1C7C8102e2")
	callOnly150 := ethrpc.MustParseAddress("0xA83c336B20401Af773B6219BA5027174338D1836")
	// withoutCode is a MultiSendCallOnly address that has no code.
	withoutCode := ethrpc.MustParseAddress("0x40A2aCCbd92BCA938b02010E17A5b8929b49130D")

	transfer := call{to: ethrpc.Address{19: 0x70}, value: big.NewInt(1), data: ethrpc.Bytes{}, operation: safenet.OperationCall}
	library := call{to: ethrpc.Address{19: 0x71}, value: new(big.Int), data: ethrpc.Bytes{0x12, 0x34}, operation: safenet.OperationDelegateCall}
	selfCall := call{value: new(big.Int), data: ethrpc.Bytes{0x56}, operation: safenet.OperationCall}
	toSafe := selfCall
	toSafe.to = safe
	with := func(c call, operation safenet.Operation) call {
		c.operation = operation
		return c
	}

	// multiSendTo returns a call to the multiSend of the contract at to, with the
	// packed transactions.
	multiSendTo := func(to ethrpc.Address, operation safenet.Operation, transactions []byte) call {
		return call{to: to, value: new(big.Int), data: solabi.Call(multiSendSelector, transactions), operation: operation}
	}
	delegate := func(to ethrpc.Address, calls ...call) call {
		return multiSendTo(to, safenet.OperationDelegateCall, pack(calls...))
	}
	// batched returns calls as the calls of a MultiSend.
	batched := func(calls ...call) []call {
		for i := range calls {
			calls[i].kind = Batched
		}
		return calls
	}
	withData := func(c call, data ethrpc.Bytes) call {
		c.data = data
		return c
	}
	withValue := func(c call) call {
		c.value = big.NewInt(1)
		return c
	}

	tests := []struct {
		name    string
		c       call
		calls   []call
		reverts bool
	}{
		{"not a MultiSend", transfer, []call{transfer}, false},
		{"MultiSend", delegate(multiSend141, transfer, library), batched(transfer, library), false},
		{"MultiSendCallOnly", delegate(callOnly141, transfer, transfer), batched(transfer, transfer), false},
		{"no transactions", delegate(multiSend141), []call{emptyMultiSend(multiSend141)}, false},
		{"empty", emptyMultiSend(multiSend150), []call{emptyMultiSend(multiSend150)}, false},
		{"nested empty", delegate(multiSend141, delegate(multiSend150)), []call{emptyMultiSend(multiSend141)}, false},
		{
			"nested",
			delegate(multiSend130, library, delegate(multiSend150, transfer, delegate(callOnly150, transfer))),
			batched(library, transfer, transfer),
			false,
		},
		{"MultiSend called", with(delegate(multiSend141, transfer), safenet.OperationCall), nil, true},
		{
			"MultiSendCallOnly called",
			with(delegate(callOnly141, transfer), safenet.OperationCall),
			[]call{with(delegate(callOnly141, transfer), safenet.OperationCall)},
			false,
		},
		{"placeholder", revertingMultiSend(multiSend150), nil, true},
		{"no selector", withData(delegate(multiSend141), ethrpc.Bytes{}), nil, true},
		{"other selector", withData(delegate(multiSend141), ethrpc.Bytes{1, 2, 3, 4}), nil, true},
		{"MultiSendCallOnly called with a delegate call", with(delegate(callOnly141, transfer, library), safenet.OperationCall), nil, true},
		{"MultiSendCallOnly called with a batch that reverts", with(delegate(callOnly141, with(transfer, 2)), safenet.OperationCall), nil, true},
		{
			"MultiSendCallOnly called with no transactions",
			withValue(with(delegate(callOnly141), safenet.OperationCall)),
			[]call{withValue(with(delegate(callOnly141), safenet.OperationCall))},
			false,
		},
		{
			"MultiSendCallOnly without code called with a delegate call",
			withValue(with(delegate(withoutCode, library), safenet.OperationCall)),
			[]call{withValue(with(delegate(withoutCode, library), safenet.OperationCall))},
			false,
		},
		{
			"MultiSendCallOnly without code that a batch calls",
			delegate(multiSend141, transfer, with(delegate(withoutCode, library), safenet.OperationCall)),
			batched(transfer, with(delegate(withoutCode, library), safenet.OperationCall)),
			false,
		},
		{"delegate call from MultiSendCallOnly", delegate(callOnly141, transfer, library), nil, true},
		{
			"delegate call from a MultiSendCallOnly that a batch calls",
			delegate(multiSend141, transfer, with(delegate(callOnly150, library), safenet.OperationCall)),
			nil,
			true,
		},
		{"unknown operation", delegate(multiSend141, transfer, with(transfer, 2)), nil, true},
		{"call to MultiSend", delegate(multiSend141, transfer, with(delegate(multiSend150), safenet.OperationCall)), nil, true},
		{"nested revert", delegate(multiSend141, transfer, delegate(callOnly141, library)), nil, true},
		{"zero address since 1.5.0", delegate(multiSend150, selfCall), batched(toSafe), false},
		{"zero address from MultiSendCallOnly since 1.5.0", delegate(callOnly150, selfCall), batched(toSafe), false},
		{"zero address before 1.5.0", delegate(multiSend141, selfCall), batched(selfCall), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &safenet.SafeTransaction{Safe: safe, To: test.c.to, Value: test.c.value, Data: test.c.data, Operation: test.c.operation}
			want := test.calls
			if test.reverts {
				want = []call{revertingMultiSend(test.c.to)}
			}
			calls, unexpanded, err := expandTransactionCalls(t.Context(), tx, identifiedExcept(withoutCode))
			if !equalCalls(calls, want) || unexpanded || err != nil {
				t.Errorf("expandTransactionCalls() = %+v, %t, %v; want %+v, false", calls, unexpanded, err, want)
			}
		})
	}

	// A failed read of whether the contract is there fails the expansion.
	errRead := errors.New("read failed")
	failing := func(context.Context, ethrpc.Address) (bool, error) { return false, errRead }
	c := delegate(multiSend141, transfer, with(delegate(callOnly141, library), safenet.OperationCall))
	tx := &safenet.SafeTransaction{Safe: safe, To: c.to, Value: c.value, Data: c.data, Operation: c.operation}
	if _, _, err := expandTransactionCalls(t.Context(), tx, failing); !errors.Is(err, errRead) {
		t.Errorf("expandTransactionCalls() with a failed read = _, _, %v; want %v", err, errRead)
	}
}

// TestExpandTransactionCallsUnexpanded checks that a transaction with a batch
// that expandTransactionCalls can't expand is left as its own call, unless it
// reverts anyway, and that no secure check matches it.
func TestExpandTransactionCallsUnexpanded(t *testing.T) {
	safe := ethrpc.Address{19: 0x5a}
	multiSend141 := ethrpc.MustParseAddress("0x38869bf66a61cF6bDB996A6aE40D5853Fd43B526")
	callOnly141 := ethrpc.MustParseAddress("0x9641d764fc13c8B624c04430C7356C1C7C8102e2")

	library := call{to: ethrpc.Address{19: 0x71}, value: new(big.Int), data: ethrpc.Bytes{0x12, 0x34}, operation: safenet.OperationDelegateCall}
	addOwner := call{
		to:        safe,
		value:     new(big.Int),
		data:      solabi.Call(solabi.Selector("addOwnerWithThreshold(address,uint256)"), ethrpc.Address{19: 0x0e}, uint64(1)),
		operation: safenet.OperationCall,
	}
	delegate := func(calls ...call) call {
		return call{to: multiSend141, value: new(big.Int), data: solabi.Call(multiSendSelector, pack(calls...)), operation: safenet.OperationDelegateCall}
	}
	// nest returns c in levels of batches.
	nest := func(levels int, c call) call {
		for range levels {
			c = delegate(c)
		}
		return c
	}
	// cutShort returns a batch of c, whose data runs zeros bytes past the end of
	// the batch, where the contracts read zeros.
	cutShort := func(c call, zeros int) call {
		d := delegate(c)
		packed := pack(c)
		length := solabi.Uint64(uint64(len(c.data) + zeros))
		copy(packed[53:85], length[:])
		d.data = solabi.Call(multiSendSelector, packed)
		return d
	}
	// padded returns c, batched, with zeros bytes after its data.
	padded := func(c call, zeros int) call {
		c.data = append(append(ethrpc.Bytes{}, c.data...), make([]byte, zeros)...)
		c.kind = Batched
		return c
	}
	undecodable := call{to: multiSend141, value: new(big.Int), data: multiSendSelector[:], operation: safenet.OperationDelegateCall}
	reverting := delegate(call{to: multiSend141, value: new(big.Int), data: ethrpc.Bytes{}, operation: safenet.OperationCall})
	half := multisend.MaxPadding / 2

	tests := []struct {
		name string
		c    call
		// calls are the calls of an expanded transaction, and nil for one that is left
		// as its own call.
		calls []call
	}{
		{"8 levels", nest(8, library), []call{padded(library, 0)}},
		{"9 levels", nest(9, library), nil},
		{"owner change 9 levels deep", nest(9, addOwner), nil},
		{"zeros up to the bound", cutShort(library, multisend.MaxPadding), []call{padded(library, multisend.MaxPadding)}},
		{"zeros over the bound", cutShort(library, multisend.MaxPadding+1), nil},
		{
			"zeros of two batches up to the bound",
			delegate(cutShort(library, half), cutShort(library, half)),
			[]call{padded(library, half), padded(library, half)},
		},
		{"zeros of two batches over the bound", delegate(cutShort(library, half), cutShort(library, half+1)), nil},
		{"16 MiB of zeros", cutShort(nest(1, library), multisend.MaxDataLength-len(nest(1, library).data)), nil},
		{"undecodable arguments", undecodable, nil},
		{"undecodable arguments next to an owner change", delegate(addOwner, undecodable), nil},
		{"undecodable arguments of a call to MultiSendCallOnly", call{
			to:        callOnly141,
			value:     new(big.Int),
			data:      multiSendSelector[:],
			operation: safenet.OperationCall,
		}, nil},
		{"revert before a batch too deep", delegate(reverting, nest(9, library)), []call{revertingMultiSend(multiSend141)}},
		{"revert after a batch too deep", delegate(nest(9, library), reverting), []call{revertingMultiSend(multiSend141)}},
		{
			"revert after too many zeros",
			delegate(cutShort(library, multisend.MaxPadding+1), reverting),
			[]call{revertingMultiSend(multiSend141)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &safenet.SafeTransaction{Safe: safe, To: test.c.to, Value: test.c.value, Data: test.c.data, Operation: test.c.operation}
			calls, unexpanded, err := expandTransactionCalls(t.Context(), tx, identifiedExcept())
			if err != nil {
				t.Fatal(err)
			}
			if test.calls != nil {
				if !equalCalls(calls, test.calls) || unexpanded {
					t.Errorf("expandTransactionCalls() = %d calls, %t; want %d calls, false", len(calls), unexpanded, len(test.calls))
				}
				return
			}
			if !equalCalls(calls, []call{test.c}) || !unexpanded {
				t.Fatalf("expandTransactionCalls() = %d calls, %t; want the transaction's own call, true", len(calls), unexpanded)
			}
			for _, c := range checks {
				if c.verdict != Secure {
					continue
				}
				if match, err := c.fn(t.Context(), nil, &safeID{address: safe, chainID: big.NewInt(ethrpc.Mainnet)}, calls[0]); match || err != nil {
					t.Errorf("secure check %q = %t, %v; want false", c.classification(), match, err)
				}
			}
		})
	}
}

func TestMultiSendChecks(t *testing.T) {
	multiSend := ethrpc.MustParseAddress("0x38869bf66a61cF6bDB996A6aE40D5853Fd43B526")
	other := ethrpc.Address{19: 0x70}
	withValue := func(c call) call {
		c.value = big.NewInt(1)
		return c
	}
	tests := []struct {
		name            string
		c               call
		empty, reverted bool
	}{
		{"empty", emptyMultiSend(multiSend), true, false},
		{"reverting", revertingMultiSend(multiSend), false, true},
		{"empty with value", withValue(emptyMultiSend(multiSend)), false, false},
		{"reverting with value", withValue(revertingMultiSend(multiSend)), false, false},
		{"empty to another contract", emptyMultiSend(other), false, false},
		{"reverting to another contract", revertingMultiSend(other), false, false},
		{"MultiSend with transactions", call{
			to:        multiSend,
			value:     new(big.Int),
			data:      solabi.Call(multiSendSelector, make([]byte, 85)),
			operation: safenet.OperationDelegateCall,
		}, false, false},
	}
	for _, test := range tests {
		for _, check := range []struct {
			c    check
			want bool
		}{{emptyMultiSendCheck, test.empty}, {invalidMultiSendCheck, test.reverted}} {
			if got, err := check.c.fn(t.Context(), nil, &safeID{}, test.c); got != check.want || err != nil {
				t.Errorf("%s: check %q = %t, %v; want %t", test.name, check.c.classification(), got, err, check.want)
			}
		}
	}

	// The transactions are by a Safe of version 1.5.0 at the zero address.
	safeVersions.Clear()
	node := &fakeNode{accounts: map[ethrpc.Address]account{{}: proxy(safeProxy150, singletonOf("1.5.0"))}}
	dial := node.dialer(t, testSafeBlock)
	multiSend150 := ethrpc.MustParseAddress("0x218543288004CD07832472D464648173c77D7eB7")
	empty := safenet.SafeTransaction{
		To:        multiSend150,
		Data:      solabi.Call(multiSendSelector, []byte{}),
		Operation: safenet.OperationDelegateCall,
	}
	reverting := safenet.SafeTransaction{To: multiSend150, Data: solabi.Call(multiSendSelector, []byte{})}
	refunded := reverting
	refunded.GasPrice = big.NewInt(1)
	// nested returns a transaction that adds an owner to the Safe in levels of
	// batches.
	nested := func(levels int) safenet.SafeTransaction {
		addOwner := solabi.Call(solabi.Selector("addOwnerWithThreshold(address,uint256)"), ethrpc.Address{19: 0x0e}, uint64(1))
		c := call{value: new(big.Int), data: addOwner, operation: safenet.OperationCall}
		for range levels {
			c = call{to: multiSend150, value: new(big.Int), data: solabi.Call(multiSendSelector, pack(c)), operation: safenet.OperationDelegateCall}
		}
		return safenet.SafeTransaction{To: c.to, Data: c.data, Operation: c.operation}
	}
	for _, test := range []struct {
		name string
		tx   safenet.SafeTransaction
		want Classification
	}{
		{"empty", empty, emptyMultiSendCheck.classification()},
		{"reverting", reverting, invalidMultiSendCheck.classification()},
		{"reverting with a refund", refunded, Classification{}},
		{"owner change 8 levels deep", nested(8), addOwnerCheck.classification()},
		{"owner change 9 levels deep", nested(9), Classification{}},
	} {
		if got, err := Classify(t.Context(), dial, request(test.tx)); got != test.want || err != nil {
			t.Errorf("%s: Classify() = %+v, %v; want %+v", test.name, got, err, test.want)
		}
	}
}

// TestMultiSendCallOnlyWithoutCode checks that a call to the address of a
// MultiSendCallOnly is only replaced by a placeholder that always reverts if
// the contract is there, as Calls identifies it: a call to an address without
// code succeeds, and moves its value.
func TestMultiSendCallOnlyWithoutCode(t *testing.T) {
	callOnly141 := ethrpc.MustParseAddress("0x9641d764fc13c8B624c04430C7356C1C7C8102e2")
	// MultiSendCallOnly reverts on the delegate call in the batch.
	library := call{to: ethrpc.Address{19: 0x71}, value: new(big.Int), operation: safenet.OperationDelegateCall}
	tx := safenet.SafeTransaction{To: callOnly141, Value: big.NewInt(1), Data: solabi.Call(multiSendSelector, pack(library))}
	for _, test := range []struct {
		name        string
		chainID     int64
		code        ethrpc.Bytes
		want        Classification
		placeholder string
		value       int64
	}{
		{"with code", ethrpc.Mainnet, ethrpc.Bytes{0x60, 0x80}, invalidMultiSendCheck.classification(), "reverting", 0},
		{"without code", ethrpc.Mainnet, nil, Classification{}, "", 1},
		{"network that the Charter doesn't cover", 10, ethrpc.Bytes{0x60, 0x80}, offNetwork.classification(), "", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			safeVersions.Clear()
			node := &fakeNode{accounts: map[ethrpc.Address]account{
				{}:          proxy(safeProxy100, singletonOf("1.4.1")),
				callOnly141: {code: test.code},
			}}
			dial := node.dialer(t, testSafeBlock)
			tx.ChainID = big.NewInt(test.chainID)
			r := request(tx)
			if got, err := Classify(t.Context(), dial, r); got != test.want || err != nil {
				t.Errorf("Classify() = %+v, %v; want %+v", got, err, test.want)
			}
			// offNetwork runs first, so the checks read nothing of a network that the
			// Charter doesn't cover.
			if n := node.reads.Load(); test.chainID != ethrpc.Mainnet && n != 0 {
				t.Errorf("Classify() read the chain %d times, want 0", n)
			}
			calls, err := Calls(t.Context(), dial, r)
			if err != nil {
				t.Fatal(err)
			}
			if len(calls.Calls) != 1 || calls.Calls[0].Placeholder != test.placeholder ||
				calls.Calls[0].Value.Cmp(big.NewInt(test.value)) != 0 {
				t.Errorf("Calls() = %+v; want one call with placeholder %q and value %d", calls.Calls, test.placeholder, test.value)
			}
		})
	}
}
