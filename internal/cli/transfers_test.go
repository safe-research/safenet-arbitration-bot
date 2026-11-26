package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

func TestTransfersErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	config := write("config.json", "{}")
	request := write("request.json", `{"proposal": {"safeBlock": 24000000, "transaction": {"chainId": 1}}}`)

	tests := []struct {
		args   []string
		code   int
		stderr string
	}{
		{nil, 2, "-request-file is required"},
		{[]string{"-request-file", request, "-blocks", "0"}, 2, "-blocks must be at least 1"},
		{[]string{"-request-file", request, "-to", "0x1234"}, 2, `-to "0x1234"`},
		{[]string{"-request-file", request, "-in", "-from", "0x1234"}, 2, `-from "0x1234"`},
		{[]string{"-request-file", request, "-from", "0x000000000000000000000000000000000000dEaD"}, 2, "-from only applies to transfers into the Safe"},
		{[]string{"-request-file", request, "-in", "-to", "0x000000000000000000000000000000000000dEaD"}, 2, "-to only applies to transfers out of the Safe"},
		{[]string{"-request-file", request, "-all", "-blocks", "5"}, 2, "-blocks doesn't apply with -all"},
		{[]string{"-request-file", request, "extra"}, 2, "Usage: arbot transfers"},
		{[]string{"-request-file", write("no-block.json", `{"proposal": {"transaction": {"chainId": 1}}}`)}, 1, "no Safe chain ID or block"},
		{[]string{"-request-file", write("no-chain.json", `{"proposal": {"safeBlock": 24000000}}`)}, 1, "no Safe chain ID or block"},
		{[]string{"-request-file", filepath.Join(dir, "missing.json")}, 1, "no such file"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		args := append([]string{"arbot", "-config", config, "transfers"}, test.args...)
		code := Run(t.Context(), args, &stdout, &stderr)
		if code != test.code || stdout.Len() != 0 || !strings.Contains(stderr.String(), test.stderr) {
			t.Errorf("transfers %v: got status %d, stdout %q, stderr %q; want %d, no output, and stderr containing %q",
				test.args, code, stdout.String(), stderr.String(), test.code, test.stderr)
		}
	}
}

var (
	historySafe  = ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	historyAlice = ethrpc.MustParseAddress("0xa11ce00000000000000000000000000000000003")
	historyBob   = ethrpc.MustParseAddress("0xb0b0000000000000000000000000000000000004")
	historyToken = ethrpc.MustParseAddress("0x7000000000000000000000000000000000000001")

	erc20Transfer = solabi.Event("Transfer(address,address,uint256)")
)

// historyChain is a fake node for Ethereum Mainnet with logs.
type historyChain struct {
	logs []ethrpc.Log
	// nonce is the Safe's nonce, if set, which the node has from the block
	// stateFrom on: it answers eth_call at an earlier block as a node does that
	// doesn't keep the state of old blocks. Any other call fails the test.
	nonce     *big.Int
	stateFrom uint64
	// failingBlocks are the blocks whose logs the node fails to serve with HTTP
	// status 500, as an overloaded node does.
	failingBlocks []uint64

	mu sync.Mutex
	// ranges are the block ranges of the eth_getLogs requests, in order.
	ranges [][2]uint64
}

