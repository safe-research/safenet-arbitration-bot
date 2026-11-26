package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"regexp"
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
		if err := printSimulation(&env{stdout: &stdout}, report, test.tx); err != nil {
			t.Fatal(err)
		}
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

// TestSimulateTraceShortfall checks that -trace doesn't trace a call that sends
// more than the Safe's balance, which the simulation doesn't run, and which
// nodes reject before tracing.
func TestSimulateTraceShortfall(t *testing.T) {
	node := &stateNode{blocks: map[uint64]map[ethrpc.Address]stateAccount{200: {stateSafe: safeAccount(1, 0)}}}
	config, request := stateFiles(t, node.serve(t))
	data := fmt.Appendf(nil, `{"proposal": {"safeBlock": %d, "transaction": {"chainId": 1, "safe": %q, "to": %q, "value": 1, "operation": "CALL"}}}`,
		stateSafeBlock, stateSafe, stateEOA)
	if err := os.WriteFile(request, data, 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runArbot(t, "-config", config, "simulate", "-trace", "-json", "-request-file", request)
	var got simulateReport
	if err := json.Unmarshal([]byte(stdout), &got); code != 0 || err != nil || got.Result.ValueShortfall == nil || got.Trace == nil ||
		got.Trace.Available || got.Trace.Reason != "not traced: the call sends more than the Safe's balance" {
		t.Errorf("simulate -trace of a call above the balance: got status %d, %s, stderr %q; want a shortfall and no trace", code, stdout, stderr)
	}
}

func TestPrintTrace(t *testing.T) {
	safe := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	to := ethrpc.MustParseAddress("0x40A2aCCbd92BCA938b02010E17A5b8929b49130D")
	created := ethrpc.Address{0: 0xc1}
	tx := &safenet.SafeTransaction{To: to, Value: new(big.Int), Operation: safenet.OperationDelegateCall}
	method := simulate.MethodSimulateAndRevert
	run := func(trace *simulate.Trace) string {
		var stdout bytes.Buffer
		result := &simulate.Result{Method: method, Status: simulate.Success, ReturnData: ethrpc.Bytes{}}
		if err := printSimulation(&env{stdout: &stdout}, &simulateReport{ChainID: 1, SafeBlock: 200, Safe: safe, Result: result, Trace: trace}, tx); err != nil {
			t.Fatal(err)
		}
		return stdout.String()
	}

	// The node's reason is quoted.
	got := run(&simulate.Trace{Reason: "debug_traceCall: HTTP 401 Unauthorized: \"\x1b[31mno\"", Calls: []simulate.Frame{}})
	if want := "\nTrace unavailable: \"debug_traceCall: HTTP 401 Unauthorized: \\\"\\x1b[31mno\\\"\"\n"; !strings.HasSuffix(got, want) {
		t.Errorf("unavailable trace: got\n%s\nwant it to end with\n%s", got, want)
	}

	// A frame's type and error come from the node, and its revert reason from a
	// contract, so they are escaped and shortened, rather than widening every row.
	long := strings.Repeat("x", 200)
	got = run(&simulate.Trace{
		Available: true,
		Calls: []simulate.Frame{
			{Depth: 0, Type: "CALL", From: ethrpc.Address{}, To: &safe, Value: new(big.Int), Selector: ethrpc.Bytes{0xb4, 0xfa, 0xba, 0x09}, InputSize: 100, Error: "execution reverted"},
			{Depth: 1, Type: "DELEGATECALL", From: safe, To: &to, Selector: ethrpc.Bytes{0x8d, 0x80, 0xff, 0x0a}, InputSize: 68, Error: "execution reverted", RevertReason: "GS\x1b013"},
			{Depth: 2, Type: "CREATE", From: safe, InputSize: 2, InitCodeHash: &ethrpc.Hash{0: 0xaa}, Error: long},
			{Depth: 2, Type: "\x1b" + long, From: safe, To: &created, Value: big.NewInt(5), InputSize: 0},
			{Depth: 2, Type: "CREATE2", From: safe, To: &created, InputSize: 3, InitCodeHash: &ethrpc.Hash{0: 0xbb}},
		},
	})
	short := strings.Repeat("x", 100) + "..."
	for _, line := range []string{
		`Trace \(debug_traceCall, callTracer\)`,
		`# +DEPTH +TYPE +FROM +TO +VALUE +SELECTOR +INPUT +ERROR`,
		`1 +0 +CALL +` + ethrpc.Address{}.String() + ` +` + safe.String() + ` +0 +0xb4faba09 +100 bytes +execution reverted`,
		`2 +1 +DELEGATECALL +` + safe.String() + ` +` + to.String() + ` +- +0x8d80ff0a +68 bytes +execution reverted: "GS\\x1b013"`,
		`3 +2 +CREATE +` + safe.String() + ` +- +- +- +2 bytes +` + short,
		`4 +2 +\\x1b` + strings.Repeat("x", 99) + `\.\.\. +` + safe.String() + ` +` + created.String() + ` +5 +- +0 bytes +-`,
		`5 +2 +CREATE2 +` + safe.String() + ` +` + created.String() + ` +- +- +3 bytes +-`,
		`Created contracts`,
		`DEPTH +TYPE +ADDRESS +CREATOR +INIT CODE +ERROR`,
		`2 +CREATE +- +` + safe.String() + ` +2 bytes, hash 0xaa0{62} +` + short,
		`2 +CREATE2 +` + created.String() + ` +` + safe.String() + ` +3 bytes, hash 0xbb0{62} +-`,
	} {
		if !regexp.MustCompile(`(?m)^` + line + `$`).MatchString(got) {
			t.Errorf("trace: output has no line matching %q:\n%s", line, got)
		}
	}

	// A creation in a call that failed is rolled back, even if the call that made
	// it caught the failure, but not by the frames of simulateAndRevert, which
	// always revert, while the top frame of a call is the simulated call.
	frames := []simulate.Frame{
		{Depth: 0, Type: "CALL", To: &safe, Error: "execution reverted"},
		{Depth: 1, Type: "DELEGATECALL", From: safe, To: &to, Error: "execution reverted"},
		{Depth: 2, Type: "DELEGATECALL", From: safe, To: &to},
		{Depth: 3, Type: "CALL", From: safe, To: &created, Error: "execution reverted"},
		{Depth: 4, Type: "CREATE", From: created, To: &created, InputSize: 1, InitCodeHash: &ethrpc.Hash{0: 0xcc}},
		{Depth: 3, Type: "CREATE", From: safe, To: &created, InputSize: 1, InitCodeHash: &ethrpc.Hash{0: 0xdd}},
	}
	for m, want := range map[string][2]string{
		simulate.MethodSimulateAndRevert: {"rolled back: #4 failed", "-"},
		simulate.MethodCall:              {"rolled back: #4 failed", "rolled back: #2 failed"},
	} {
		method = m
		got = run(&simulate.Trace{Available: true, Calls: frames})
		for i, hash := range []string{"cc", "dd"} {
			if line := `CREATE +` + created.String() + ` +\S+ +1 bytes, hash 0x` + hash + `0{62} +` + want[i]; !regexp.MustCompile(`(?m)^\d +` + line + `$`).MatchString(got) {
				t.Errorf("%s trace: output has no line matching %q:\n%s", m, line, got)
			}
		}
	}
	method = simulate.MethodSimulateAndRevert

	// A trace without creations says so.
	got = run(&simulate.Trace{Available: true, Calls: []simulate.Frame{{Type: "CALL", To: &safe}}})
	if !strings.HasSuffix(got, "\nCreated contracts\n\n  none\n") {
		t.Errorf("trace without creations: got\n%s", got)
	}
}
