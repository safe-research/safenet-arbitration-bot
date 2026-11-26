// Package simulate runs a Safe transaction with eth_call, on the state of the
// Safe's chain at a block, in the Safe's own context and without its
// signatures, and decodes why it reverted.
//
// A simulation shows what the transaction would do on that state, which is
// onchain data (Charter § 2.8), not the effects of the transaction after the
// proposal, which can do otherwise in a later block.
package simulate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// Status is the outcome of a simulated call.
type Status string

const (
	// Success is the status of a call that returned.
	Success Status = "success"
	// Reverted is the status of a call that reverted, or halted, such as on an
	// invalid opcode, which the Safe sees as the same failure.
	Reverted Status = "reverted"
)

// The methods of a simulation.
const (
	// MethodCall is a call from the Safe with eth_call, which runs the called
	// account's code in its own context, as a Safe transaction's call does.
	MethodCall = "eth_call"
	// MethodSimulateAndRevert is an eth_call of the Safe's simulateAndRevert, which
	// delegate calls the target from the Safe, in the Safe's context, as a Safe
	// transaction's delegate call does, and reverts with the outcome.
	MethodSimulateAndRevert = "simulateAndRevert"
)

// Result is the outcome of a simulated Safe transaction.
type Result struct {
	// Method is MethodCall or MethodSimulateAndRevert.
	Method string `json:"method"`
	// Origin is the simulation's tx.origin: the Safe for a call, and the zero
	// address for a delegate call. That of an execution is the account that sends
	// its transaction, which can't be the Safe.
	Origin ethrpc.Address `json:"origin"`
	Status Status         `json:"status"`
	// ReturnData is the data that the call returned, or its revert data, if the
	// node reports it.
	ReturnData ethrpc.Bytes `json:"returnData"`
	// RevertReason is the node's message for a call with eth_call that reverted or
	// halted.
	RevertReason string `json:"revertReason,omitempty"`
	// ValueShortfall is how much more than the Safe's balance a call from the Safe
	// sends, in wei, or nil if it doesn't.
	ValueShortfall *big.Int `json:"valueShortfall,omitempty"`
	// Error is the revert data of a call that reverted, decoded, or nil if it
	// returned, or reverted with less than a selector.
	Error *RevertError `json:"error"`
}

// Simulation is a Safe transaction to run with eth_call.
type Simulation struct {
	// Method is MethodCall or MethodSimulateAndRevert.
	Method  string
	Request ethrpc.CallRequest
}

// simulateAndRevertSelector is the selector of the simulateAndRevert of the
// StorageAccessible contract that the Safe singletons of § 2.1 inherit.
var simulateAndRevertSelector = solabi.Selector("simulateAndRevert(address,bytes)")

// TransactionOf returns the simulation of the Safe transaction tx, without its
// signatures: a call from the Safe to tx.To with tx.Value and tx.Data, or a
// delegate call with the Safe's simulateAndRevert, which delegate calls tx.To
// with tx.Data from the Safe. A delegate call sends no value, so tx.Value is
// ignored for it, as the Safe ignores it.
//
// simulateAndRevert is a function of the StorageAccessible contract that the
// Safe and SafeL2 singletons of 1.3.0, 1.4.1, and 1.5.0 inherit, so it needs a
// Safe whose singleton is one of them. Anyone can call it, and it has no
// effects, since it reverts.
func TransactionOf(tx *safenet.SafeTransaction) (Simulation, error) {
	switch tx.Operation {
	case safenet.OperationCall:
		req := ethrpc.CallRequest{From: tx.Safe, To: tx.To, Data: tx.Data}
		if tx.Value != nil && tx.Value.Sign() > 0 {
			req.Value = (*ethrpc.BigQuantity)(tx.Value)
		}
		return Simulation{Method: MethodCall, Request: req}, nil
	case safenet.OperationDelegateCall:
		data := solabi.Call(simulateAndRevertSelector, tx.To, []byte(tx.Data))
		return Simulation{Method: MethodSimulateAndRevert, Request: ethrpc.CallRequest{To: tx.Safe, Data: data}}, nil
	}
	return Simulation{}, fmt.Errorf("the Safe transaction's operation %d is neither a call nor a delegate call", tx.Operation)
}

