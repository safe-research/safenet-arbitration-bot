package simulate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
)

// Trace is the trace of a simulation, with debug_traceCall and the callTracer:
// every call that it makes, nested to any depth, including those that create
// contracts.
type Trace struct {
	// Available is whether the node served the trace, and Reason is why it didn't:
	// its error, or that the call wasn't traced.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Calls are the frames of the trace, the simulated call first, each followed by
	// the frames of the calls that it makes, in order.
	Calls []Frame `json:"calls"`
}

// Frame is a call in a trace.
type Frame struct {
	// Depth is 0 for the simulated call, and one more for each call that a call
	// makes.
	Depth int `json:"depth"`
	// Type is the call's opcode, such as CALL, DELEGATECALL, STATICCALL, CREATE, or
	// CREATE2.
	Type string         `json:"type"`
	From ethrpc.Address `json:"from"`
	// To is the account called, or created, or nil for a creation that failed.
	To *ethrpc.Address `json:"to"`
	// Value is the native currency that the call sends, or nil for a STATICCALL,
	// which sends none, or a DELEGATECALL, whose value nodes report as that of the
	// call that it runs in.
	Value *big.Int `json:"value"`
	// Selector is the first 4 bytes of the call's data, or empty for a creation,
	// whose data is init code, or data shorter than that.
	Selector  ethrpc.Bytes `json:"selector"`
	InputSize int          `json:"inputSize"`
	// InitCodeHash is the hash of the init code of a CREATE or CREATE2, or nil for
	// another call.
	InitCodeHash *ethrpc.Hash `json:"initCodeHash,omitempty"`
	// Error is the node's error of a call that failed, such as "execution
	// reverted", and RevertReason the message that it reverted with, if any.
	Error        string `json:"error,omitempty"`
	RevertReason string `json:"revertReason,omitempty"`
}

// maxTraceDepth bounds the depth of the frames that a trace has: a trace deeper
// than the EVM's call depth limit of 1024 is malformed.
const maxTraceDepth = 1100

// TraceCall traces sim on the state at block with debug_traceCall and the
// callTracer. Most public nodes don't serve traces, so a node's answer that it
// can't trace the call, such as that it doesn't serve the method or the block's
// state, or that the trace timed out, and a refusal with an HTTP error status
// other than 429, such as a 401 page or a 503, give a trace that isn't
// available, with the error as its reason, as arbot executions reads a refused
// trace. A rate limit, a dropped connection, or a response that isn't JSON-RPC
// fails, since the node may serve traces, and so does a result that isn't a
// callTracer trace of sim's call, such as null.
func TraceCall(ctx context.Context, eth *ethrpc.Client, sim Simulation, block ethrpc.BlockNumber) (*Trace, error) {
	var top *ethrpc.CallFrame
	err := eth.RawRequest(ctx, &top, "debug_traceCall", sim.Request, block, map[string]any{"tracer": "callTracer"})
	httpErr, refused := errors.AsType[*ethrpc.HTTPError](err)
	_, answered := errors.AsType[*ethrpc.Error](err)
	switch {
	case err == nil && top == nil:
		return nil, fmt.Errorf("tracing the call to %s: debug_traceCall: null result", sim.Request.To)
	case err == nil:
		if err := top.Check(); err != nil {
			return nil, fmt.Errorf("tracing the call to %s: debug_traceCall: %w", sim.Request.To, err)
		}
		if !strings.EqualFold(top.Type, "CALL") || top.From != sim.Request.From || top.To == nil || *top.To != sim.Request.To ||
			!bytes.Equal(top.Input, sim.Request.Data) {
			return nil, fmt.Errorf("tracing the call to %s: debug_traceCall: the trace's top call isn't the simulated call", sim.Request.To)
		}
		return traceOf(top)
	case refused && httpErr.StatusCode != http.StatusTooManyRequests,
		!refused && answered && (!ethrpc.Transient(err) || ethrpc.TimedOut(err)):
		return &Trace{Reason: err.Error(), Calls: []Frame{}}, nil
	}
	return nil, fmt.Errorf("tracing the call to %s: %w", sim.Request.To, err)
}

// traceOf returns the trace whose top frame is top.
func traceOf(top *ethrpc.CallFrame) (*Trace, error) {
	trace := &Trace{Available: true, Calls: []Frame{}}
	if err := trace.add(top, 0); err != nil {
		return nil, err
	}
	return trace, nil
}

// add adds f, at depth, and the frames of the calls that it makes.
func (t *Trace) add(f *ethrpc.CallFrame, depth int) error {
	if depth > maxTraceDepth {
		return fmt.Errorf("the callTracer's trace is deeper than %d calls", maxTraceDepth)
	}
	typ := strings.ToUpper(f.Type)
	frame := Frame{Depth: depth, Type: typ, From: f.From, InputSize: len(f.Input), Error: f.Error, RevertReason: f.RevertReason}
	if f.To != nil && (*f.To != ethrpc.Address{} || !isCreate(typ)) {
		to := *f.To
		frame.To = &to
	}
	if f.Value != nil && typ != "DELEGATECALL" && typ != "STATICCALL" {
		frame.Value = new(big.Int).Set((*big.Int)(f.Value))
	}
	if isCreate(typ) {
		hash := ethrpc.Hash(keccak256.Hash(f.Input))
		frame.InitCodeHash = &hash
	} else if len(f.Input) >= 4 {
		frame.Selector = ethrpc.Bytes(f.Input[:4])
	}
	t.Calls = append(t.Calls, frame)
	for i := range f.Calls {
		if err := t.add(&f.Calls[i], depth+1); err != nil {
			return err
		}
	}
	return nil
}

// isCreate reports whether a frame's type is a creation.
func isCreate(typ string) bool {
	return typ == "CREATE" || typ == "CREATE2"
}
