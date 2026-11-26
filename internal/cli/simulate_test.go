package cli

import (
	"bytes"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/simulate"
)

func TestPrintSimulation(t *testing.T) {
	safe := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	to := ethrpc.MustParseAddress("0x40A2aCCbd92BCA938b02010E17A5b8929b49130D")
	call := &safenet.SafeTransaction{To: to, Value: big.NewInt(3), Data: ethrpc.Bytes{0xa9, 0x05, 0x9c, 0xbb, 0}}
	delegateCall := &safenet.SafeTransaction{To: to, Value: new(big.Int), Data: ethrpc.Bytes{0x8d, 0x80, 0xff, 0x0a}, Operation: safenet.OperationDelegateCall}
	header := func(tx *safenet.SafeTransaction, method string) string {
		return "The Safe transaction of " + safe.String() + " on chain 1, simulated on the state at block 200\n" +
			"  To         " + to.String() + "\n" +
			"  Value      " + tx.Value.String() + "\n" +
			"  Operation  " + tx.Operation.String() + "\n" +
			"  Data       " + summarizeData(tx.Data) + "\n" +
			"  Method     " + method + "\n"
	}
	words := append(bytes.Repeat([]byte{0x11}, 32), 0x22)
	for _, test := range []struct {
		name   string
		tx     *safenet.SafeTransaction
		result simulate.Result
		want   string
	}{
		{
			"returned", call,
			simulate.Result{Method: simulate.MethodCall, Status: simulate.Success, ReturnData: words},
			header(call, "eth_call from the Safe") + "\nResult       success\n" +
				"  Returned   0x" + string(bytes.Repeat([]byte("11"), 32)) + "\n" +
				"             0x22\n",
		},
		{
			// The node's message and the error's arguments come from the contract, so they
			// are quoted.
			"reverted", call,
			simulate.Result{
				Method: simulate.MethodCall, Status: simulate.Reverted, ReturnData: ethrpc.Bytes{0x08, 0xc3, 0x79, 0xa0},
				RevertReason: "execution reverted: GS\x1b013",
				Error: &simulate.RevertError{Selector: ethrpc.Bytes{0x08, 0xc3, 0x79, 0xa0}, Signature: "Error(string)", Args: []simulate.Value{
					{Name: "message", Type: "string", Value: "GS\x1b013"},
				}},
			},
			header(call, "eth_call from the Safe") + "\nResult       reverted\n" +
				"  Reason     \"execution reverted: GS\\x1b013\"\n" +
				"  Error      0x08c379a0 Error(string)\n" +
				"  error      string message = \"GS\\x1b013\"\n" +
				"  Returned   0x08c379a0\n",
		},
		{
			"reverted with a panic", delegateCall,
			simulate.Result{
				Method: simulate.MethodSimulateAndRevert, Status: simulate.Reverted, ReturnData: ethrpc.Bytes{0x4e},
				Error: &simulate.RevertError{Selector: ethrpc.Bytes{0x4e, 0x48, 0x7b, 0x71}, Signature: "Panic(uint256)", Args: []simulate.Value{
					{Name: "code", Type: "uint256", Value: big.NewInt(17)},
				}},
			},
			header(delegateCall, "simulateAndRevert of the Safe, which delegate calls the target in the Safe's context") +
				"\nResult       reverted\n" +
				"  Error      0x4e487b71 Panic(uint256)\n" +
				"  error      uint256 code = 17\n" +
				"  Returned   0x4e\n",
		},
		{
			"reverted with an unknown error", delegateCall,
			simulate.Result{
				Method: simulate.MethodSimulateAndRevert, Status: simulate.Reverted, ReturnData: ethrpc.Bytes{0xde, 0xad, 0xbe, 0xef},
				Error: &simulate.RevertError{Selector: ethrpc.Bytes{0xde, 0xad, 0xbe, 0xef}},
			},
			header(delegateCall, "simulateAndRevert of the Safe, which delegate calls the target in the Safe's context") +
				"\nResult       reverted\n" +
				"  Error      0xdeadbeef (unknown error)\n" +
				"  Returned   0xdeadbeef\n",
		},
		{
			"more than the balance", call,
			simulate.Result{Method: simulate.MethodCall, Status: simulate.Reverted, ReturnData: ethrpc.Bytes{}, ValueShortfall: big.NewInt(1)},
			header(call, "eth_call from the Safe") + "\nResult       reverted\n" +
				"  Shortfall  1 wei more than the Safe's balance\n" +
				"  Returned   -\n",
		},
	} {
		var stdout bytes.Buffer
		report := &simulateReport{ChainID: 1, SafeBlock: 200, Safe: safe, Result: &test.result}
		printSimulation(&env{stdout: &stdout}, report, test.tx)
		if got := stdout.String(); got != test.want {
			t.Errorf("%s: got\n%s\nwant\n%s", test.name, got, test.want)
		}
	}
}

