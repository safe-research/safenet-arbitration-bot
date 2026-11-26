package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/executions"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

func TestExecutionsErrors(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(dir, "request.json")
	if err := os.WriteFile(request, []byte(`{"proposal": {"transaction": {"chainId": 1}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		args   []string
		code   int
		stderr string
	}{
		{nil, 2, "-request-file is required"},
		{[]string{"-request-file", request, "-blocks", "0"}, 2, "-blocks must be at least 1"},
		{[]string{"-request-file", request, "-all", "-blocks", "5"}, 2, "-blocks doesn't apply with -all"},
		{[]string{"-request-file", request, "-all"}, 1, "no Safe chain ID or block"},
		{[]string{"-request-file", request, "extra"}, 2, "Usage: arbot executions"},
		{[]string{"-request-file", request}, 1, "no Safe chain ID or block"},
		{[]string{"-request-file", filepath.Join(dir, "missing.json")}, 1, "no such file"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		args := append([]string{"arbot", "-config", config, "executions"}, test.args...)
		code := Run(t.Context(), args, &stdout, &stderr)
		if code != test.code || stdout.Len() != 0 || !strings.Contains(stderr.String(), test.stderr) {
			t.Errorf("executions %v: got status %d, stdout %q, stderr %q; want %d, no output, and stderr containing %q",
				test.args, code, stdout.String(), stderr.String(), test.code, test.stderr)
		}
	}
}

func TestExecutionsAllIncomplete(t *testing.T) {
	// A Safe whose nonce is 1, and whose execution neither the node nor the service
	// has, so the history is incomplete.
	safe := "0x5aFE000000000000000000000000000000000001"
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "eth_chainId":
			res["result"] = "0x1"
		case "eth_call":
			res["result"] = "0x" + strings.Repeat("0", 63) + "1"
		case "eth_getLogs":
			res["result"] = []any{}
		default:
			res["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(node.Close)
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/safes/" + safe + "/":
			w.Write([]byte(`{}`))
		case "/api/v1/safes/" + safe + "/multisig-transactions/":
			w.Write([]byte(`{"results": []}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(service.Close)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, fmt.Appendf(nil, `{"rpcs": {"1": %q}, "txServices": {"1": %q}}`, node.URL, service.URL), 0o644); err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(dir, "request.json")
	if err := os.WriteFile(request, fmt.Appendf(nil, `{"proposal": {"safeBlock": 200, "transaction": {"chainId": 1, "safe": %q}}}`, safe), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"arbot", "-config", config, "executions", "-all", "-request-file", request}, &stdout, &stderr); code != 0 {
		t.Fatalf("executions -all: exit status %d\n%s", code, stderr.String())
	}
	if want := "Nonce at block 200: 1 (incomplete)"; !strings.Contains(stdout.String(), want) {
		t.Errorf("executions -all: got %q, want a line %q", stdout.String(), want)
	}
	if want := "warning: the executions found don't account for every nonce below the Safe's nonce 1 at block 200"; !strings.Contains(stderr.String(), want) {
		t.Errorf("executions -all: got stderr %q, want it to contain %q", stderr.String(), want)
	}
}

// listedExecution returns the execution of tx in the transaction hash at block
// as the service lists it, which is also how it has tx by its hash.
func listedExecution(tx *safenet.SafeTransaction, hash ethrpc.Hash, block uint64) map[string]any {
	return map[string]any{
		"safe": tx.Safe, "to": tx.To, "value": tx.Value.String(), "data": tx.Data, "operation": uint8(tx.Operation),
		"safeTxGas": tx.SafeTxGas.String(), "baseGas": tx.BaseGas.String(), "gasPrice": tx.GasPrice.String(),
		"gasToken": tx.GasToken, "refundReceiver": tx.RefundReceiver, "nonce": tx.Nonce.Uint64(),
		"safeTxHash": tx.Hash(), "transactionHash": hash, "blockNumber": block, "isExecuted": true,
	}
}

