package status

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/executions"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

var (
	safe             = ethrpc.Address{0: 0x5a, 19: 1}
	executionSuccess = solabi.Event("ExecutionSuccess(bytes32,uint256)")
	executionFailure = solabi.Event("ExecutionFailure(bytes32,uint256)")
	ownHash          = ethrpc.Hash{0: 0x0e, 31: 1}
	otherHash        = ethrpc.Hash{0: 0x07, 31: 2}
)

// The request's Safe block is Ethereum Mainnet block 500, and its Safe
// transaction has the nonce 5. The Checker of the tests scans 100 blocks:
// blocks 500 to 599, or for a stale nonce, 401 to 500.
const (
	safeBlock = 500
	txNonce   = 5
)

// blockTime returns the timestamp of block number of the fake chain, whose
// blocks are 12 seconds apart, as on Ethereum Mainnet.
func blockTime(number uint64) time.Time {
	return time.Unix(1_700_000_000+12*int64(number), 0).UTC()
}

// execution is an execution of a Safe transaction on the fake chain.
type execution struct {
	block      uint64
	safeTxHash ethrpc.Hash
	txHash     ethrpc.Hash
	failed     bool
	// input is the calldata of the onchain transaction, or nil if the node doesn't
	// have it.
	input ethrpc.Bytes
	// setsNonce is the Safe's nonce after an execution whose delegate call sets it,
	// or nil for one that increases it.
	setsNonce *int64
}

// log returns the log of x, as Safe 1.3.0 logs it.
func (x execution) log() ethrpc.Log {
	event := executionSuccess
	if x.failed {
		event = executionFailure
	}
	data := make([]byte, 64)
	copy(data, x.safeTxHash[:])
	return ethrpc.Log{Address: safe, Topics: []ethrpc.Hash{event}, Data: data, BlockNumber: ethrpc.BlockNumber(x.block), TransactionHash: x.txHash}
}

// node is a fake node for Ethereum Mainnet up to block latest, on which the
// Safe made executions, using the nonces from startNonce on.
type node struct {
	latest     uint64
	startNonce int64
	executions []execution
	// noCode is whether the Safe has no code, so that its nonce() returns nothing.
	noCode bool
	// failing are the methods that the node fails with HTTP status 500, as an
	// overloaded node does, and logsError the JSON-RPC error that it answers
	// eth_getLogs with, if any, as for a range that it won't scan, or else
	// logsResult the result, if any, such as null. lookupError is the JSON-RPC
	// error that it answers eth_getTransactionByHash with, if any.
	failing     []string
	logsError   map[string]any
	logsResult  json.RawMessage
	lookupError map[string]any
}

