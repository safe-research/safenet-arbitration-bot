package checks

import (
	"math/big"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

func TestCancellation(t *testing.T) {
	safe := &safeID{address: ethrpc.Address{0: 0x5a, 19: 1}, chainID: big.NewInt(ethrpc.Mainnet)}
	for _, check := range []struct {
		check check
		to    ethrpc.Address
	}{{emptySelfCallCheck, safe.address}, {emptyZeroAddressCallCheck, ethrpc.Address{}}} {
		cancel := call{to: check.to, value: new(big.Int), data: ethrpc.Bytes{}, operation: safenet.OperationCall}
		with := func(change func(c *call)) call {
			c := cancel
			change(&c)
			return c
		}

		tests := []struct {
			name string
			c    call
			want bool
		}{
			{"cancellation", cancel, true},
			{"nil data", with(func(c *call) { c.data = nil }), true},
			{"batched", with(func(c *call) { c.kind = Batched }), true},
			{"with value", with(func(c *call) { c.value = big.NewInt(1) }), false},
			{"refund", with(func(c *call) { c.kind = Refund }), false},
			{"with data", with(func(c *call) { c.data = ethrpc.Bytes{0} }), false},
			{"with a selector", with(func(c *call) { c.data = solabi.Call(solabi.Selector("nonce()")) }), false},
			{"delegate call", with(func(c *call) { c.operation = safenet.OperationDelegateCall }), false},
			{"to another account", with(func(c *call) { c.to = ethrpc.Address{19: 0x70} }), false},
		}
		for _, test := range tests {
			if got, err := check.check.fn(t.Context(), nil, safe, test.c); got != test.want || err != nil {
				t.Errorf("%s: check %q = %v, %v; want %v", test.name, check.check.classification(), got, err, test.want)
			}
		}
	}
}

func TestClassifyCancellation(t *testing.T) {
	safeVersions.Clear()
	safe := ethrpc.Address{0: 0x5a, 19: 4}
	node := &fakeNode{accounts: map[ethrpc.Address]account{safe: proxy(safeProxy130, singletonOf("1.3.0"))}}
	dial := node.dialer(t, testSafeBlock)

	multiSend := ethrpc.MustParseAddress("0x38869bf66a61cF6bDB996A6aE40D5853Fd43B526")
	batch := func(to ...ethrpc.Address) safenet.SafeTransaction {
		var calls []call
		for _, to := range to {
			calls = append(calls, call{to: to, value: new(big.Int), data: ethrpc.Bytes{}, operation: safenet.OperationCall})
		}
		return safenet.SafeTransaction{
			Safe:      safe,
			To:        multiSend,
			Data:      solabi.Call(multiSendSelector, pack(calls...)),
			Operation: safenet.OperationDelegateCall,
		}
	}
	tests := []struct {
		name string
		tx   safenet.SafeTransaction
		want Classification
	}{
		{"cancellation", safenet.SafeTransaction{Safe: safe, To: safe}, emptySelfCallCheck.classification()},
		{"cancellation to the zero address", safenet.SafeTransaction{Safe: safe}, emptyZeroAddressCallCheck.classification()},
		{"batched cancellations", batch(safe, safe), emptySelfCallCheck.classification()},
		{
			"batched cancellations to the Safe and the zero address",
			batch(safe, ethrpc.Address{}),
			Classification{
				Verdict:     Secure,
				Description: emptySelfCallCheck.description + "; " + emptyZeroAddressCallCheck.description,
			},
		},
		{
			"cancellation with a refund",
			safenet.SafeTransaction{Safe: safe, To: safe, GasPrice: big.NewInt(1), RefundReceiver: ethrpc.Address{19: 0xee}},
			Classification{},
		},
		{"with value", safenet.SafeTransaction{Safe: safe, To: safe, Value: big.NewInt(1)}, Classification{}},
		{"to the zero address with value", safenet.SafeTransaction{Safe: safe, Value: big.NewInt(1)}, Classification{}},
	}
	for _, test := range tests {
		if got, err := Classify(t.Context(), dial, request(test.tx)); got != test.want || err != nil {
			t.Errorf("%s: Classify() = %+v, %v; want %+v", test.name, got, err, test.want)
		}
	}
}
