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
