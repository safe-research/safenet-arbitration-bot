package cli

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// stateSafeBlock is the Safe block of the requests of the tests of a stateNode.
const stateSafeBlock = 200

var (
	stateSafe  = ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	stateEOA   = ethrpc.MustParseAddress("0xe0a0000000000000000000000000000000000001")
	stateToken = ethrpc.MustParseAddress("0x7000000000000000000000000000000000000001")

	// stateSafeProxy is the code of the SafeProxy of Safe 1.3.0.
	stateSafeProxy = func() ethrpc.Bytes {
		b, _ := hex.DecodeString("608060405273ffffffffffffffffffffffffffffffffffffffff600054167fa619486e0000000000000000000000000000000000000000000000000000000060003514156050578060005260206000f35b3660008037600080366000845af43d6000803e60008114156070573d6000fd5b3d6000f3fea2646970667358221220d1429297349653a4918076d650332de1a1068c5f3e07c5c82360c277770b955264736f6c63430007060033")
		return b
	}()
)

// stateAccount is an account of a stateNode at a block.
type stateAccount struct {
	code    ethrpc.Bytes
	storage map[ethrpc.Hash]ethrpc.Hash
	balance int64
	nonce   uint64
	// calls are what eth_call to the account returns for each selector. A call with
	// another selector reverts.
	calls map[string]ethrpc.Bytes
}

// stateNode is a fake node for Ethereum Mainnet with the accounts of each
// block, which fails every request but eth_chainId with HTTP status 500 if
// overloaded, as an overloaded node does, or only the requests of the method or
// eth_call selector failing.
type stateNode struct {
	blocks     map[uint64]map[ethrpc.Address]stateAccount
	overloaded bool
	failing    string
}

