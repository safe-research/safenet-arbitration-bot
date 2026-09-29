// Package erc20 reads ERC-20 token history from a chain's logs.
package erc20

import (
	"context"
	"fmt"
	"math/big"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// transferEvent is the ERC-20 Transfer event. ERC-721 has an event with the
// same signature, whose token ID is indexed too, so it has four topics instead
// of three.
var transferEvent = solabi.Event("Transfer(address,address,uint256)")

// Transfer is an ERC-20 Transfer event.
type Transfer struct {
	Block    uint64      `json:"block"`
	TxHash   ethrpc.Hash `json:"txHash"`
	LogIndex uint64      `json:"logIndex"`
	// Token is the contract that emitted the event.
	Token ethrpc.Address `json:"token"`
	From  ethrpc.Address `json:"from"`
	To    ethrpc.Address `json:"to"`
	// Value is the amount in the token's smallest unit.
	Value *big.Int `json:"value"`
}

// TransfersFrom returns the Transfer events of any token that move tokens out
// of the balance of from, in the blocks fromBlock to toBlock, in order. It only
// includes those to the address to, unless to is nil.
//
// The events show that a token contract moved tokens, not what caused it: an
// account with an allowance can move tokens out of from without it making a
// transaction. Logs that don't have the shape of an ERC-20 Transfer event, such
// as ERC-721 ones, are left out.
func TransfersFrom(ctx context.Context, eth *ethrpc.Client, from ethrpc.Address, to *ethrpc.Address, fromBlock, toBlock ethrpc.BlockNumber) ([]Transfer, error) {
	topics := [][]ethrpc.Hash{{transferEvent}, {solabi.Address(from)}}
	if to != nil {
		topics = append(topics, []ethrpc.Hash{solabi.Address(*to)})
	}
	logs := eth.ScanLogs(ctx, ethrpc.LogFilter{FromBlock: fromBlock, ToBlock: toBlock, Topics: topics})

	transfers := []Transfer{}
	for log, err := range logs {
		if err != nil {
			return nil, fmt.Errorf("listing ERC-20 transfers from %s: %w", from, err)
		}
		if log.Removed || len(log.Topics) != 3 || len(log.Data) != 32 {
			continue
		}
		transfers = append(transfers, Transfer{
			Block:    uint64(log.BlockNumber),
			TxHash:   log.TransactionHash,
			LogIndex: uint64(log.LogIndex),
			Token:    log.Address,
			From:     ethrpc.Address(log.Topics[1][12:]),
			To:       ethrpc.Address(log.Topics[2][12:]),
			Value:    new(big.Int).SetBytes(log.Data),
		})
	}
	return transfers, nil
}