func TestSimulateErrors(t *testing.T) {
	// A Safe whose singleton isn't one of § 2.1, which isn't known to have
	// simulateAndRevert, and an account with a § 2.1 singleton in slot 0 whose code
	// isn't a SafeProxy, which may answer anything. Neither is known to make a call
	// as the simulation does, and nor is an account without code.
	unknown := stateAccount{code: stateSafeProxy, storage: map[ethrpc.Hash]ethrpc.Hash{{}: {31: 0x01}}}
	notAProxy := stateAccount{code: ethrpc.Bytes{0x60, 0x00, 0x60, 0x00, 0xfd}, storage: map[ethrpc.Hash]ethrpc.Hash{{}: storageWord(stateSingleton)}}
	files := func(rpc string, value int, operation safenet.Operation) (config, request string) {
		config, request = stateFiles(t, rpc)
		data := fmt.Appendf(nil, `{"proposal": {"safeBlock": %d, "transaction": {"chainId": 1, "safe": %q, "to": %q, "value": %d, "operation": %q}}}`,
			stateSafeBlock, stateSafe, stateEOA, value, operation)
		if err := os.WriteFile(request, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return config, request
	}
	for _, test := range []struct {
		name      string
		node      *stateNode
		value     int
		operation safenet.Operation
		code      int
		stderr    string
	}{
		{"unknown singleton", &stateNode{blocks: map[uint64]map[ethrpc.Address]stateAccount{200: {stateSafe: unknown}}}, 0, 1, 1, "its singleton isn't a Safe or SafeL2 singleton of a § 2.1 version"},
		{"not a proxy", &stateNode{blocks: map[uint64]map[ethrpc.Address]stateAccount{200: {stateSafe: notAProxy}}}, 0, 1, 1, "its code isn't that of a SafeProxy"},
		{"no code", &stateNode{blocks: map[uint64]map[ethrpc.Address]stateAccount{200: {}}}, 0, 0, 1, "it has no code"},
		{"not a proxy for a call", &stateNode{blocks: map[uint64]map[ethrpc.Address]stateAccount{200: {stateSafe: notAProxy}}}, 0, 0, 1, "its code isn't that of a SafeProxy"},
		// A node that fails to answer says nothing about the call, so the command fails
		// rather than reporting that it reverted.
		{"balance failing", &stateNode{blocks: map[uint64]map[ethrpc.Address]stateAccount{200: {stateSafe: safeAccount(1, 0)}}, failing: "eth_getBalance"}, 1, 0, 1, "run the command again later"},
		{"call failing", &stateNode{blocks: map[uint64]map[ethrpc.Address]stateAccount{200: {stateSafe: safeAccount(1, 0)}}, failing: "eth_call"}, 0, 0, 1, "run the command again later"},
	} {
		config, request := files(test.node.serve(t), test.value, test.operation)
		code, stdout, stderr := runArbot(t, "-config", config, "simulate", "-request-file", request)
		if code != test.code || stdout != "" || !strings.Contains(stderr, test.stderr) {
			t.Errorf("simulate with %s: got status %d, stdout %q, stderr %q; want %d and stderr containing %q", test.name, code, stdout, stderr, test.code, test.stderr)
		}
	}
	if code, _, stderr := runArbot(t, "simulate"); code != 2 || !strings.Contains(stderr, "-request-file is required") {
		t.Errorf("simulate without a request file: got status %d, stderr %q", code, stderr)
	}
}