func TestExecutionsWithoutOldState(t *testing.T) {
	// Three executions of the Safe, at the nonces 0 to 2, in the blocks 100, 150,
	// and 180, and a node that doesn't keep the state of old blocks, so it can't
	// read the Safe's nonce that the calldata and traces need. The service lists
	// the first and the last, and has the second by its hash, or not.
	executionSuccess := solabi.Event("ExecutionSuccess(bytes32,uint256)")
	payment := solabi.Uint(big.NewInt(0))
	var txs []*safenet.SafeTransaction
	var logs []ethrpc.Log
	var listings []map[string]any
	for n, block := range []uint64{100, 150, 180} {
		tx := &safenet.SafeTransaction{
			ChainID: big.NewInt(1), Safe: historySafe, To: historyAlice, Value: big.NewInt(int64(n)), Data: []byte{},
			SafeTxGas: new(big.Int), BaseGas: new(big.Int), GasPrice: new(big.Int), Nonce: big.NewInt(int64(n)),
		}
		txs = append(txs, tx)
		logs = append(logs, ethrpc.Log{
			Address: historySafe, Topics: []ethrpc.Hash{executionSuccess, tx.Hash()}, Data: payment[:],
			BlockNumber: ethrpc.BlockNumber(block), TransactionHash: hashOf(byte(n + 1)),
		})
		listings = append(listings, listedExecution(tx, hashOf(byte(n+1)), block))
	}

	for _, test := range []struct {
		name string
		args []string
		// stateFrom is the first block whose state the node has, and failing the block
		// of the nonce that it fails to read.
		stateFrom, failing uint64
	}{
		// The nonce is read at the Safe block, and with -all, at the block of the
		// execution that the service doesn't list.
		{"executions", nil, 201, 200},
		{"executions -all", []string{"-all"}, 200, 150},
	} {
		for _, known := range []bool{true, false} {
			service := &historyService{executions: []map[string]any{listings[0], listings[2]}, transactions: map[ethrpc.Hash]map[string]any{}}
			for i, tx := range txs {
				if known || i != 1 {
					service.transactions[tx.Hash()] = listings[i]
				}
			}
			chain := &historyChain{logs: logs, nonce: big.NewInt(3), stateFrom: test.stateFrom}
			config, request := historyFiles(t, chain.serve(t), service.serve(t))
			args := append([]string{"-config", config, "executions", "-json", "-request-file", request}, test.args...)
			code, stdout, stderr := runArbot(t, args...)
			warning := fmt.Sprintf("warning: reading the nonce of %s at block %d: eth_call: JSON-RPC error -32000: missing trie node", historySafe, test.failing)
			if !known {
				// The second transaction is in no source, and the calldata might have had it.
				if want := "the calldata and traces need the Safe's nonce, and no other source has the transaction"; code != 1 || stdout != "" || !strings.Contains(stderr, want) {
					t.Errorf("%s without the second transaction: got status %d, stdout %q, stderr %q; want 1 and an error containing %q", test.name, code, stdout, stderr, want)
				}
				continue
			}
			var got struct {
				Executions []struct {
					Source      string                   `json:"source"`
					Transaction *safenet.SafeTransaction `json:"transaction"`
				} `json:"executions"`
			}
			if err := json.Unmarshal([]byte(stdout), &got); code != 0 || err != nil {
				t.Fatalf("%s: got status %d, %v\n%s", test.name, code, err, stderr)
			}
			if len(got.Executions) != 3 || got.Executions[1].Source != "service" || got.Executions[1].Transaction == nil || got.Executions[1].Transaction.Hash() != txs[1].Hash() {
				t.Errorf("%s: got executions %+v, want three, the second from the service", test.name, got.Executions)
			}
			if n := strings.Count(stderr, "warning: "); n != 1 || !strings.Contains(stderr, warning) {
				t.Errorf("%s: got stderr %q, want one warning containing %q", test.name, stderr, warning)
			}
		}
	}
}

func TestNonceRanges(t *testing.T) {
	for _, test := range []struct {
		ranges []executions.NonceRange
		want   string
	}{
		{[]executions.NonceRange{}, "none"},
		{[]executions.NonceRange{{From: 1, To: 2}, {From: 4, To: 4}}, "1-2, 4"},
	} {
		if got := nonceRanges(test.ranges); got != test.want {
			t.Errorf("nonceRanges(%v): got %q, want %q", test.ranges, got, test.want)
		}
	}
}

func TestWriteHistoryCreation(t *testing.T) {
	factory := ethrpc.MustParseAddress("0x4e1DCf7AD4e460CfD30791CCC4F9c8a4f820ec67")
	tx := ethrpc.Hash{0: 0xaa, 31: 9}
	for _, test := range []struct {
		creation *executions.Creation
		want     string
	}{
		{nil, `(?m)^Creation: unknown$`},
		{&executions.Creation{Block: 80}, `(?m)^Created in block 80, by a transaction that arbot doesn't bind to the account$`},
		{&executions.Creation{Block: 80, TxHash: &tx}, `(?m)^Created in block 80, transaction ` + tx.String() + ` \(contract creation\)$`},
		{&executions.Creation{Block: 80, TxHash: &tx, Factory: &factory}, `(?m)^Created in block 80, transaction ` + tx.String() + ` \(ProxyCreation event of ` + factory.String() + `\)$`},
	} {
		var stdout bytes.Buffer
		report := &executionsReport{ChainID: 1, ToBlock: 200, History: &historyReport{Nonce: big.NewInt(0), Creation: test.creation}}
		writeHistory(&env{stdout: &stdout}, report)
		if !regexp.MustCompile(test.want).Match(stdout.Bytes()) {
			t.Errorf("writeHistory: output has no line matching %q:\n%s", test.want, stdout.String())
		}
	}
}
