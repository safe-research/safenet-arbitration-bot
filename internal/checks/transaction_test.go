package checks

import (
	"math/big"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

func TestComponents(t *testing.T) {
	safe := ethrpc.Address{19: 0x5a}
	to := ethrpc.Address{19: 0x70}
	receiver := ethrpc.Address{19: 0xee}
	token := ethrpc.Address{19: 0x7c}
	multiSend := ethrpc.MustParseAddress("0x38869bf66a61cF6bDB996A6aE40D5853Fd43B526")
	tx := safenet.SafeTransaction{
		ChainID:   big.NewInt(100),
		Safe:      safe,
		To:        to,
		Value:     big.NewInt(1),
		Data:      ethrpc.Bytes{0x12, 0x34},
		Operation: safenet.OperationDelegateCall,
	}
	base := call{to: to, value: big.NewInt(1), data: ethrpc.Bytes{0x12, 0x34}, operation: safenet.OperationDelegateCall}
	withRefund := func(tx safenet.SafeTransaction, gasToken, receiver ethrpc.Address, gasPrice *big.Int) safenet.SafeTransaction {
		tx.SafeTxGas, tx.BaseGas, tx.GasPrice = big.NewInt(100_000), big.NewInt(20_000), gasPrice
		tx.GasToken, tx.RefundReceiver = gasToken, receiver
		return tx
	}
	// reverting calls MultiSend, which reverts.
	reverting := tx
	reverting.To, reverting.Data, reverting.Operation = multiSend, solabi.Call(multiSendSelector, []byte{}), safenet.OperationCall

	gwei := big.NewInt(1_000_000_000)
	refund := new(big.Int).Mul(big.NewInt(120_000), gwei)
	etherRefund := func(to ethrpc.Address, amount *big.Int) call {
		return call{to: to, value: amount, data: ethrpc.Bytes{}, operation: safenet.OperationCall}
	}
	tokenRefund := func(amount *big.Int) call {
		return call{
			to:        token,
			value:     new(big.Int),
			data:      solabi.Call(solabi.Selector("transfer(address,uint256)"), receiver, amount),
			operation: safenet.OperationCall,
		}
	}

	tests := []struct {
		name  string
		tx    safenet.SafeTransaction
		calls []call
	}{
		{"no refund", withRefund(tx, token, receiver, new(big.Int)), []call{base}},
		{"ether refund", withRefund(tx, ethrpc.Address{}, receiver, gwei), []call{base, etherRefund(receiver, refund)}},
		{
			"ether refund to the executor",
			withRefund(tx, ethrpc.Address{}, ethrpc.Address{}, gwei),
			[]call{base, etherRefund(ethrpc.Address{}, refund)},
		},
		{
			"overflowing ether refund",
			withRefund(tx, ethrpc.Address{}, receiver, maxUint256),
			[]call{base, etherRefund(receiver, maxUint256)},
		},
		{"token refund", withRefund(tx, token, receiver, gwei), []call{base, tokenRefund(refund)}},
		{"overflowing token refund", withRefund(tx, token, receiver, maxUint256), []call{base, tokenRefund(maxUint256)}},
		{"reverts", reverting, []call{revertingMultiSend(multiSend)}},
		{"reverts with a refund", withRefund(reverting, token, receiver, gwei), []call{revertingMultiSend(multiSend), tokenRefund(refund)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := components(&request(test.tx).Proposal.Transaction)
			if err != nil {
				t.Fatal(err)
			}
			want := safeID{address: safe, chainID: big.NewInt(100)}
			if got.safe.address != want.address || got.safe.chainID.Cmp(want.chainID) != 0 || !equalCalls(got.calls, test.calls) {
				t.Errorf("components() = %+v, want %+v with calls %+v", got, want, test.calls)
			}
		})
	}
}
