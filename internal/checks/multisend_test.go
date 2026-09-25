package checks

import (
	"bytes"
	"math/big"
	"slices"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// equalCalls reports whether a and b are the same calls.
func equalCalls(a, b []call) bool {
	return slices.EqualFunc(a, b, call.equal)
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
	// withDataLength sets the data length of the first of packed transactions.
	withDataLength := func(packed []byte, length *big.Int) []byte {
		word := solabi.Uint(length)
		copy(packed[53:85], word[:])
		return packed
	}
	// truncated is a transaction that ends partway through its value, which
	// MultiSend reads as if it were followed by zeros.
	truncated := append(append([]byte{0}, bytes.Repeat([]byte{0x11}, 20)...), bytes.Repeat([]byte{0x22}, 19)...)
	truncatedValue := new(big.Int).SetBytes(append(bytes.Repeat([]byte{0x22}, 19), make([]byte, 13)...))

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
			"only ignored trailing bytes",
			multiSendTo(multiSend141, safenet.OperationDelegateCall, make([]byte, 32)),
			[]call{emptyMultiSend(multiSend141)},
			false,
		},
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
		{
			"undecodable arguments",
			withData(delegate(multiSend141), multiSendSelector[:]),
			[]call{withData(delegate(multiSend141), multiSendSelector[:])},
			false,
		},
		{"delegate call from MultiSendCallOnly", delegate(callOnly141, transfer, library), nil, true},
		{"unknown operation", delegate(multiSend141, transfer, with(transfer, 2)), nil, true},
		{"call to MultiSend", delegate(multiSend141, transfer, with(delegate(multiSend150), safenet.OperationCall)), nil, true},
		{"nested revert", delegate(multiSend141, transfer, delegate(callOnly141, library)), nil, true},
		{"zero address since 1.5.0", delegate(multiSend150, selfCall), batched(toSafe), false},
		{"zero address from MultiSendCallOnly since 1.5.0", delegate(callOnly150, selfCall), batched(toSafe), false},
		{"zero address before 1.5.0", delegate(multiSend141, selfCall), batched(selfCall), false},
		{
			"ignored trailing bytes",
			multiSendTo(multiSend141, safenet.OperationDelegateCall, append(pack(transfer), make([]byte, 32)...)),
			batched(transfer),
			false,
		},
		{
			"truncated transaction",
			multiSendTo(multiSend141, safenet.OperationDelegateCall, append(pack(transfer), truncated...)),
			batched(transfer, call{to: ethrpc.Address(truncated[1:21]), value: truncatedValue, data: ethrpc.Bytes{}, operation: safenet.OperationCall}),
			false,
		},
		{
			"truncated data",
			multiSendTo(multiSend141, safenet.OperationDelegateCall, pack(library)[:86]),
			batched(withData(library, ethrpc.Bytes{0x12, 0})),
			false,
		},
		{
			"largest data length",
			multiSendTo(multiSend141, safenet.OperationDelegateCall, withDataLength(pack(transfer), big.NewInt(maxDataLength))),
			batched(withData(transfer, make(ethrpc.Bytes, maxDataLength))),
			false,
		},
		{
			"data length too large",
			multiSendTo(multiSend141, safenet.OperationDelegateCall, withDataLength(pack(transfer), big.NewInt(maxDataLength+1))),
			nil,
			true,
		},
		{
			"data length over 64 bits",
			multiSendTo(multiSend141, safenet.OperationDelegateCall, withDataLength(pack(transfer), maxUint256)),
			nil,
			true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &safenet.SafeTransaction{Safe: safe, To: test.c.to, Value: test.c.value, Data: test.c.data, Operation: test.c.operation}
			want := test.calls
			if test.reverts {
				want = []call{revertingMultiSend(test.c.to)}
			}
			if calls := expandTransactionCalls(tx); !equalCalls(calls, want) {
				t.Errorf("expandTransactionCalls() = %+v, want %+v", calls, want)
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
	for _, test := range []struct {
		name string
		tx   safenet.SafeTransaction
		want Classification
	}{
		{"empty", empty, emptyMultiSendCheck.classification()},
		{"reverting", reverting, invalidMultiSendCheck.classification()},
		{"reverting with a refund", refunded, Classification{}},
	} {
		if got, err := Classify(t.Context(), dial, request(test.tx)); got != test.want || err != nil {
			t.Errorf("%s: Classify() = %+v, %v; want %+v", test.name, got, err, test.want)
		}
	}
}