// nonce returns the Safe's nonce at the end of block.
func (n *node) nonce(block uint64) int64 {
	nonce := n.startNonce
	for _, x := range n.executions {
		switch {
		case x.block > block:
		case x.setsNonce != nil:
			nonce = *x.setsNonce
		default:
			nonce++
		}
	}
	return nonce
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
		if slices.Contains(n.failing, req.Method) {
			http.Error(w, "overloaded", http.StatusInternalServerError)
			return
		}
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "eth_chainId":
			res["result"] = ethrpc.Quantity(ethrpc.Mainnet)
		case "eth_blockNumber":
			res["result"] = ethrpc.Quantity(n.latest)
		case "eth_getBlockByNumber":
			var number ethrpc.BlockNumber
			json.Unmarshal(req.Params[0], &number)
			res["result"] = ethrpc.Block{Number: number, Timestamp: ethrpc.Quantity(blockTime(uint64(number)).Unix())}
		case "eth_call":
			var block ethrpc.BlockNumber
			json.Unmarshal(req.Params[1], &block)
			res["result"] = ethrpc.Bytes{}
			if !n.noCode {
				word := solabi.Uint(big.NewInt(n.nonce(uint64(block))))
				res["result"] = ethrpc.Bytes(word[:])
			}
		case "eth_getLogs":
			if n.logsError != nil {
				res["error"] = n.logsError
				break
			}
			if n.logsResult != nil {
				res["result"] = n.logsResult
				break
			}
			var filter ethrpc.LogFilter
			json.Unmarshal(req.Params[0], &filter)
			logs := []ethrpc.Log{}
			for _, x := range n.executions {
				if x.block >= uint64(filter.FromBlock) && x.block <= uint64(filter.ToBlock) {
					logs = append(logs, x.log())
				}
			}
			res["result"] = logs
		case "eth_getTransactionByHash":
			if n.lookupError != nil {
				res["error"] = n.lookupError
				break
			}
			var hash ethrpc.Hash
			json.Unmarshal(req.Params[0], &hash)
			res["result"] = nil
			for _, x := range n.executions {
				if x.txHash == hash && x.input != nil {
					res["result"] = ethrpc.Transaction{Hash: hash, To: &safe, Input: x.input}
				}
			}
		case "eth_getTransactionReceipt":
			var hash ethrpc.Hash
			json.Unmarshal(req.Params[0], &hash)
			res["result"] = nil
			for _, x := range n.executions {
				if x.txHash == hash {
					res["result"] = ethrpc.Receipt{TransactionHash: hash, BlockNumber: ethrpc.BlockNumber(x.block), Status: 1, Logs: []ethrpc.Log{x.log()}}
				}
			}
		default:
			res["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(server.Close)
	client, err := ethrpc.NewClient(t.Context(), ethrpc.Mainnet, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// service is a fake Safe Transaction Service that has the multisig transactions
// by Safe transaction hash, and an about page, or answers every request with
// status, if it is set, to be retried at once. requests counts the requests
// that it gets.
type service struct {
	transactions map[ethrpc.Hash]map[string]any
	status       int
	requests     atomic.Int32
}

func (s *service) serve(t *testing.T) *txservice.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		if s.status != 0 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "unavailable", s.status)
			return
		}
		if r.URL.Path == "/api/v1/about/" {
			w.Write([]byte(`{"name": "Safe Transaction Service"}`))
			return
		}
		for hash, tx := range s.transactions {
			if r.URL.Path == "/api/v1/multisig-transactions/"+hash.String()+"/" {
				json.NewEncoder(w).Encode(tx)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return txservice.NewClient(server.URL, ethrpc.Mainnet)
}

// transaction returns the request's Safe transaction, or with to, another one.
func transaction(to byte) *safenet.SafeTransaction {
	return &safenet.SafeTransaction{
		ChainID: big.NewInt(ethrpc.Mainnet), Safe: safe, To: ethrpc.Address{19: to}, Value: new(big.Int), Data: []byte{},
		SafeTxGas: new(big.Int), BaseGas: new(big.Int), GasPrice: new(big.Int), Nonce: big.NewInt(txNonce),
	}
}

// listing returns the service's multisig transaction for the request's Safe
// transaction, executed in the onchain transaction txHash in block, unless
// txHash is zero.
func listing(txHash ethrpc.Hash, block uint64) map[string]any {
	tx := transaction(0x70)
	result := map[string]any{
		"safe": tx.Safe, "to": tx.To, "value": "0", "data": nil, "operation": 0, "safeTxGas": 0, "baseGas": 0, "gasPrice": "0",
		"gasToken": tx.GasToken, "refundReceiver": tx.RefundReceiver, "nonce": txNonce, "safeTxHash": ownHash, "isExecuted": false,
	}
	if txHash != (ethrpc.Hash{}) {
		result["transactionHash"], result["blockNumber"], result["isExecuted"] = txHash, block, true
	}
	return result
}

// decoded returns an execution in block of tx, which the node has the calldata
// of.
func decoded(block uint64, tx *safenet.SafeTransaction) execution {
	input := solabi.Call(solabi.Selector("execTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes)"),
		tx.To, tx.Value, []byte(tx.Data), uint64(tx.Operation), tx.SafeTxGas, tx.BaseGas, tx.GasPrice, tx.GasToken, tx.RefundReceiver, make([]byte, 65))
	return execution{block: block, safeTxHash: tx.Hash(), txHash: ethrpc.Hash{0: 0xdd, 31: byte(block)}, input: input}
}

// request returns the request of the tests.
func request() *safenet.Request {
	return &safenet.Request{
		ID:       ethrpc.Hash{0: 0x4e, 31: 1},
		Proposal: safenet.Proposal{SafeBlock: safeBlock, SafeTxHash: ownHash, Transaction: *transaction(0x70)},
	}
}

func own(block uint64) execution {
	return execution{block: block, safeTxHash: ownHash, txHash: ethrpc.Hash{0: 0xaa, 31: byte(block)}}
}

func other(block uint64) execution {
	return execution{block: block, safeTxHash: otherHash, txHash: ethrpc.Hash{0: 0xbb, 31: byte(block)}}
}

func TestCheck(t *testing.T) {
	// Another transaction with the nonce, which the node has the calldata of.
	rival := transaction(0x71)
	stale := *rival
	stale.Nonce = big.NewInt(txNonce - 1)
	failed := own(502)
	failed.failed = true
	setBack := own(502)
	setBack.setsNonce = new(int64(txNonce))
	staleSetBack := other(700)
	staleSetBack.setsNonce = new(int64(txNonce))
	staleReset := other(501)
	staleReset.setsNonce = new(int64(txNonce))
	staleResetLater := other(510)
	staleResetLater.setsNonce = new(int64(txNonce))
	setBackBefore := own(450)
	setBackBefore.setsNonce = new(int64(txNonce))
	otherSetBack := other(450)
	otherSetBack.setsNonce = new(int64(txNonce))
	// rivalSetBack used the nonce before the Safe block; only the service has it.
	rivalSetBack := execution{block: 450, safeTxHash: rival.Hash(), txHash: ethrpc.Hash{0: 0xbb, 31: 1}, setsNonce: new(int64(txNonce))}
	rivalListing := listing(ethrpc.Hash{}, 0)
	rivalListing["to"], rivalListing["safeTxHash"] = rival.To, rival.Hash()
	tests := []struct {
		name       string
		latest     uint64
		executions []execution
		// startNonce is the Safe's nonce before the executions, txNonce if zero, and
		// blocks the Checker's Blocks, 100 if zero.
		startNonce int64
		blocks     uint64
		service    map[ethrpc.Hash]map[string]any
		// wantStatus, wantSource, wantBlock, and wantSafeTxHash are the execution that
		// Check should report, whose call succeeded unless wantFailed.
		wantStatus, wantSource string
		wantBlock              uint64
		wantSafeTxHash         ethrpc.Hash
		wantFailed             bool
		wantStale              bool
		wantScanned            *executions.BlockRange
		// wantReason and wantNote are in the execution's reason and in a note, and
		// notReason isn't in the reason.
		wantReason, wantNote, notReason string
	}{
		{
			name: "executed before the proposal", executions: []execution{own(safeBlock)},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: safeBlock, wantSafeTxHash: ownHash,
			wantStale: true, wantScanned: &executions.BlockRange{FromBlock: 401, ToBlock: 600},
			wantReason: "ExecutionSuccess", wantNote: "below the Safe's nonce 6 at the Safe block 500",
		},
		{
			name: "executed after the proposal, and failed", executions: []execution{failed},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: 502, wantSafeTxHash: ownHash, wantFailed: true,
			wantScanned: &executions.BlockRange{FromBlock: 400, ToBlock: 599}, wantReason: "ExecutionFailure",
		},
		{
			name: "executed, from the service", executions: []execution{own(502)},
			service:    map[ethrpc.Hash]map[string]any{ownHash: listing(own(502).txHash, 502)},
			wantStatus: Executed, wantSource: SourceService, wantBlock: 502, wantSafeTxHash: ownHash, wantReason: "names transaction",
		},
		{
			// The service names a transaction whose receipt has no log of the execution, so
			// the chain is read instead.
			name: "misreported by the service", executions: []execution{own(502), other(600)},
			service:    map[ethrpc.Hash]map[string]any{ownHash: listing(other(600).txHash, 600)},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: 502, wantSafeTxHash: ownHash,
			wantScanned: &executions.BlockRange{FromBlock: 400, ToBlock: 599}, wantNote: "which the chain doesn't confirm",
		},
		{
			name: "named by the service, without a receipt", executions: []execution{own(502)},
			service:    map[ethrpc.Hash]map[string]any{ownHash: listing(ethrpc.Hash{31: 0xcc}, 502)},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: 502, wantSafeTxHash: ownHash, wantNote: "getting its receipt failed",
		},
		{
			// The Safe's nonce is unused, but a delegate call of the transaction set it
			// back, so the scan finds the execution.
			name: "nonce set back", executions: []execution{setBack},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: 502, wantSafeTxHash: ownHash,
		},
		{
			// The transaction ran before the Safe block, and its delegate call set the
			// nonce back, so the nonce isn't stale, and only the scan of the blocks before
			// the Safe block finds the execution.
			name: "executed before the Safe block, nonce set back", latest: 550, executions: []execution{setBackBefore},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: 450, wantSafeTxHash: ownHash,
			wantScanned: &executions.BlockRange{FromBlock: 400, ToBlock: 550}, wantReason: "in block 450",
		},
		{
			name:       "not executed",
			service:    map[ethrpc.Hash]map[string]any{ownHash: listing(ethrpc.Hash{}, 0)},
			wantStatus: NotExecuted, wantSource: SourceNonce, wantScanned: &executions.BlockRange{FromBlock: 400, ToBlock: 599},
			wantReason: "An execution before block 400 or after block 599 that set the nonce back isn't ruled out", wantNote: "doesn't list the Safe transaction as executed",
		},
		{
			name: "not executed, unknown to the service", service: map[ethrpc.Hash]map[string]any{},
			wantStatus: NotExecuted, wantSource: SourceNonce, wantNote: "doesn't have the Safe transaction",
		},
		{
			// The scan reaches the latest block.
			name: "not executed, without the service", latest: 550,
			wantStatus: NotExecuted, wantSource: SourceNonce, wantScanned: &executions.BlockRange{FromBlock: 400, ToBlock: 550},
			wantReason: "An execution before block 400 that set the nonce back isn't ruled out", notReason: "after block",
		},
		{
			// The scan reaches the first block and the latest.
			name: "not executed, every block scanned", latest: 550, blocks: 600,
			wantStatus: NotExecuted, wantSource: SourceNonce, wantScanned: &executions.BlockRange{FromBlock: 0, ToBlock: 550},
			notReason: "isn't ruled out",
		},
		{
			// Another transaction used the nonce before the Safe block, and its delegate
			// call set the nonce back, so that use says nothing about this transaction.
			name: "nonce used before the Safe block and set back", latest: 550, executions: []execution{rivalSetBack},
			service:    map[ethrpc.Hash]map[string]any{rival.Hash(): rivalListing},
			wantStatus: NotExecuted, wantSource: SourceNonce, wantNote: "doesn't have the Safe transaction",
		},
		{
			name: "nonce used by another transaction", executions: []execution{other(501), decoded(503, rival)}, startNonce: txNonce - 1,
			wantStatus: OtherTransaction, wantSource: SourceLogs, wantBlock: 503, wantSafeTxHash: rival.Hash(), wantReason: "(source calldata)",
		},
		{
			name: "stale nonce used by another transaction", executions: []execution{decoded(420, &stale), decoded(450, rival)}, startNonce: txNonce - 1,
			wantStatus: OtherTransaction, wantSource: SourceLogs, wantBlock: 450, wantSafeTxHash: rival.Hash(),
			wantStale: true, wantScanned: &executions.BlockRange{FromBlock: 401, ToBlock: 600},
		},
		{
			// No source has the transaction that used the nonce, so which one did is
			// unknown, though the nonce rose by one. The one before the Safe block, whose
			// use of the nonce was set back, doesn't count.
			name: "nonce used by an unknown transaction", executions: []execution{otherSetBack, other(503)},
			wantStatus: Unknown,
			wantReason: "executions there: 1, unknown transactions among them: 1). The Safe logged no execution of the transaction in blocks 400 to 499 either.",
		},
		{
			name: "stale nonce used before the scan", executions: []execution{other(300), other(350)}, startNonce: txNonce - 1,
			wantStatus: Unknown, wantStale: true, wantScanned: &executions.BlockRange{FromBlock: 401, ToBlock: 600},
			wantReason: "in blocks 501 to 600 either. The Safe may have used the nonce outside those blocks, which a larger -blocks scans",
		},
		{
			// Another transaction used the nonce before the Safe block, but a delegate call
			// after it set the nonce back, and the transaction ran, which only the scan of
			// the blocks after the Safe block finds.
			name: "stale nonce used by another transaction, then executed", latest: 550,
			executions: []execution{decoded(450, rival), staleReset, own(502)},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: 502, wantSafeTxHash: ownHash,
			wantStale: true, wantScanned: &executions.BlockRange{FromBlock: 401, ToBlock: 550}, wantReason: "in a scan of blocks 401 to 550",
		},
		{
			// Another transaction used the nonce before the Safe block, and a delegate call
			// after it set the nonce back, so the Safe's nonce at the end of the scan says
			// nothing about the nonce of the other one, which the Safe's nonce at the Safe
			// block gives.
			name: "stale nonce used by another transaction, then set back", latest: 550,
			executions: []execution{decoded(450, rival), staleReset},
			wantStatus: OtherTransaction, wantSource: SourceLogs, wantBlock: 450, wantSafeTxHash: rival.Hash(),
			wantStale: true, wantScanned: &executions.BlockRange{FromBlock: 401, ToBlock: 550}, wantReason: "in a scan of blocks 401 to 500",
		},
		{
			// Another transaction used the nonce again after a set-back after the Safe
			// block, which says nothing about which one used it before.
			name: "stale nonce used again after the Safe block", latest: 550,
			executions: []execution{other(420), other(450), staleResetLater, decoded(520, rival)}, startNonce: txNonce - 1,
			wantStatus: Unknown, wantStale: true, wantScanned: &executions.BlockRange{FromBlock: 401, ToBlock: 550},
			wantReason: "(executions there: 2, unknown transactions among them: 2). The Safe logged no execution of the transaction in blocks 501 to 550 either.",
		},
		{
			// The scan reaches the first block, but not the latest.
			name: "stale nonce, the blocks after the scan unscanned", latest: 1200, blocks: 600,
			executions: []execution{other(420), other(450)}, startNonce: txNonce - 1,
			wantStatus: Unknown, wantStale: true, wantScanned: &executions.BlockRange{FromBlock: 0, ToBlock: 1100},
			wantReason: "outside those blocks, which a larger -blocks scans",
		},
		{
			// A delegate call after the Safe block set the nonce back to the transaction's,
			// which the scan up to the Safe block says nothing about.
			name: "stale nonce set back after the Safe block", executions: []execution{other(420), other(450), staleSetBack}, startNonce: txNonce - 1,
			wantStatus: Unknown, wantStale: true, wantReason: "The Safe's nonce is 6 at block 500, above the transaction's nonce 5",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chain := &node{latest: 1000, startNonce: txNonce, executions: test.executions}
			if test.latest != 0 {
				chain.latest = test.latest
			}
			if test.startNonce != 0 {
				chain.startNonce = test.startNonce
			}
			c := &Checker{Safe: chain.serve(t), Blocks: 100}
			if test.blocks != 0 {
				c.Blocks = test.blocks
			}
			if test.service != nil {
				c.Service = (&service{transactions: test.service}).serve(t)
			}
			got, err := c.Check(t.Context(), request())
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			data, _ := json.MarshalIndent(got, "", "  ")
			x := got.Execution
			if x.Status != test.wantStatus || x.Source != test.wantSource {
				t.Errorf("Check: got execution %s from %q, want %s from %q\n%s", x.Status, x.Source, test.wantStatus, test.wantSource, data)
			}
			if test.wantBlock != 0 {
				if x.Block == nil || *x.Block != test.wantBlock || !x.BlockTime.Equal(blockTime(test.wantBlock)) || *x.Success == test.wantFailed ||
					x.SafeTxHash == nil || *x.SafeTxHash != test.wantSafeTxHash {
					t.Errorf("Check: got execution %s, want one in block %d of %s\n%s", x.Status, test.wantBlock, test.wantSafeTxHash, data)
				}
			} else if x.Block != nil || x.BlockTime != nil || x.TxHash != nil || x.Success != nil || x.SafeTxHash != nil {
				t.Errorf("Check: got an execution in a block, want none\n%s", data)
			}
			if got.Nonce.Stale != test.wantStale {
				t.Errorf("Check: got stale %t, want %t", got.Nonce.Stale, test.wantStale)
			}
			if test.wantSource == SourceService && x.Scanned != nil || test.wantScanned != nil && (x.Scanned == nil || *x.Scanned != *test.wantScanned) {
				t.Errorf("Check: got blocks %+v scanned, want %+v", x.Scanned, test.wantScanned)
			}
			if (test.service != nil) != (x.Service != nil) {
				t.Errorf("Check: got service %+v, want it only with a service", x.Service)
			}
			if len(got.Notes) == 0 || got.Notes[0] != NotEvidence {
				t.Errorf("Check: got notes %q, want the first to say that nothing on chain after the Safe block is onchain data", got.Notes)
			}
			if test.wantNote != "" && !slices.ContainsFunc(got.Notes, func(n string) bool { return strings.Contains(n, test.wantNote) }) {
				t.Errorf("Check: got notes %q, want one containing %q", got.Notes, test.wantNote)
			}
			if x.Reason == "" || !strings.Contains(x.Reason, test.wantReason) || test.notReason != "" && strings.Contains(x.Reason, test.notReason) {
				t.Errorf("Check: got reason %q, want one containing %q, and not %q", x.Reason, test.wantReason, test.notReason)
			}
		})
	}
}

// TestCheckScanFails checks that a scan that the node refuses with a JSON-RPC
// error, as over its range limit, leaves the status unknown, with the node's
// error, while a result that isn't logs, such as null or a log without its
// block number, fails the check, and one without logs is an answer.
func TestCheckScanFails(t *testing.T) {
	chain := &node{latest: 1000, startNonce: txNonce, executions: []execution{own(502)}}
	chain.logsError = map[string]any{"code": -32602, "message": "block range too large"}
	got, err := (&Checker{Safe: chain.serve(t), Blocks: 100}).Check(t.Context(), request())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if x := got.Execution; x.Status != Unknown || x.Source != "" || x.Block != nil || !strings.Contains(x.Reason, "block range too large") {
		t.Errorf("Check: got execution %+v, want unknown, with the node's error", x)
	}
	for _, result := range []string{"null", "{}", "[null]", "[{}]"} {
		chain := &node{latest: 1000, startNonce: txNonce, executions: []execution{own(502)}, logsResult: json.RawMessage(result)}
		if got, err := (&Checker{Safe: chain.serve(t), Blocks: 100}).Check(t.Context(), request()); err == nil {
			t.Errorf("Check with the eth_getLogs result %s: got execution %+v, want an error", result, got.Execution)
		}
	}
	chain = &node{latest: 1000, startNonce: txNonce, logsResult: json.RawMessage("[]")}
	if got, err := (&Checker{Safe: chain.serve(t), Blocks: 100}).Check(t.Context(), request()); err != nil || got.Execution.Status != NotExecuted {
		t.Errorf("Check with the eth_getLogs result []: got %v, want not-executed", err)
	}
}

// TestCheckLookupFails checks that a request that the decoding of the
// executions makes after the scan, such as for an onchain transaction, isn't
// taken for the scan: the transaction's own execution needs no decoding, nor
// does an execution before the Safe block, another one is unknown with the
// node's refusal, and one that gets no answer fails the check.
func TestCheckLookupFails(t *testing.T) {
	refusal := map[string]any{"code": -32602, "message": "transaction lookup rejected"}
	for _, test := range []struct {
		name       string
		executions []execution
		startNonce int64
		// lookupError and failing are the node's. wantStatus and wantReason are the
		// execution's, or wantStatus is empty for an error without an answer.
		lookupError            map[string]any
		failing                []string
		wantStatus, wantReason string
	}{
		{
			name: "the transaction's own execution", executions: []execution{own(502)}, startNonce: txNonce, lookupError: refusal,
			wantStatus: Executed, wantReason: "in a scan of blocks 400 to 599",
		},
		{
			name: "another execution", executions: []execution{other(503)}, startNonce: txNonce, lookupError: refusal,
			wantStatus: Unknown, wantReason: "transaction lookup rejected",
		},
		{
			name: "another execution before the Safe block", executions: []execution{other(450)}, startNonce: txNonce - 1,
			failing: []string{"eth_getTransactionByHash"}, wantStatus: NotExecuted,
		},
		{
			name: "another execution, without an answer", executions: []execution{other(503)}, startNonce: txNonce,
			failing: []string{"eth_getTransactionByHash"},
		},
		{
			name: "another execution, the lookup timed out", executions: []execution{other(503)}, startNonce: txNonce,
			lookupError: map[string]any{"code": -32016, "message": "request timed out"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			chain := &node{latest: 1000, startNonce: test.startNonce, executions: test.executions, lookupError: test.lookupError, failing: test.failing}
			got, err := (&Checker{Safe: chain.serve(t), Blocks: 100}).Check(t.Context(), request())
			if test.wantStatus == "" {
				if !ethrpc.Transient(err) {
					t.Errorf("Check: got error %v, want one without an answer", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if x := got.Execution; x.Status != test.wantStatus || !strings.Contains(x.Reason, test.wantReason) || strings.Contains(x.Reason, "Scanning") {
				t.Errorf("Check: got execution %s with the reason %q, want %s with one containing %q, and not about the scan", x.Status, x.Reason, test.wantStatus, test.wantReason)
			}
		})
	}
}

// TestCheckServiceRefuses checks that a service that refuses the request is a
// warning and a note, and that the chain is then read without it, also when the
// scan decodes another execution, which it would ask the service for.
func TestCheckServiceRefuses(t *testing.T) {
	for _, test := range []struct {
		name       string
		executions []execution
		wantStatus string
		wantSource string
	}{
		{"the transaction's own execution", []execution{own(502), other(503)}, Executed, SourceLogs},
		// The execution is of an unknown transaction, which the scan would ask the
		// service for.
		{"another execution", []execution{other(503)}, Unknown, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			chain := &node{latest: 1000, startNonce: txNonce, executions: test.executions}
			svc := &service{status: http.StatusForbidden}
			c := &Checker{Safe: chain.serve(t), Service: svc.serve(t), Blocks: 100}
			var warnings []string
			c.Warn = func(err error) {
				if strings.Contains(err.Error(), "Safe Transaction Service") {
					warnings = append(warnings, err.Error())
				}
			}
			got, err := c.Check(t.Context(), request())
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if got.Execution.Status != test.wantStatus || got.Execution.Source != test.wantSource || len(warnings) != 1 || svc.requests.Load() != 1 ||
				!slices.ContainsFunc(got.Notes, func(n string) bool { return strings.Contains(n, "the chain was read without it: HTTP 403") }) {
				t.Errorf("Check: got execution %+v, notes %q, warnings %v, and %d requests to the service; want %s from %q, with a note and a warning on one request",
					got.Execution, got.Notes, warnings, svc.requests.Load(), test.wantStatus, test.wantSource)
			}
		})
	}
}

// TestCheckIncompleteLogMembers checks that a log without its topics, data, or
// index is no answer, rather than a log that the scan skips, which would leave
// the transaction not executed.
func TestCheckIncompleteLogMembers(t *testing.T) {
	const (
		data  = `"data":"0x"`
		index = `"logIndex":"0x0"`
	)
	topics := fmt.Sprintf(`"topics":[%q]`, own(502).log().Topics[0])
	fields := fmt.Sprintf(`"address":%q,"blockNumber":"0x1f6","transactionHash":%q`, safe, own(502).txHash)
	for _, test := range []struct{ name, log string }{
		{"no topics", fields + "," + data + "," + index},
		{"no data", fields + "," + topics + "," + index},
		{"no index", fields + "," + topics + "," + data},
	} {
		t.Run(test.name, func(t *testing.T) {
			chain := &node{latest: 550, startNonce: txNonce, logsResult: json.RawMessage("[{" + test.log + "}]")}
			got, err := (&Checker{Safe: chain.serve(t), Blocks: 100}).Check(t.Context(), request())
			if err == nil {
				t.Errorf("Check with a log with %s: got execution %+v, want an error", test.name, got.Execution)
			}
		})
	}
}

func TestCheckTransient(t *testing.T) {
	for _, test := range []struct {
		name    string
		failing []string
		// service is the status that the service answers with, http.StatusOK for one
		// that names the execution, or 0 for no service.
		service int
	}{
		{name: "latest block", failing: []string{"eth_blockNumber"}},
		{name: "nonce", failing: []string{"eth_call"}},
		{name: "logs", failing: []string{"eth_getLogs"}},
		{name: "execution time", failing: []string{"eth_getBlockByNumber"}},
		{name: "receipt", failing: []string{"eth_getTransactionReceipt"}, service: http.StatusOK},
		{name: "service", service: http.StatusInternalServerError},
		{name: "service rate limit", service: http.StatusTooManyRequests},
	} {
		t.Run(test.name, func(t *testing.T) {
			chain := &node{latest: 1000, startNonce: txNonce, executions: []execution{own(502)}, failing: test.failing}
			c := &Checker{Safe: chain.serve(t), Blocks: 100}
			switch test.service {
			case 0:
			case http.StatusOK:
				c.Service = (&service{transactions: map[ethrpc.Hash]map[string]any{ownHash: listing(own(502).txHash, 502)}}).serve(t)
			default:
				c.Service = (&service{status: test.service}).serve(t)
			}
			if _, err := c.Check(t.Context(), request()); !ethrpc.Transient(err) && !txservice.Unavailable(err) {
				t.Errorf("Check: got error %v, want one without an answer", err)
			}
		})
	}
}

func TestCheckErrors(t *testing.T) {
	otherChain, noNonce, early := request(), request(), request()
	otherChain.Proposal.Transaction.ChainID = big.NewInt(ethrpc.Gnosis)
	noNonce.Proposal.Transaction.Nonce = nil
	early.Proposal.SafeBlock = 1001
	for _, test := range []struct {
		name    string
		chain   *node
		request *safenet.Request
		want    string
	}{
		{"another chain", &node{latest: 1000}, otherChain, "is for chain 100, not 1"},
		{"no nonce", &node{latest: 1000}, noNonce, "has no nonce"},
		{"Safe block after the latest", &node{latest: 1000}, early, "the latest block 1000 of chain 1 is before the Safe block 1001"},
		{"no code", &node{latest: 1000, noCode: true}, request(), "as a call to an account without code does, so it isn't a Safe"},
	} {
		if _, err := (&Checker{Safe: test.chain.serve(t), Blocks: 100}).Check(t.Context(), test.request); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("Check %s: got error %v, want one containing %q", test.name, err, test.want)
		}
	}
}

// TestReportJSON checks the keys of the JSON of a report, which agents read,
// with null for an execution that there is none of.
func TestReportJSON(t *testing.T) {
	report := &Report{
		Request: ethrpc.Hash{31: 1}, Safe: safe, ChainID: 1, SafeBlock: safeBlock, SafeTxHash: ownHash,
		Nonce:     Nonce{Transaction: big.NewInt(5), SafeAtSafeBlock: big.NewInt(5), SafeAtLatest: big.NewInt(5), LatestBlock: 1000},
		Execution: Execution{Status: NotExecuted, Source: SourceNonce, Reason: "r", Scanned: &executions.BlockRange{FromBlock: 500, ToBlock: 599}},
		Notes:     []string{NotEvidence},
	}
	got, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"request":"` + report.Request.String() + `","safe":"` + safe.String() + `","chainId":1,"safeBlock":500,"safeTxHash":"` + ownHash.String() + `",` +
		`"nonce":{"transaction":5,"safeAtSafeBlock":5,"safeAtLatest":5,"latestBlock":1000,"stale":false},` +
		`"execution":{"status":"not-executed","txHash":null,"block":null,"blockTime":null,"success":null,"safeTxHash":null,"source":"nonce","reason":"r",` +
		`"service":null,"scanned":{"fromBlock":500,"toBlock":599}},"notes":["` + NotEvidence + `"]}`
	if string(got) != want {
		t.Errorf("JSON of a report:\ngot  %s\nwant %s", got, want)
	}
}
