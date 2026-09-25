package checks

import (
	"fmt"
	"math/big"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// transactionComponents are the parts of a Safe transaction that checks judge.
type transactionComponents struct {
	safe safeID
	// calls are the calls that the Safe makes, as expandTransactionCalls returns
	// them, followed by a synthetic call that pays the largest gas refund that the
	// transaction allows, if it has one. See gasRefund.
	calls []call
}

// safeID identifies the Safe that makes a transaction's calls.
type safeID struct {
	address ethrpc.Address
	chainID *big.Int
}

// call is a call that a Safe makes.
type call struct {
	to        ethrpc.Address
	value     *big.Int
	data      ethrpc.Bytes
	operation safenet.Operation
}

// components returns the components of a Safe transaction. It returns an error
// if the transaction is missing an amount, as a request read from a file can
// be, rather than take it to be zero.
func components(tx *safenet.SafeTransaction) (*transactionComponents, error) {
	for _, amount := range []struct {
		name  string
		value *big.Int
	}{
		{"chainId", tx.ChainID},
		{"value", tx.Value},
		{"safeTxGas", tx.SafeTxGas},
		{"baseGas", tx.BaseGas},
		{"gasPrice", tx.GasPrice},
	} {
		if amount.value == nil {
			return nil, fmt.Errorf("safe transaction has no %s", amount.name)
		}
	}
	safe := safeID{address: tx.Safe, chainID: tx.ChainID}
	calls := expandTransactionCalls(tx)
	if refund, ok := gasRefund(tx); ok {
		calls = append(calls, refund)
	}
	return &transactionComponents{safe: safe, calls: calls}, nil
}

// transferSelector is the selector of ERC-20's transfer(address,uint256), with
// which the Safe pays gas refunds in tokens.
var transferSelector = solabi.Selector("transfer(address,uint256)")

// maxUint256 is the largest uint256.
var maxUint256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// gasRefund returns a synthetic call that pays the largest gas refund that a
// Safe transaction allows. It returns false if the transaction's gas price is
// zero, in which case the Safe pays no refund.
//
// The Safe refunds (gasUsed + baseGas) * gasPrice, where gasUsed is the gas
// that it used to make the transaction's call, and ether refunds use the lower
// of gasPrice and the executing transaction's gas price. Since Safe 1.3.0, the
// call gets at most safeTxGas when the gas price isn't zero, so the refund is
// at most (safeTxGas + baseGas) * gasPrice, give or take the gas of the call
// instruction itself. A refund that overflows a uint256 reverts, so the refund
// is at most the largest uint256 too.
//
// The refund goes to the refund receiver, or to the account that executes the
// transaction if the receiver is the zero address, in which case the call's
// recipient is the zero address too. It is an ether transfer if the gas token
// is the zero address, and a call to the gas token's transfer otherwise.
func gasRefund(tx *safenet.SafeTransaction) (call, bool) {
	if tx.GasPrice.Sign() == 0 {
		return call{}, false
	}
	amount := new(big.Int).Add(tx.SafeTxGas, tx.BaseGas)
	amount.Mul(amount, tx.GasPrice)
	if amount.Cmp(maxUint256) > 0 {
		amount.Set(maxUint256)
	}
	if tx.GasToken == (ethrpc.Address{}) {
		return call{to: tx.RefundReceiver, value: amount, data: ethrpc.Bytes{}, operation: safenet.OperationCall}, true
	}
	return call{
		to:        tx.GasToken,
		value:     new(big.Int),
		data:      solabi.Call(transferSelector, tx.RefundReceiver, amount),
		operation: safenet.OperationCall,
	}, true
}
