package simulate

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

var (
	safe   = ethrpc.Address{0: 0x5a, 19: 0xfe}
	target = ethrpc.Address{0: 0xc0, 19: 1}
	batch  = ethrpc.MustParseAddress("0x40A2aCCbd92BCA938b02010E17A5b8929b49130D")
)

// node serves eth_call at block 100: it returns result, or answers with the
// JSON-RPC error answer if it is set, and records the last call and the number
// of calls. It serves the Safe's balance, and answers every request but
// eth_chainId with the HTTP status status instead, if it is set.
type node struct {
	result  ethrpc.Bytes
	answer  map[string]any
	balance int64
	status  int
	last    map[string]any
	calls   int
}

func (n *node) serve(t *testing.T) *ethrpc.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64            `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if n.status != 0 && req.Method != "eth_chainId" {
			// A server's error, with a JSON-RPC error that says that the call reverted,
			// which must not be read as a revert.
			w.WriteHeader(n.status)
			res["error"] = map[string]any{"code": 3, "message": "execution reverted"}
			json.NewEncoder(w).Encode(res)
			return
		}
		var block ethrpc.BlockNumber
		if len(req.Params) > 0 {
			json.Unmarshal(req.Params[len(req.Params)-1], &block)
		}
		switch {
		case req.Method == "eth_chainId":
			res["result"] = "0x1"
		case block != 100:
			res["error"] = map[string]any{"code": -32000, "message": "header not found"}
		case req.Method == "eth_getBalance":
			res["result"] = ethrpc.Quantity(n.balance)
		case req.Method == "eth_call":
			json.Unmarshal(req.Params[0], &n.last)
			n.calls++
			if n.answer != nil {
				res["error"] = n.answer
			} else {
				res["result"] = n.result
			}
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(server.Close)
	eth, err := ethrpc.NewClient(t.Context(), ethrpc.Mainnet, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return eth
}

// reverted is a node's answer that a call reverted with code and message, and
// data if it isn't nil.
func reverted(code int, message string, data []byte) map[string]any {
	answer := map[string]any{"code": code, "message": message}
	if data != nil {
		answer["data"] = ethrpc.Bytes(data)
	}
	return answer
}

func TestTransactionOf(t *testing.T) {
	tx := &safenet.SafeTransaction{Safe: safe, To: target, Value: big.NewInt(3), Data: ethrpc.Bytes{1}, Operation: safenet.OperationCall}
	sim, err := TransactionOf(tx)
	if err != nil || sim.Method != MethodCall || sim.Request.From != safe || sim.Request.To != target ||
		(*big.Int)(sim.Request.Value).Int64() != 3 || sim.Request.Data.String() != "0x01" {
		t.Errorf("TransactionOf a call: got %+v, %v", sim, err)
	}
	tx.Value = new(big.Int)
	if sim, err := TransactionOf(tx); err != nil || sim.Request.Value != nil {
		t.Errorf("TransactionOf a call without value: got %+v, %v", sim, err)
	}

	// A delegate call is a call to the Safe's simulateAndRevert, from no sender,
	// with the target and its data, and no value, which a delegate call can't send.
	batchData := solabi.Call(solabi.Selector("multiSend(bytes)"), []byte{1, 2})
	tx = &safenet.SafeTransaction{Safe: safe, To: batch, Value: big.NewInt(9), Data: batchData, Operation: safenet.OperationDelegateCall}
	sim, err = TransactionOf(tx)
	want := solabi.Call(solabi.Selector("simulateAndRevert(address,bytes)"), batch, []byte(batchData))
	if err != nil || sim.Method != MethodSimulateAndRevert || sim.Request.From != (ethrpc.Address{}) || sim.Request.To != safe ||
		sim.Request.Value != nil || sim.Request.Data.String() != ethrpc.Bytes(want).String() {
		t.Errorf("TransactionOf a delegate call: got %+v, %v", sim, err)
	}

	tx.Operation = 2
	if _, err := TransactionOf(tx); err == nil {
		t.Error("TransactionOf an operation that is neither: expected an error")
	}
}

func TestRun(t *testing.T) {
	call := func(value int64) Simulation {
		sim, err := TransactionOf(&safenet.SafeTransaction{Safe: safe, To: target, Value: big.NewInt(value), Data: ethrpc.Bytes{1, 2, 3, 4}})
		if err != nil {
			t.Fatal(err)
		}
		return sim
	}
	message := solabi.Call(solabi.Selector("Error(string)"), "GS013")
	gs013 := &RevertError{Selector: ethrpc.Bytes{0x08, 0xc3, 0x79, 0xa0}, Signature: "Error(string)", Args: []Value{{"message", "string", "GS013"}}}
	for _, test := range []struct {
		name  string
		node  node
		value int64
		want  Result
		// calls is the number of eth_calls that the simulation makes.
		calls int
	}{
		{
			"returned", node{result: ethrpc.Bytes{0xaa}, balance: 5}, 5,
			Result{Method: MethodCall, Origin: safe, Status: Success, ReturnData: ethrpc.Bytes{0xaa}}, 1,
		},
		{
			"reverted", node{answer: reverted(3, "execution reverted: GS013", message)}, 0,
			Result{Method: MethodCall, Origin: safe, Status: Reverted, ReturnData: message, RevertReason: "execution reverted: GS013", Error: gs013}, 1,
		},
		{
			// A node that answers a revert with another code, and only says so in its
			// message.
			"reverted by its message", node{answer: reverted(-32000, "execution reverted", message)}, 0,
			Result{Method: MethodCall, Origin: safe, Status: Reverted, ReturnData: message, RevertReason: "execution reverted", Error: gs013}, 1,
		},
		{
			"reverted without data", node{answer: reverted(3, "execution reverted", nil)}, 0,
			Result{Method: MethodCall, Origin: safe, Status: Reverted, ReturnData: ethrpc.Bytes{}, RevertReason: "execution reverted"}, 1,
		},
		{
			"halted", node{answer: reverted(-32003, "EVM error: InvalidFEOpcode", nil)}, 0,
			Result{Method: MethodCall, Origin: safe, Status: Reverted, ReturnData: ethrpc.Bytes{}, RevertReason: "EVM error: InvalidFEOpcode"}, 1,
		},
		{
			// The Safe can't send more than its balance, so the call fails without being
			// run, which nodes answer with an error that isn't a revert.
			"more than the balance", node{balance: 4, answer: reverted(-32003, "Insufficient funds for gas * price + value", nil)}, 5,
			Result{Method: MethodCall, Origin: safe, Status: Reverted, ReturnData: ethrpc.Bytes{}, ValueShortfall: big.NewInt(1)}, 0,
		},
	} {
		n := test.node
		got, err := Run(t.Context(), n.serve(t), call(test.value), 100)
		if err != nil || !reflect.DeepEqual(*got, test.want) {
			t.Errorf("%s: got %+v, %v; want %+v", test.name, got, err, test.want)
		}
		if n.calls != test.calls || n.calls > 0 && (n.last["from"] != strings.ToLower(safe.String()) || n.last["data"] != "0x01020304") {
			t.Errorf("%s: got %d eth_calls, the last %v; want %d from the Safe", test.name, n.calls, n.last, test.calls)
		}
	}

	// The call sends the transaction's value.
	n := &node{result: ethrpc.Bytes{}, balance: 5}
	if _, err := Run(t.Context(), n.serve(t), call(5), 100); err != nil || n.last["value"] != "0x5" {
		t.Errorf("a call with value: got eth_call %v, %v", n.last, err)
	}
	// A node's error that isn't the call's outcome isn't one.
	for name, n := range map[string]*node{
		"missing state": {answer: reverted(-32000, "missing trie node", nil)},
		"no block":      {},
	} {
		block := ethrpc.BlockNumber(100)
		if name == "no block" {
			block = 99
		}
		if got, err := Run(t.Context(), n.serve(t), call(0), block); err == nil {
			t.Errorf("%s: got %+v, want an error", name, got)
		}
	}
}

// recordedSimulateAndRevert is the revert data of the eth_call of
// simulateAndRevert that simulates the Safe transaction of request
// 0xcbfadf6b9595bdd5140712ca54bc13921d8ea8b587e9bd2522110b6ca6d2967b, a
// MultiSendCallOnly batch, at its Safe block 26432178 on Ethereum Mainnet, as
// an archive node answered it: the batch succeeded, and returned no data.
const recordedSimulateAndRevert = "0x" +
	"0000000000000000000000000000000000000000000000000000000000000001" +
	"0000000000000000000000000000000000000000000000000000000000000000"

// packed returns the revert data of simulateAndRevert for a delegate call that
// succeeded or not, with data, as the Safe's assembly writes it.
func packed(success bool, data []byte) []byte {
	flag := uint64(0)
	if success {
		flag = 1
	}
	return append(solabi.Encode(flag, uint64(len(data))), data...)
}

func TestRunSimulateAndRevert(t *testing.T) {
	sim, err := TransactionOf(&safenet.SafeTransaction{Safe: safe, To: batch, Data: ethrpc.Bytes{1}, Operation: safenet.OperationDelegateCall})
	if err != nil {
		t.Fatal(err)
	}
	recorded, _ := hex.DecodeString(strings.TrimPrefix(recordedSimulateAndRevert, "0x"))
	message := solabi.Call(solabi.Selector("Error(string)"), "GS013")
	for _, test := range []struct {
		name string
		data []byte
		want Result
	}{
		{"succeeded", recorded, Result{Method: MethodSimulateAndRevert, Status: Success, ReturnData: ethrpc.Bytes{}}},
		{"returned data", packed(true, []byte{1, 2}), Result{Method: MethodSimulateAndRevert, Status: Success, ReturnData: ethrpc.Bytes{1, 2}}},
		{
			"reverted", packed(false, message),
			Result{Method: MethodSimulateAndRevert, Status: Reverted, ReturnData: message, Error: &RevertError{
				Selector: ethrpc.Bytes{0x08, 0xc3, 0x79, 0xa0}, Signature: "Error(string)", Args: []Value{{"message", "string", "GS013"}},
			}},
		},
	} {
		n := &node{answer: reverted(3, "execution reverted", test.data)}
		got, err := Run(t.Context(), n.serve(t), sim, 100)
		if err != nil || !reflect.DeepEqual(*got, test.want) {
			t.Errorf("%s: got %+v, %v; want %+v", test.name, got, err, test.want)
		}
		if _, ok := n.last["from"]; ok || n.last["to"] != strings.ToLower(safe.String()) {
			t.Errorf("%s: got eth_call %v, want a call to the Safe with no sender", test.name, n.last)
		}
	}

	// simulateAndRevert that returns, halts, or reverts without data, or with data
	// that isn't its outcome, gives no outcome.
	for name, n := range map[string]*node{
		"returned": {result: ethrpc.Bytes{}},
		"halted":   {answer: reverted(-32000, "out of gas", nil)},
		"no data":  {answer: reverted(3, "execution reverted", []byte{})},
		"no flag":  {answer: reverted(3, "execution reverted", message)},
	} {
		if got, err := Run(t.Context(), n.serve(t), sim, 100); err == nil {
			t.Errorf("%s: got %+v, want an error", name, got)
		}
	}
}

// TestRunTransient checks that a node that fails to answer, even with a body
// that says that the call reverted, is never read as a call that reverted.
func TestRunTransient(t *testing.T) {
	// Statuses that httpretry retries, such as 503, would make the test wait.
	for _, status := range []int{500, 501} {
		for _, value := range []int64{0, 1} {
			sim, err := TransactionOf(&safenet.SafeTransaction{Safe: safe, To: target, Value: big.NewInt(value)})
			if err != nil {
				t.Fatal(err)
			}
			n := &node{status: status}
			if _, err := Run(t.Context(), n.serve(t), sim, 100); err == nil || !ethrpc.Transient(err) {
				t.Errorf("HTTP %d with value %d: got error %v, want a transient one", status, value, err)
			}
		}
	}
}

func TestDecodeSimulateAndRevert(t *testing.T) {
	message := solabi.Call(solabi.Selector("Error(string)"), "GS013")
	recorded, _ := hex.DecodeString(strings.TrimPrefix(recordedSimulateAndRevert, "0x"))
	for _, test := range []struct {
		name    string
		data    []byte
		success bool
		want    string
	}{
		{"recorded", recorded, true, "0x"},
		{"success with data", packed(true, []byte{1, 2, 3}), true, "0x010203"},
		{"revert with a message", packed(false, message), false, ethrpc.Bytes(message).String()},
	} {
		success, got, err := decodeSimulateAndRevert(test.data)
		if err != nil || success != test.success || got.String() != test.want {
			t.Errorf("%s: got %t, %s, %v; want %t, %s", test.name, success, got, err, test.success, test.want)
		}
	}

	// Data that isn't a flag, a length, and that many bytes is malformed, such as
	// abi.encode(success, data), which has the offset of the data.
	for name, data := range map[string][]byte{
		"empty":           nil,
		"no length":       solabi.Encode(uint64(1)),
		"flag that is 2":  solabi.Encode(uint64(2), uint64(0)),
		"short data":      solabi.Encode(uint64(1), uint64(3)),
		"long data":       append(solabi.Encode(uint64(1), uint64(0)), 1),
		"ABI-encoded":     solabi.Encode(true, []byte{}),
		"huge length":     solabi.Encode(uint64(1), new(big.Int).Lsh(big.NewInt(1), 255)),
		"length overflow": append(solabi.Encode(uint64(1), ^uint64(0)), make([]byte, 32)...),
	} {
		if _, _, err := decodeSimulateAndRevert(data); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDecodeRevert(t *testing.T) {
	errorString := solabi.Call(solabi.Selector("Error(string)"), "GS013")
	for _, test := range []struct {
		name string
		data []byte
		want *RevertError
	}{
		{"no data", nil, nil},
		{"short", []byte{1, 2, 3}, nil},
		{
			"Error(string)", errorString,
			&RevertError{Selector: ethrpc.Bytes{0x08, 0xc3, 0x79, 0xa0}, Signature: "Error(string)", Args: []Value{{"message", "string", "GS013"}}},
		},
		{
			"Panic(uint256)", solabi.Call(solabi.Selector("Panic(uint256)"), uint64(0x11)),
			&RevertError{Selector: ethrpc.Bytes{0x4e, 0x48, 0x7b, 0x71}, Signature: "Panic(uint256)", Args: []Value{{"code", "uint256", big.NewInt(0x11)}}},
		},
		{
			"malformed Error(string)", errorString[:4+32],
			&RevertError{Selector: ethrpc.Bytes{0x08, 0xc3, 0x79, 0xa0}, Signature: "Error(string)"},
		},
		{"unknown error", []byte{0xde, 0xad, 0xbe, 0xef, 0}, &RevertError{Selector: ethrpc.Bytes{0xde, 0xad, 0xbe, 0xef}}},
	} {
		if got := decodeRevert(test.data); !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s: got %+v, want %+v", test.name, got, test.want)
		}
	}
}
