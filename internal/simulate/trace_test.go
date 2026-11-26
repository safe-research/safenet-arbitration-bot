package simulate

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/create"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// parse parses the callTracer's trace raw.
func parse(t *testing.T, raw string) (*Trace, error) {
	t.Helper()
	var top ethrpc.CallFrame
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		t.Fatal(err)
	}
	return traceOf(&top)
}

// TestTrace reads the callTracer's trace of the simulateAndRevert call that
// simulates the Safe transaction of request
// 0xcbfadf6b9595bdd5140712ca54bc13921d8ea8b587e9bd2522110b6ca6d2967b at its
// Safe block, as an archive node returned it, with each frame's input shortened
// to 36 bytes and the fields that arbot doesn't read left out. Its batch calls
// a helper three times, which creates a contract with CREATE each time, nested
// in calls that delegate call the singleton and the MultiSendCallOnly.
func TestTrace(t *testing.T) {
	raw, err := os.ReadFile("testdata/trace.json")
	if err != nil {
		t.Fatal(err)
	}
	trace, err := parse(t, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !trace.Available || len(trace.Calls) != 16 {
		t.Fatalf("got %d frames, want 16", len(trace.Calls))
	}
	safe := ethrpc.MustParseAddress("0x47623C62f281807D615eeb4A2CEee9d97F9D3C49")
	helper := ethrpc.MustParseAddress("0xe530c247341e857042FdFf953E73Dd6c580f8D42")
	top, batch, creation := trace.Calls[0], trace.Calls[2], trace.Calls[5]
	// simulateAndRevert reverts by design.
	if top.Depth != 0 || top.Type != "CALL" || top.To == nil || *top.To != safe || top.Error != "execution reverted" ||
		top.Selector.String() != "0xb4faba09" || top.Value == nil || top.Value.Sign() != 0 || top.InitCodeHash != nil {
		t.Errorf("top frame: got %+v", top)
	}
	// The node gives a DELEGATECALL the value of the call that it runs in, which it
	// doesn't send.
	if batch.Depth != 2 || batch.Type != "DELEGATECALL" || batch.Selector.String() != "0x8d80ff0a" || batch.Error != "" || batch.Value != nil {
		t.Errorf("batch frame: got %+v", batch)
	}
	if creation.Depth != 4 || creation.Type != "CREATE" || creation.From != helper || creation.Selector != nil || creation.Error != "" {
		t.Errorf("creation frame: got %+v", creation)
	}
	// A STATICCALL sends no value.
	if s := trace.Calls[6]; s.Type != "STATICCALL" || s.Value != nil || s.Selector.String() != "0x82c63066" {
		t.Errorf("static call frame: got %+v", s)
	}
	// In JSON, a value that isn't sent is null, and a creation has no selector, but
	// the hash of its init code.
	for f, want := range map[*Frame]string{&batch: `"value":null,`, &creation: `"selector":"0x","inputSize":36,"initCodeHash":"0x`} {
		if data, err := json.Marshal(f); err != nil || !strings.Contains(string(data), want) {
			t.Errorf("%s frame: got JSON %s, %v; want it to contain %s", f.Type, data, err, want)
		}
	}

	// The contracts that the helper created with its nonces 1 to 3.
	var creations []Frame
	for _, f := range trace.Calls {
		if f.InitCodeHash != nil {
			creations = append(creations, f)
		}
	}
	if len(creations) != 3 {
		t.Fatalf("got %d creations, want 3", len(creations))
	}
	for i, c := range creations {
		want := create.Address(helper, uint64(i+1))
		if c.To == nil || *c.To != want || c.From != helper || c.Type != "CREATE" || c.Depth != 4 || c.InputSize != 36 || c.Error != "" {
			t.Errorf("creation %d: got %+v, want %s created by %s", i, c, want, helper)
		}
	}
}

func TestTraceFailedCreation(t *testing.T) {
	// A CREATE2 that failed has no address, nor does a CREATE whose address is the
	// zero address, and a nested call has its error and revert reason.
	trace, err := parse(t, `{"type": "CALL", "from": "0x0000000000000000000000000000000000000001", "to": "0x0000000000000000000000000000000000000002",
		"value": "0x10", "input": "0x12345678", "calls": [
			{"type": "CREATE2", "from": "0x0000000000000000000000000000000000000002", "value": "0x0", "input": "0x6000", "error": "contract address collision"},
			{"type": "CREATE", "from": "0x0000000000000000000000000000000000000002", "to": "0x0000000000000000000000000000000000000000", "value": "0x0", "input": "0x00", "error": "out of gas"},
			{"type": "CALL", "from": "0x0000000000000000000000000000000000000002", "to": "0x0000000000000000000000000000000000000003",
				"input": "0x01", "error": "execution reverted", "revertReason": "GS013"}
		]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Calls) != 4 || trace.Calls[0].Value.Int64() != 16 {
		t.Fatalf("got %+v", trace)
	}
	if c := trace.Calls[1]; c.To != nil || c.Type != "CREATE2" || c.InputSize != 2 || c.InitCodeHash == nil || c.Error != "contract address collision" {
		t.Errorf("failed creation: got %+v", c)
	}
	if c := trace.Calls[2]; c.To != nil || c.Type != "CREATE" || c.InitCodeHash == nil || c.Error != "out of gas" {
		t.Errorf("failed creation at the zero address: got %+v", c)
	}
	if f := trace.Calls[3]; f.Selector != nil || f.InputSize != 1 || f.RevertReason != "GS013" || f.Depth != 1 {
		t.Errorf("reverted call: got %+v", f)
	}
}

func TestTraceDepth(t *testing.T) {
	// A trace as deep as the depth bound is read, and a deeper one, which no call
	// can make, isn't.
	nested := func(depth int) string {
		return strings.Repeat(`{"type": "CALL", "calls": [`, depth) + `{"type": "CALL"}` + strings.Repeat(`]}`, depth)
	}
	if trace, err := parse(t, nested(maxTraceDepth)); err != nil || len(trace.Calls) != maxTraceDepth+1 {
		t.Errorf("trace of depth %d: got %v", maxTraceDepth, err)
	}
	if _, err := parse(t, nested(maxTraceDepth+1)); err == nil {
		t.Errorf("trace of depth %d: expected an error", maxTraceDepth+1)
	}
}

func TestTraceCall(t *testing.T) {
	sim := Simulation{Method: MethodCall, Request: ethrpc.CallRequest{From: safe, To: target}}
	// A node that serves the trace.
	n := &node{trace: json.RawMessage(`{"type": "CALL", "from": "0x5a000000000000000000000000000000000000fe", "to": "0xc000000000000000000000000000000000000001", "input": "0x"}`)}
	trace, err := TraceCall(t.Context(), n.serve(t), sim, 100)
	if err != nil || !trace.Available || len(trace.Calls) != 1 {
		t.Errorf("TraceCall: got %+v, %v", trace, err)
	}
	if n.last["from"] != strings.ToLower(safe.String()) || n.last["to"] != strings.ToLower(target.String()) {
		t.Errorf("debug_traceCall: got %v", n.last)
	}

	// A node that answers that it can't trace the call, or refuses it with an HTTP
	// error status, would answer every trace the same way, and says nothing about
	// the simulation, so the trace is unavailable.
	for name, n := range map[string]*node{
		"method":    {},
		"block":     {trace: json.RawMessage(`{}`)},
		"state":     {traceAnswer: map[string]any{"code": -32000, "message": "missing trie node"}},
		"timed out": {traceAnswer: map[string]any{"code": -32016, "message": "Request timed out"}},
		"HTTP 401":  {traceStatus: http.StatusUnauthorized},
		"HTTP 500":  {traceStatus: http.StatusInternalServerError},
		"HTTP 503":  {traceStatus: http.StatusServiceUnavailable},
	} {
		block := ethrpc.BlockNumber(100)
		if name == "block" {
			block = 99
		}
		trace, err := TraceCall(t.Context(), n.serve(t), sim, block)
		if err != nil || trace.Available || trace.Reason == "" || trace.Calls == nil {
			t.Errorf("TraceCall from a node without the %s: got %+v, %v; want an unavailable trace with a reason", name, trace, err)
		}
	}
	// A rate limit, or a response that isn't JSON-RPC, says nothing about whether
	// the node serves traces, and a null trace isn't one.
	for name, n := range map[string]*node{
		"HTTP 429":    {traceStatus: http.StatusTooManyRequests},
		"HTML page":   {traceStatus: http.StatusOK},
		"null result": {trace: json.RawMessage(`null`)},
	} {
		if trace, err := TraceCall(t.Context(), n.serve(t), sim, 100); err == nil || name != "null result" && !ethrpc.Transient(err) {
			t.Errorf("TraceCall with %s: got %+v, %v; want an error", name, trace, err)
		}
	}
}