func (c *historyChain) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64            `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "eth_chainId":
			res["result"] = "0x1"
		case "eth_getLogs":
			var filter ethrpc.LogFilter
			json.Unmarshal(req.Params[0], &filter)
			c.mu.Lock()
			c.ranges = append(c.ranges, [2]uint64{uint64(filter.FromBlock), uint64(filter.ToBlock)})
			c.mu.Unlock()
			if slices.ContainsFunc(c.failingBlocks, func(b uint64) bool { return b >= uint64(filter.FromBlock) && b <= uint64(filter.ToBlock) }) {
				http.Error(w, "overloaded", http.StatusInternalServerError)
				return
			}
			logs := []ethrpc.Log{}
			for _, log := range c.logs {
				if log.BlockNumber < filter.FromBlock || log.BlockNumber > filter.ToBlock ||
					len(filter.Addresses) > 0 && !slices.Contains(filter.Addresses, log.Address) {
					continue
				}
				matches := true
				for i, topics := range filter.Topics {
					if topics != nil && (i >= len(log.Topics) || !slices.Contains(topics, log.Topics[i])) {
						matches = false
					}
				}
				if matches {
					logs = append(logs, log)
				}
			}
			res["result"] = logs
		case "eth_call":
			var call ethrpc.CallRequest
			var block ethrpc.BlockNumber
			json.Unmarshal(req.Params[0], &call)
			json.Unmarshal(req.Params[1], &block)
			switch {
			case c.nonce == nil:
				res["error"] = map[string]any{"code": -32601, "message": "method not found"}
			case call.To != historySafe || string(call.Data) != string(solabi.Call(solabi.Selector("nonce()"))):
				t.Errorf("eth_call to %s with data %s, want the Safe's nonce()", call.To, call.Data)
				res["error"] = map[string]any{"code": 3, "message": "execution reverted"}
			case uint64(block) < c.stateFrom:
				res["error"] = map[string]any{"code": -32000, "message": "missing trie node 7b2ad5 (path ) state 0x7b2ad5 is not available"}
			default:
				word := solabi.Uint(c.nonce)
				res["result"] = ethrpc.Bytes(word[:])
			}
		default:
			res["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// historyService is a fake Safe Transaction Service for Ethereum Mainnet that
// knows the Safe, and lists its executions and transfers, and has the
// transactions by Safe transaction hash, or fails with HTTP status 500 if
// failing. Like the service, it lists nothing for a Safe that it doesn't know,
// which is any Safe if unknown. It filters the transfers by kind, sender, and
// recipient, but not by block, as a service that misfilters would, and keeps
// the queries for them.
type historyService struct {
	executions   []map[string]any
	transactions map[ethrpc.Hash]map[string]any
	transfers    []map[string]any
	failing      bool
	unknown      bool

	mu      sync.Mutex
	queries []url.Values
}

func (s *historyService) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.failing {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		if id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/multisig-transactions/"); ok {
			hash, err := ethrpc.ParseHash(strings.TrimSuffix(id, "/"))
			if tx, ok := s.transactions[hash]; ok && err == nil {
				json.NewEncoder(w).Encode(tx)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		path, _ := strings.CutPrefix(r.URL.Path, "/api/v1/safes/")
		address, endpoint, _ := strings.Cut(path, "/")
		known := address == historySafe.String() && !s.unknown
		results := []map[string]any{}
		switch {
		case endpoint == "" && known:
			json.NewEncoder(w).Encode(map[string]any{"address": historySafe})
			return
		case endpoint == "transfers/" && known:
			q := r.URL.Query()
			s.mu.Lock()
			s.queries = append(s.queries, q)
			s.mu.Unlock()
			types := map[string]string{"erc20": "ERC20_TRANSFER", "ether": "ETHER_TRANSFER"}
			for _, x := range s.transfers {
				if (q.Get("erc20") == "true" && x["type"] == types["erc20"] || q.Get("ether") == "true" && x["type"] == types["ether"]) &&
					(q.Get("_from") == "" || q.Get("_from") == fmt.Sprint(x["from"])) && (q.Get("to") == "" || q.Get("to") == fmt.Sprint(x["to"])) {
					results = append(results, x)
				}
			}
		case endpoint == "multisig-transactions/" && known:
			results = append(results, s.executions...)
		case endpoint != "transfers/" && endpoint != "multisig-transactions/":
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"count": len(results), "results": results})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// historyFiles writes a configuration file that reads Mainnet from the node at
// rpc, with the service at service, and a request of the Safe whose Safe block
// is 200, and returns their paths.
func historyFiles(t *testing.T, rpc, service string) (config, request string) {
	t.Helper()
	dir := t.TempDir()
	config = filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, fmt.Appendf(nil, `{"rpcs": {"1": %q}, "txServices": {"1": %q}}`, rpc, service), 0o644); err != nil {
		t.Fatal(err)
	}
	request = filepath.Join(dir, "request.json")
	if err := os.WriteFile(request, fmt.Appendf(nil, `{"proposal": {"safeBlock": 200, "transaction": {"chainId": 1, "safe": %q}}}`, historySafe), 0o644); err != nil {
		t.Fatal(err)
	}
	return config, request
}

func hashOf(n byte) ethrpc.Hash { return ethrpc.Hash{0: 0xaa, 31: n} }

// matchLines reports each of the patterns that no line of the output of command
// matches.
func matchLines(t *testing.T, command string, output []byte, patterns ...string) {
	t.Helper()
	for _, want := range patterns {
		if !regexp.MustCompile(`(?m)` + want).Match(output) {
			t.Errorf("%s: output has no line matching %q:\n%s", command, want, output)
		}
	}
}

// transferLog returns the Transfer log of the token of value from from to to,
// in the transaction hash at block.
func transferLog(from, to ethrpc.Address, value int64, hash ethrpc.Hash, block, index uint64) ethrpc.Log {
	amount := solabi.Uint(big.NewInt(value))
	return ethrpc.Log{
		Address: historyToken, Topics: []ethrpc.Hash{erc20Transfer, solabi.Address(from), solabi.Address(to)}, Data: amount[:],
		BlockNumber: ethrpc.BlockNumber(block), TransactionHash: hash, LogIndex: ethrpc.Quantity(index),
	}
}

// listedTransfer returns an ERC-20 transfer of the token as the service lists
// it.
func listedTransfer(from, to ethrpc.Address, value string, hash ethrpc.Hash, block uint64) map[string]any {
	return map[string]any{
		"type": "ERC20_TRANSFER", "blockNumber": block, "transactionHash": hash, "tokenAddress": historyToken,
		"from": from, "to": to, "value": value,
	}
}

func TestTransfersAll(t *testing.T) {
	// The service lists a transfer out of the Safe that the logs have, one that
	// they don't, one after the Safe block, and one into the Safe.
	chain := &historyChain{logs: []ethrpc.Log{
		transferLog(historySafe, historyAlice, 5, hashOf(1), 100, 0),
		transferLog(historyAlice, historySafe, 6, hashOf(3), 120, 0),
		transferLog(historySafe, historyAlice, 7, hashOf(4), 300, 0),
	}}
	service := &historyService{transfers: []map[string]any{
		listedTransfer(historySafe, historyAlice, "5", hashOf(1), 100),
		listedTransfer(historySafe, historyBob, "8", hashOf(2), 110),
		listedTransfer(historySafe, historyAlice, "7", hashOf(4), 300),
		listedTransfer(historyAlice, historySafe, "6", hashOf(3), 120),
	}}
	serviceURL := service.serve(t)
	config, request := historyFiles(t, chain.serve(t), serviceURL)

	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"arbot", "-config", config, "transfers", "-all", "-json", "-request-file", request}, &stdout, &stderr); code != 0 {
		t.Fatalf("transfers -all: exit status %d\n%s", code, stderr.String())
	}
	var got struct {
		FromBlock *uint64 `json:"fromBlock"`
		ToBlock   uint64  `json:"toBlock"`
		Transfers []struct {
			Block uint64         `json:"block"`
			To    ethrpc.Address `json:"to"`
			Value *big.Int       `json:"value"`
		} `json:"transfers"`
		Service *struct {
			URL        string `json:"url"`
			ObservedAt string `json:"observedAt"`
			Listed     int    `json:"listed"`
			Blocks     int    `json:"blocks"`
			Unmatched  int    `json:"unmatched"`
		} `json:"service"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("transfers -all: %v\n%s", err, stdout.String())
	}
	// It reads the blocks that the service names rather than a range, so it has no
	// first block.
	if got.FromBlock != nil || got.ToBlock != 200 || len(got.Transfers) != 1 || got.Transfers[0].Block != 100 || got.Transfers[0].Value.Int64() != 5 {
		t.Errorf("transfers -all: got %s, want the transfer to alice in block 100, up to block 200", stdout.String())
	}
	if s := got.Service; s == nil || s.URL != serviceURL || s.ObservedAt == "" || s.Listed != 2 || s.Blocks != 2 || s.Unmatched != 1 {
		t.Errorf("transfers -all: got service %+v, want 2 listed in 2 blocks, 1 unmatched", s)
	}
	if want := "lists a transfer of 8 from " + historySafe.String() + " to " + historyBob.String(); !strings.Contains(stderr.String(), want) {
		t.Errorf("transfers -all: got stderr %q, want a warning containing %q", stderr.String(), want)
	}
	// The block without the transfer is read again before it is unmatched, and no
	// other blocks are read.
	if want := [][2]uint64{{100, 100}, {110, 110}, {110, 110}}; !slices.Equal(chain.ranges, want) {
		t.Errorf("transfers -all: read the logs of %v, want %v", chain.ranges, want)
	}
	// The service is asked for the ERC-20 transfers out of the Safe up to its
	// block.
	want := url.Values{"erc20": {"true"}, "_from": {historySafe.String()}, "block_number__lt": {"201"}, "limit": {"200"}}
	if len(service.queries) != 1 || !reflect.DeepEqual(service.queries[0], want) {
		t.Errorf("transfers -all: got the queries %v, want %v", service.queries, want)
	}

	// Into the Safe, it asks for the ERC-20 and native transfers to it.
	service.queries = nil
	stdout.Reset()
	if code := Run(t.Context(), []string{"arbot", "-config", config, "transfers", "-all", "-in", "-json", "-request-file", request}, &stdout, &stderr); code != 0 {
		t.Fatalf("transfers -all -in: exit status %d\n%s", code, stderr.String())
	}
	got.Transfers = nil
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("transfers -all -in: %v\n%s", err, stdout.String())
	}
	if len(got.Transfers) != 1 || got.Transfers[0].Block != 120 || got.Service == nil || got.Service.Listed != 1 || got.Service.Unmatched != 0 {
		t.Errorf("transfers -all -in: got %s, want the transfer from alice in block 120", stdout.String())
	}
	for _, q := range service.queries {
		if q.Get("to") != historySafe.String() || q.Has("_from") {
			t.Errorf("transfers -all -in: got the query %v, want one for the transfers to the Safe", q)
		}
	}
	if len(service.queries) != 2 || service.queries[0].Get("erc20") != "true" || service.queries[1].Get("ether") != "true" {
		t.Errorf("transfers -all -in: got the queries %v, want one for ERC-20 transfers and one for native ones", service.queries)
	}

	stdout.Reset()
	if code := Run(t.Context(), []string{"arbot", "-config", config, "transfers", "-all", "-request-file", request}, &stdout, &stderr); code != 0 {
		t.Fatalf("transfers -all: exit status %d\n%s", code, stderr.String())
	}
	matchLines(t, "transfers -all", stdout.Bytes(),
		`^ERC-20 transfers from `+historySafe.String()+` on chain 1, up to block 200, in the blocks with transfers that the Safe Transaction Service lists: 2$`,
		`^Listed by the Safe Transaction Service "`+regexp.QuoteMeta(serviceURL)+`", observed \S+Z: 2, not in the logs of their blocks: 1$`,
		`^100 +`+hashOf(1).String()+` +`,
	)

	// Without a service, -all fails and says so.
	config, request = historyFiles(t, chain.serve(t), "")
	stderr.Reset()
	if code := Run(t.Context(), []string{"arbot", "-config", config, "transfers", "-all", "-request-file", request}, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "-all needs the Safe Transaction Service of chain 1") {
		t.Errorf("transfers -all without a service: got status %d, stderr %q; want 1 and an error about the service", code, stderr.String())
	}
}

