package erc20

import (
	"context"
	"fmt"
	"math/big"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// The view functions of ERC-20 that Read calls.
var (
	nameSelector        = solabi.Selector("name()")
	symbolSelector      = solabi.Selector("symbol()")
	decimalsSelector    = solabi.Selector("decimals()")
	totalSupplySelector = solabi.Selector("totalSupply()")
	balanceOfSelector   = solabi.Selector("balanceOf(address)")
	allowanceSelector   = solabi.Selector("allowance(address,address)")
)

// Token is what an ERC-20 token contract returns about itself, and a holder's
// balance of it. Any contract can return any name and symbol, so they say what
// the contract claims to be, not what it is.
type Token struct {
	// Name and Symbol are empty if the contract doesn't return a string for them,
	// since they are optional in ERC-20.
	Name   string
	Symbol string
	// Decimals is nil if the contract's decimals() reverts, halts, or returns no
	// data, as it may, since it is optional in ERC-20 too.
	Decimals *uint8
	// TotalSupply is the total supply that the contract returns, in the token's
	// smallest unit, or nil if it doesn't return one.
	TotalSupply *big.Int
	// Balance is the holder's balance, in the token's smallest unit.
	Balance *big.Int
}

// Read calls the ERC-20 view functions of the account token as of block, for
// its name, symbol, decimals, total supply, and the balance of holder. It
// returns nil if the account doesn't answer balanceOf, and decimals or else
// allowance, as an ERC-20 token does, such as an account without code, one
// whose calls revert or halt, or one whose decimals() answers with data that
// isn't a uint8.
func Read(ctx context.Context, eth *ethrpc.Client, token, holder ethrpc.Address, block ethrpc.BlockNumber) (*Token, error) {
	result, ok, err := call(ctx, eth, token, block, solabi.Call(decimalsSelector))
	if err != nil {
		return nil, err
	}
	var decimals *uint8
	if ok {
		// A contract that has decimals() but doesn't answer it as an ERC-20 token does
		// isn't one. A uint8 is one word, so an answer of another length isn't one,
		// such as a string, whose first word is its offset.
		d := solabi.NewDecoder(result)
		n := d.Uint64(0)
		if len(result) != 32 || d.Err() != nil || n > 255 {
			return nil, nil
		}
		decimals = new(uint8(n))
	}
	if result, ok, err = call(ctx, eth, token, block, solabi.Call(balanceOfSelector, holder)); err != nil || !ok {
		return nil, err
	}
	// A uint256 is one word too.
	if len(result) != 32 {
		return nil, nil
	}
	t := &Token{Decimals: decimals, Balance: new(big.Int).SetBytes(result)}
	// An ERC-721 token answers balanceOf too, but has no allowance, which ERC-20
	// requires, so a contract without decimals must answer it.
	if decimals == nil {
		if allowance, err := Allowance(ctx, eth, token, holder, holder, block); err != nil || allowance == nil {
			return nil, err
		}
	}
	for _, s := range []struct {
		selector [4]byte
		value    *string
	}{{nameSelector, &t.Name}, {symbolSelector, &t.Symbol}} {
		result, ok, err := call(ctx, eth, token, block, solabi.Call(s.selector))
		if err != nil {
			return nil, err
		}
		if ok {
			d := solabi.NewDecoder(result)
			if value := d.String(0); d.Err() == nil {
				*s.value = value
			}
		}
	}
	if result, ok, err = call(ctx, eth, token, block, solabi.Call(totalSupplySelector)); err != nil {
		return nil, err
	}
	if ok && len(result) == 32 {
		t.TotalSupply = new(big.Int).SetBytes(result)
	}
	return t, nil
}

// Allowance calls the ERC-20 allowance function of the account token as of
// block, and returns the amount that spender may transfer from owner, in the
// token's smallest unit, or nil if the account doesn't answer as an ERC-20
// token does.
func Allowance(ctx context.Context, eth *ethrpc.Client, token, owner, spender ethrpc.Address, block ethrpc.BlockNumber) (*big.Int, error) {
	result, ok, err := call(ctx, eth, token, block, solabi.Call(allowanceSelector, owner, spender))
	if err != nil || !ok {
		return nil, err
	}
	if len(result) != 32 {
		return nil, nil
	}
	return new(big.Int).SetBytes(result), nil
}

// call calls the account token with data as of block, and returns its return
// data, or false if the call reverted or halted, such as on an invalid opcode,
// or returned no data, as an account without code does, or a contract without
// the function whose fallback returns nothing. Any other error fails the call,
// so that a node that fails to answer isn't read as an account that isn't a
// token.
func call(ctx context.Context, eth *ethrpc.Client, token ethrpc.Address, block ethrpc.BlockNumber, data []byte) (ethrpc.Bytes, bool, error) {
	result, err := eth.Call(ctx, ethrpc.CallRequest{To: token, Data: data}, block)
	if err != nil {
		if ethrpc.Reverted(err) || ethrpc.Halted(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("calling %s at block %d: %w", token, block, err)
	}
	if len(result) == 0 {
		return nil, false, nil
	}
	return result, true, nil
}