// Run runs sim on the state at block, as if the executor sends no value with
// execTransaction. A call whose value is more than the Safe's balance at block
// then reverts without being run, though execTransaction is payable, so the
// executor could send the shortfall.
//
// The simulation's tx.origin is sim.Request.From, the Safe for a call, and the
// zero address for a delegate call, which is its msg.sender too, while that of
// an execution is the account that sends its transaction. A target that checks
// tx.origin, such as with require(msg.sender == tx.origin), can therefore do
// otherwise when the transaction is executed.
//
// It fails if a request to the node fails rather than being answered
// (ethrpc.Transient), or is answered with an error that isn't the call's
// outcome, so that a node that doesn't run the call is never read as a call
// that reverted.
func Run(ctx context.Context, eth *ethrpc.Client, sim Simulation, block ethrpc.BlockNumber) (*Result, error) {
	result := &Result{Method: sim.Method, Origin: sim.Request.From, Status: Success, ReturnData: ethrpc.Bytes{}}
	if value := (*big.Int)(sim.Request.Value); value != nil {
		balance, err := eth.GetBalance(ctx, sim.Request.From, block)
		if err != nil {
			return nil, fmt.Errorf("reading the balance of %s: %w", sim.Request.From, err)
		}
		if balance.Cmp(value) < 0 {
			result.Status, result.ValueShortfall = Reverted, new(big.Int).Sub(value, balance)
			return result, nil
		}
	}
	out, err := eth.Call(ctx, sim.Request, block)
	rpcErr, _ := errors.AsType[*ethrpc.Error](err)
	outcome := ethrpc.Reverted(err) || ethrpc.Halted(err)
	switch {
	case err != nil && !outcome:
		return nil, fmt.Errorf("simulating the call to %s: %w", sim.Request.To, err)
	case sim.Method == MethodSimulateAndRevert && err == nil:
		return nil, fmt.Errorf("simulateAndRevert of %s returned instead of reverting, so its singleton isn't a Safe that has it", sim.Request.To)
	case sim.Method == MethodSimulateAndRevert:
		data := revertData(rpcErr)
		if len(data) == 0 {
			return nil, fmt.Errorf("the node didn't return the revert data of simulateAndRevert (%q), which has the outcome of the simulation", rpcErr.Message)
		}
		success, returned, err := decodeSimulateAndRevert(data)
		if err != nil {
			return nil, err
		}
		result.ReturnData = returned
		if !success {
			result.Status = Reverted
		}
	case err != nil:
		result.Status, result.RevertReason, result.ReturnData = Reverted, rpcErr.Message, revertData(rpcErr)
	default:
		result.ReturnData = out
	}
	if result.Status == Reverted {
		result.Error = decodeRevert(result.ReturnData)
	}
	return result, nil
}

// revertData returns the revert data of a node's answer that a call reverted,
// or none if it has none, or data that isn't bytes.
func revertData(rpcErr *ethrpc.Error) ethrpc.Bytes {
	var data ethrpc.Bytes
	if len(rpcErr.Data) == 0 || json.Unmarshal(rpcErr.Data, &data) != nil || data == nil {
		return ethrpc.Bytes{}
	}
	return data
}

// decodeSimulateAndRevert decodes the revert data of the Safe's
// simulateAndRevert: whether the delegate call succeeded, and the data that it
// returned or reverted with. The Safe versions of § 2.1 revert with
// abi.encodePacked(success, returnData.length, returnData), as their
// StorageAccessible's assembly writes it, which its comments call
// abi.encode(success, returnData) but which has no offset of the bytes: a word
// that is 0 or 1, a word with the length, and the data, unpadded.
func decodeSimulateAndRevert(data []byte) (bool, ethrpc.Bytes, error) {
	d := solabi.NewDecoder(data)
	success := d.Bool(0)
	length := d.Uint(1)
	if d.Err() != nil || !length.IsUint64() || length.Uint64() != uint64(len(data))-64 {
		return false, nil, fmt.Errorf("the revert data of simulateAndRevert, %s, isn't its success flag, length, and data", summarize(data))
	}
	return success, ethrpc.Bytes(data[64:]), nil
}

// summarize returns data as hex, shortened to its first 68 bytes.
func summarize(data []byte) string {
	if len(data) <= 68 {
		return ethrpc.Bytes(data).String()
	}
	return fmt.Sprintf("%s... (%d bytes)", ethrpc.Bytes(data[:68]), len(data))
}

// RevertError is the revert data of a call, decoded.
type RevertError struct {
	Selector ethrpc.Bytes `json:"selector"`
	// Signature is "Error(string)" or "Panic(uint256)", which Solidity reverts
	// with, or empty for another error.
	Signature string `json:"signature,omitempty"`
	// Args are the error's arguments, or nil for another error, or data that
	// doesn't decode as the error.
	Args []Value `json:"args"`
}

// Value is an argument of an error.
type Value struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Value is a string or a *big.Int.
	Value any `json:"value"`
}

// The selectors of the errors that Solidity reverts with, for require and
// revert with a message, and for a failed assertion or arithmetic.
var (
	errorSelector = solabi.Selector("Error(string)")
	panicSelector = solabi.Selector("Panic(uint256)")
)

// decodeRevert decodes the revert data of a call as Error(string) or
// Panic(uint256), or else only by its selector. It returns nil for data without
// a selector, such as that of revert() or of running out of gas.
func decodeRevert(data []byte) *RevertError {
	if len(data) < 4 {
		return nil
	}
	revert := &RevertError{Selector: ethrpc.Bytes(data[:4])}
	d := solabi.NewDecoder(data[4:])
	var arg Value
	switch [4]byte(data[:4]) {
	case errorSelector:
		revert.Signature, arg = "Error(string)", Value{Name: "message", Type: "string", Value: d.String(0)}
	case panicSelector:
		revert.Signature, arg = "Panic(uint256)", Value{Name: "code", Type: "uint256", Value: d.Uint(0)}
	default:
		return revert
	}
	if d.Err() == nil {
		revert.Args = []Value{arg}
	}
	return revert
}