func (n *stateNode) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64            `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if req.Method == "eth_chainId" {
			res["result"] = "0x1"
			json.NewEncoder(w).Encode(res)
			return
		}
		if n.overloaded {
			http.Error(w, "overloaded", http.StatusInternalServerError)
			return
		}
		var address ethrpc.Address
		var call ethrpc.CallRequest
		if req.Method == "eth_call" {
			json.Unmarshal(req.Params[0], &call)
			address = call.To
		} else {
			json.Unmarshal(req.Params[0], &address)
		}
		var block ethrpc.BlockNumber
		json.Unmarshal(req.Params[len(req.Params)-1], &block)
		accounts, ok := n.blocks[uint64(block)]
		if !ok {
			res["error"] = map[string]any{"code": -32000, "message": fmt.Sprintf("header not found: block %d", block)}
			json.NewEncoder(w).Encode(res)
			return
		}
		a := accounts[address]
		if req.Method == n.failing || req.Method == "eth_call" && hex.EncodeToString(call.Data[:min(4, len(call.Data))]) == n.failing {
			http.Error(w, "overloaded", http.StatusInternalServerError)
			return
		}
		switch req.Method {
		case "eth_getCode":
			res["result"] = append(ethrpc.Bytes{}, a.code...)
		case "eth_getStorageAt":
			var slot ethrpc.Hash
			json.Unmarshal(req.Params[1], &slot)
			res["result"] = a.storage[slot]
		case "eth_getBalance":
			res["result"] = fmt.Sprintf("0x%x", a.balance)
		case "eth_getTransactionCount":
			res["result"] = ethrpc.Quantity(a.nonce)
		case "eth_call":
			if len(a.code) == 0 {
				res["result"] = "0x"
			} else if result, ok := a.calls[hex.EncodeToString(call.Data[:min(4, len(call.Data))])]; ok {
				res["result"] = result
			} else {
				res["error"] = map[string]any{"code": 3, "message": "execution reverted"}
			}
		default:
			res["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// stateFiles writes a configuration file that reads Mainnet from the node at
// rpc, and a request of stateSafe whose Safe block is stateSafeBlock, and
// returns their paths.
func stateFiles(t *testing.T, rpc string) (config, request string) {
	t.Helper()
	dir := t.TempDir()
	config = filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, fmt.Appendf(nil, `{"rpcs": {"1": %q}}`, rpc), 0o644); err != nil {
		t.Fatal(err)
	}
	request = filepath.Join(dir, "request.json")
	data := fmt.Appendf(nil, `{"proposal": {"safeBlock": %d, "transaction": {"chainId": 1, "safe": %q}}}`, stateSafeBlock, stateSafe)
	if err := os.WriteFile(request, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return config, request
}

// runArbot runs arbot with args, and returns its status and output.
func runArbot(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Run(t.Context(), append([]string{"arbot"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// compactJSON returns data without insignificant spaces.
func compactJSON(data []byte) string {
	var b bytes.Buffer
	json.Compact(&b, data)
	return b.String()
}

func TestAccount(t *testing.T) {
	spender := ethrpc.Address{0: 0x5e, 19: 1}
	// A token, as of blocks 150 and 200, whose name is a string, and whose symbol
	// call reverts.
	token := func(balance uint64) stateAccount {
		return stateAccount{code: ethrpc.Bytes{0x60, 0x00}, calls: map[string]ethrpc.Bytes{
			"313ce567": solabi.Encode(uint64(6)),         // decimals()
			"70a08231": solabi.Encode(balance),           // balanceOf(address)
			"06fdde03": solabi.Encode("Token"),           // name()
			"18160ddd": solabi.Encode(uint64(1_000_000)), // totalSupply()
			"dd62ed3e": solabi.Encode(balance / 2),       // allowance(address,address)
		}}
	}
	node := &stateNode{blocks: map[uint64]map[ethrpc.Address]stateAccount{
		0:   {},
		150: {stateToken: token(10)},
		200: {stateToken: token(40), stateEOA: {balance: 7, nonce: 3}, stateSafe: {code: stateSafeProxy}},
	}}
	config, request := stateFiles(t, node.serve(t))
	type output struct {
		Block    uint64          `json:"block"`
		Address  ethrpc.Address  `json:"address"`
		CodeSize int             `json:"codeSize"`
		Self     bool            `json:"self"`
		Balance  *big.Int        `json:"balance"`
		Nonce    uint64          `json:"nonce"`
		ERC20    json.RawMessage `json:"erc20"`
	}
	run := func(args ...string) output {
		t.Helper()
		code, stdout, stderr := runArbot(t, append([]string{"-config", config, "account", "-json", "-request-file", request}, args...)...)
		if code != 0 {
			t.Fatalf("account %v: got status %d, stderr %q", args, code, stderr)
		}
		var got output
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("decoding account %v: %v", args, err)
		}
		return got
	}

	got := run(stateToken.String())
	if want := `{"name":"Token","symbol":"","decimals":6,"totalSupply":1000000,"safeBalance":40}`; got.Block != 200 || compactJSON(got.ERC20) != want {
		t.Errorf("account of the token: got block %d and token %s, want block 200 and %s", got.Block, got.ERC20, want)
	}
	got = run("-block", "150", "-spender", spender.String(), stateToken.String())
	if want := `{"name":"Token","symbol":"","decimals":6,"totalSupply":1000000,"safeBalance":10,"spender":"` + strings.ToLower(spender.String()) + `","safeAllowance":5}`; got.Block != 150 || compactJSON(got.ERC20) != want {
		t.Errorf("account -block 150 -spender of the token: got block %d and token %s, want block 150 and %s", got.Block, got.ERC20, want)
	}
	if got := run(stateEOA.String()); got.CodeSize != 0 || got.Balance.Int64() != 7 || got.Nonce != 3 || string(got.ERC20) != "null" {
		t.Errorf("account of an account without code: got %+v, want balance 7, nonce 3, and no token", got)
	}
	if got := run(stateSafe.String()); !got.Self || got.CodeSize == 0 || string(got.ERC20) != "null" {
		t.Errorf("account of the Safe: got %+v, want the Safe itself, which isn't a token", got)
	}
	// Block 0 is read like any other, before the token had code.
	if got := run("-block", "0", stateToken.String()); got.Block != 0 || got.CodeSize != 0 || string(got.ERC20) != "null" {
		t.Errorf("account -block 0: got %+v, want block 0, without code or a token", got)
	}
	// The Safe block itself can be read, and the block after it can't.
	if got := run("-block", "200", stateToken.String()); got.Block != 200 {
		t.Errorf("account -block 200: got block %d, want 200", got.Block)
	}
	code, _, stderr := runArbot(t, "-config", config, "account", "-block", "201", "-request-file", request, stateToken.String())
	if code != 2 || !strings.Contains(stderr, "-block 201 is after the request's Safe block 200") {
		t.Errorf("account -block 201: got status %d, stderr %q; want a usage error", code, stderr)
	}
}

func TestAccountErrors(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(dir, "request.json")
	if err := os.WriteFile(request, []byte(`{"proposal": {"transaction": {"chainId": 1}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	address := "0x000000000000000000000000000000000000dEaD"

	tests := []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{address}, 2, "-request-file is required"},
		{[]string{"-request-file", request}, 2, "Usage: arbot account"},
		{[]string{"-request-file", request, address, "extra"}, 2, "Usage: arbot account"},
		{[]string{"-request-file", request, "0x12"}, 2, `address "0x12"`},
		{[]string{"-request-file", request, "-spender", "0x12", address}, 2, `-spender "0x12"`},
		{[]string{"-request-file", request, address}, 1, "no Safe chain ID or block"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		args := append([]string{"arbot", "-config", config, "account"}, test.args...)
		code := Run(t.Context(), args, &stdout, &stderr)
		if code != test.code || stdout.Len() != 0 || !strings.Contains(stderr.String(), test.stderr) {
			t.Errorf("account %v: got status %d, stdout %q, stderr %q; want %d, no output, and stderr containing %q",
				test.args, code, stdout.String(), stderr.String(), test.code, test.stderr)
		}
	}
}