func TestTransfersAllUnmatchedOnce(t *testing.T) {
	// The service lists two native transfers of the same value in one transaction,
	// of which only one has a SafeReceived event, and an ERC-20 transfer whose ID
	// has another log index than the transfer that the logs have.
	value := solabi.Uint(big.NewInt(7))
	chain := &historyChain{logs: []ethrpc.Log{
		transferLog(historyAlice, historySafe, 6, hashOf(3), 120, 0),
		{
			Address: historySafe, Topics: []ethrpc.Hash{solabi.Event("SafeReceived(address,uint256)"), solabi.Address(historyAlice)},
			Data: value[:], BlockNumber: 130, TransactionHash: hashOf(5), LogIndex: 1,
		},
	}}
	erc20 := listedTransfer(historyAlice, historySafe, "6", hashOf(3), 120)
	erc20["transferId"] = "e" + hashOf(3).String()[2:] + "4"
	native := map[string]any{
		"type": "ETHER_TRANSFER", "blockNumber": 130, "transactionHash": hashOf(5), "tokenAddress": nil,
		"from": historyAlice, "to": historySafe, "value": "7", "transferId": "i" + hashOf(5).String()[2:] + "0",
	}
	service := &historyService{transfers: []map[string]any{erc20, native, native}}
	config, request := historyFiles(t, chain.serve(t), service.serve(t))

	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"arbot", "-config", config, "transfers", "-all", "-in", "-json", "-request-file", request}, &stdout, &stderr); code != 0 {
		t.Fatalf("transfers -all -in: exit status %d\n%s", code, stderr.String())
	}
	var got struct {
		Service struct {
			Listed    int `json:"listed"`
			Unmatched int `json:"unmatched"`
		} `json:"service"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("transfers -all -in: %v\n%s", err, stdout.String())
	}
	// Each log matches one transfer that the service lists, at its log index.
	if got.Service.Listed != 3 || got.Service.Unmatched != 2 {
		t.Errorf("transfers -all -in: got %d listed and %d unmatched, want 3 and 2", got.Service.Listed, got.Service.Unmatched)
	}
}

func TestTransfersAllFailure(t *testing.T) {
	// A service or a node that fails says nothing about the transfers, so -all
	// fails rather than list fewer.
	listed := &historyService{transfers: []map[string]any{listedTransfer(historySafe, historyAlice, "5", hashOf(1), 100)}}
	for _, test := range []struct {
		name    string
		chain   *historyChain
		service *historyService
		stderr  string
	}{
		{"failing service", &historyChain{}, &historyService{failing: true}, "listing the transfers of"},
		{"failing node", &historyChain{failingBlocks: []uint64{100}}, listed, "run the command again later"},
	} {
		config, request := historyFiles(t, test.chain.serve(t), test.service.serve(t))
		var stdout, stderr bytes.Buffer
		code := Run(t.Context(), []string{"arbot", "-config", config, "transfers", "-all", "-request-file", request}, &stdout, &stderr)
		if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), test.stderr) {
			t.Errorf("%s: got status %d, stdout %q, stderr %q; want 1, no output, and %q", test.name, code, stdout.String(), stderr.String(), test.stderr)
		}
	}
}

func TestTransfersAllUnknownSafe(t *testing.T) {
	// The service lists nothing for a Safe that it doesn't know, and nothing shows
	// what that leaves out, so -all fails rather than list no transfers.
	chain := &historyChain{}
	service := &historyService{unknown: true}
	config, request := historyFiles(t, chain.serve(t), service.serve(t))
	for _, args := range [][]string{nil, {"-in"}} {
		var stdout, stderr bytes.Buffer
		args := append([]string{"arbot", "-config", config, "transfers", "-all", "-json", "-request-file", request}, args...)
		if code := Run(t.Context(), args, &stdout, &stderr); code != 1 || stdout.Len() != 0 ||
			!strings.Contains(stderr.String(), "-all needs the Safe Transaction Service, which doesn't know the Safe "+historySafe.String()) {
			t.Errorf("%v: got status %d, stdout %q, stderr %q; want 1, no output, and an error about the unknown Safe", args, code, stdout.String(), stderr.String())
		}
	}
}
