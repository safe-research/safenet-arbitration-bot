package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ens"
	"github.com/safe-research/safenet-arbitration-bot/internal/ipfs"
)

func TestCharterRequestFileErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	config := write("config.json", "{}")
	request := write("request.json", `{"charter": "charter.safenet-gov.eth", "proposal": {"ethereumBlock": 24000000}}`)

	tests := []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"-block", "24000000", "-request-file", request}, 2, "mutually exclusive"},
		{[]string{"-request-file", write("no-block.json", `{"charter": "charter.safenet-gov.eth"}`)}, 1, "no Charter name or Ethereum Mainnet block"},
		{[]string{"-request-file", write("no-charter.json", `{"proposal": {"ethereumBlock": 24000000}}`)}, 1, "no Charter name or Ethereum Mainnet block"},
		{[]string{"-request-file", write("unknown.json", `{"charter": "charter.safenet-gov.eth", "ethereumBlock": 24000000}`)}, 1, `unknown field "ethereumBlock"`},
		{[]string{"-request-file", filepath.Join(dir, "missing.json")}, 1, "no such file"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		args := append([]string{"arbot", "-config", config, "charter"}, test.args...)
		code := Run(t.Context(), args, &stdout, &stderr)
		if code != test.code || stdout.Len() != 0 || !strings.Contains(stderr.String(), test.stderr) {
			t.Errorf("charter %v: got status %d, stdout %q, stderr %q; want %d, no output, and stderr containing %q",
				test.args, code, stdout.String(), stderr.String(), test.code, test.stderr)
		}
	}
}

// ensNode starts an Ethereum Mainnet node whose latest block is latest, and
// whose ENS registry gives name a resolver with the IPFS content hash of
// content. It returns the path of a configuration file for it, and the blocks
// that the ENS records were read at.
func ensNode(t *testing.T, name string, content []byte, latest uint64) (string, *[]string) {
	t.Helper()
	node := ens.Namehash(name)
	resolver := strings.Repeat("11", 20)
	digest := sha256.Sum256(content)
	contenthash := "e30101551220" + hex.EncodeToString(digest[:])
	word := func(n int) string { return fmt.Sprintf("%064x", n) }
	calls := map[string]string{
		hex.EncodeToString(ens.Registry[:]) + ":0178b8bf" + hex.EncodeToString(node[:]): strings.Repeat("0", 24) + resolver,
		resolver + ":bc1c58d1" + hex.EncodeToString(node[:]): word(32) + word(len(contenthash)/2) + contenthash +
			strings.Repeat("0", 64-len(contenthash)%64),
	}

	var blocks []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64            `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "eth_chainId":
			res["result"] = "0x1"
		case "eth_blockNumber":
			res["result"] = fmt.Sprintf("0x%x", latest)
		case "eth_call":
			var call struct {
				To   string `json:"to"`
				Data string `json:"data"`
			}
			var block string
			json.Unmarshal(req.Params[0], &call)
			json.Unmarshal(req.Params[1], &block)
			blocks = append(blocks, block)
			key := strings.ToLower(strings.TrimPrefix(call.To, "0x")) + ":" + strings.TrimPrefix(call.Data, "0x")
			if result, ok := calls[key]; ok {
				res["result"] = "0x" + result
			} else {
				res["error"] = map[string]any{"code": 3, "message": "execution reverted"}
			}
		default:
			res["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(server.Close)

	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, fmt.Appendf(nil, `{"rpcs": {"1": %q}}`, server.URL), 0o644); err != nil {
		t.Fatal(err)
	}
	return config, &blocks
}

func TestCharterVerify(t *testing.T) {
	content := []byte("# Safenet Arbitration Charter\n")
	cid := ipfs.ComputeCID(content).String()
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	charter := write("charter.md", string(content))
	edited := write("edited.md", string(content)+"\n- A rule that isn't in the Charter.\n")
	request := write("request.json", `{"charter": "other.safenet-gov.eth", "proposal": {"ethereumBlock": 24000000}}`)

	tests := []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
		block  string
	}{
		{"block", []string{"-block", "24000000", "-verify", charter}, 0, cid + "\n", "", "0x16e3600"},
		{"latest", []string{"-verify", charter}, 0, cid + "\n", "", "0x16e3601"},
		{"request file", []string{"-request-file", request, "-verify", charter}, 0, cid + "\n", "", "0x16e3600"},
		{"edited", []string{"-block", "24000000", "-verify", edited}, 1, "", "references " + cid + " at block 24000000", "0x16e3600"},
		{"missing", []string{"-verify", filepath.Join(dir, "missing.md")}, 1, "", "no such file", ""},
	}
	for _, test := range tests {
		name := "charter.safenet-gov.eth"
		if slices.Contains(test.args, "-request-file") {
			name = "other.safenet-gov.eth"
		}
		config, blocks := ensNode(t, name, content, 24_000_001)
		var stdout, stderr bytes.Buffer
		args := append([]string{"arbot", "-config", config, "charter"}, test.args...)
		code := Run(t.Context(), args, &stdout, &stderr)
		if code != test.code || stdout.String() != test.stdout || !strings.Contains(stderr.String(), test.stderr) {
			t.Errorf("%s: got status %d, stdout %q, stderr %q; want %d, stdout %q, and stderr containing %q",
				test.name, code, stdout.String(), stderr.String(), test.code, test.stdout, test.stderr)
		}
		for _, block := range *blocks {
			if block != test.block {
				t.Errorf("%s: read ENS records at block %s, want %s", test.name, block, test.block)
			}
		}
	}
}