func TestAccountTransientError(t *testing.T) {
	// A node that fails to answer says nothing about the account, so the command
	// fails rather than reporting an account without code.
	node := &stateNode{overloaded: true}
	config, request := stateFiles(t, node.serve(t))
	code, stdout, stderr := runArbot(t, "-config", config, "account", "-request-file", request, stateToken.String())
	if code != 1 || stdout != "" || !strings.Contains(stderr, "run the command again later") {
		t.Errorf("account: got status %d, stdout %q, stderr %q; want 1 and a hint to run it again", code, stdout, stderr)
	}
	// So does a node that fails any one of the reads of a token.
	token := stateAccount{code: ethrpc.Bytes{0x60, 0x00}, calls: map[string]ethrpc.Bytes{
		"313ce567": solabi.Encode(uint64(6)), "70a08231": solabi.Encode(uint64(1)), "06fdde03": solabi.Encode("Token"),
		"95d89b41": solabi.Encode("TKN"), "18160ddd": solabi.Encode(uint64(1)), "dd62ed3e": solabi.Encode(uint64(1)),
	}}
	for _, failing := range []string{
		"eth_getBalance", "eth_getTransactionCount", "313ce567", "70a08231", "06fdde03", "95d89b41", "18160ddd", "dd62ed3e",
	} {
		node := &stateNode{blocks: map[uint64]map[ethrpc.Address]stateAccount{200: {stateToken: token}}, failing: failing}
		config, request := stateFiles(t, node.serve(t))
		code, stdout, stderr := runArbot(t, "-config", config, "account", "-spender", stateEOA.String(), "-request-file", request, stateToken.String())
		if code != 1 || stdout != "" || !strings.Contains(stderr, "run the command again later") {
			t.Errorf("account with %s failing: got status %d, stdout %q, stderr %q; want 1 and a hint to run it again", failing, code, stdout, stderr)
		}
	}
}
