package checks

import (
	"math/big"
	"reflect"
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
	tx := safenet.SafeTransaction{
		ChainID:   big.NewInt(100),
		Safe:      safe,
		To:        to,
		Value:     big.NewInt(1),
		Data:      ethrpc.Bytes{0x12, 0x34},
		Operation: safenet.OperationDelegateCall,
	}
	withRefund := func(gasToken, receiver ethrpc.Address, gasPrice *big.Int) safenet.SafeTransaction {
		tx := tx
		tx.SafeTxGas, tx.BaseGas, tx.GasPrice = big.NewInt(100_000), big.NewInt(20_000), gasPrice
		tx.GasToken, tx.RefundReceiver = gasToken, receiver
		return tx
	}
	gwei := big.NewInt(1_000_000_000)
	refund := new(big.Int).Mul(big.NewInt(120_000), gwei)

	tests := []struct {
		name   string
		tx     safenet.SafeTransaction
		refund []call
	}{
		{"no refund", withRefund(token, receiver, new(big.Int)), nil},
		{
			"ether refund",
			withRefund(ethrpc.Address{}, receiver, gwei),
			[]call{{to: receiver, value: refund, data: ethrpc.Bytes{}, operation: safenet.OperationCall}},
		},
		{
			"ether refund to the executor",
			withRefund(ethrpc.Address{}, ethrpc.Address{}, gwei),
			[]call{{to: ethrpc.Address{}, value: refund, data: ethrpc.Bytes{}, operation: safenet.OperationCall}},
		},
		{
			"token refund",
			withRefund(token, receiver, gwei),
			[]call{{
				to:        token,
				value:     new(big.Int),
				data:      solabi.Call(solabi.Selector("transfer(address,uint256)"), receiver, refund),
				operation: safenet.OperationCall,
			}},
		},
		{
			"overflowing refund",
			withRefund(token, receiver, maxUint256),
			[]call{{
				to:        token,
				value:     new(big.Int),
				data:      solabi.Call(solabi.Selector("transfer(address,uint256)"), receiver, maxUint256),
				operation: safenet.OperationCall,
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := components(&request(test.tx).Proposal.Transaction)
			if err != nil {
				t.Fatal(err)
			}
			want := &transactionComponents{
				safe: safeID{address: safe, chainID: big.NewInt(100)},
				calls: append(
					[]call{{to: to, value: big.NewInt(1), data: ethrpc.Bytes{0x12, 0x34}, operation: safenet.OperationDelegateCall}},
					test.refund...,
				),
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("components() = %+v, want %+v", got, want)
			}
		})
	}
}
