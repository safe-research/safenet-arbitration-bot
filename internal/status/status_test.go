package status

import (
	"cmp"
	"context"
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

// The request is proposed in Gnosis Chain block 1201, at genesis plus 6005
// seconds, so its Safe block is Ethereum Mainnet block 500, at genesis plus
// 6000, and its Safe transaction has the nonce 5. Its commit and reveal
// deadlines are blocks 1207 and 1213, it was frozen in block 1208, and the
// arbitration was settled in block 2000, before the latest block 2400. The
// Checker of the tests scans 100 blocks: blocks 500 to 599, or for a stale
// nonce, 401 to 500.
const (
	proposalBlock  = 1201
	safeBlock      = 500
	txNonce        = 5
	commitDeadline = 1207
	revealDeadline = 1213
	frozenBlock    = 1208
	settledBlock   = 2000
	latestGnosis   = 2400
)

// genesis is the time of block 0 of the fake chains.
const genesis = 1_700_000_000

// blockTime returns the timestamp of block number of a fake Ethereum Mainnet,
// whose blocks are 12 seconds apart.
func blockTime(number uint64) time.Time {
	return time.Unix(genesis+12*int64(number), 0).UTC()
}

// gnosisTime returns the timestamp of block number of a fake Gnosis Chain,
// whose blocks are 5 seconds apart.
func gnosisTime(number uint64) time.Time {
	return time.Unix(genesis+5*int64(number), 0).UTC()
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

// node is a fake node for the chain with the ID chainID, Ethereum Mainnet if
// zero, up to block latest, whose blocks are at the times that time returns,
// blockTime if nil, and on which the Safe made executions, using the nonces
// from startNonce on.
type node struct {
	chainID    uint64
	time       func(uint64) time.Time
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
	// logsRead counts the eth_getLogs requests that the node got.
	logsRead atomic.Int32
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
	chainID, timeOf := cmp.Or(n.chainID, ethrpc.Mainnet), n.time
	if timeOf == nil {
		timeOf = blockTime
	}
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
			res["result"] = ethrpc.Quantity(chainID)
		case "eth_blockNumber":
			res["result"] = ethrpc.Quantity(n.latest)
		case "eth_getBlockByNumber":
			var number ethrpc.BlockNumber
			json.Unmarshal(req.Params[0], &number)
			res["result"] = ethrpc.Block{Number: number, Timestamp: ethrpc.Quantity(timeOf(uint64(number)).Unix())}
		case "eth_call":
			var block ethrpc.BlockNumber
			json.Unmarshal(req.Params[1], &block)
			res["result"] = ethrpc.Bytes{}
			if !n.noCode {
				word := solabi.Uint(big.NewInt(n.nonce(uint64(block))))
				res["result"] = ethrpc.Bytes(word[:])
			}
		case "eth_getLogs":
			n.logsRead.Add(1)
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
	client, err := ethrpc.NewClient(t.Context(), chainID, server.URL)
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
		ID:    ethrpc.Hash{0: 0x4e, 31: 1},
		Terms: safenet.Terms{CommitDeadline: commitDeadline, RevealDeadline: revealDeadline},
		Proposal: safenet.Proposal{
			Block: proposalBlock, Time: gnosisTime(proposalBlock), SafeBlock: safeBlock, SafeTxHash: ownHash, Transaction: *transaction(0x70),
		},
	}
}

// progress returns the request's progress as of the latest Gnosis Chain block:
// frozen, then resolved secure.
func progress() *safenet.Progress {
	return &safenet.Progress{
		Block: latestGnosis, State: safenet.StateResolvedApproved,
		Terms: safenet.Terms{CommitDeadline: commitDeadline, RevealDeadline: revealDeadline},
		Arbitration: &safenet.Arbitration{
			FrozenBlock: frozenBlock, Deadline: frozenBlock + 100, Outcome: safenet.OutcomeSecure, Record: &safenet.Record{Block: settledBlock},
		},
	}
}

// fixed returns a reader of a request's progress that returns p.
func fixed(p *safenet.Progress) func(context.Context, ethrpc.Hash) (*safenet.Progress, error) {
	return func(context.Context, ethrpc.Hash) (*safenet.Progress, error) { return p, nil }
}

// checker returns a Checker for the Safe on chain, and a fake Gnosis Chain up
// to the latest block, which scans 100 blocks.
func checker(t *testing.T, chain *node) *Checker {
	t.Helper()
	gnosis := &node{chainID: ethrpc.Gnosis, time: gnosisTime, latest: latestGnosis}
	return &Checker{Gnosis: gnosis.serve(t), Safe: chain.serve(t), Blocks: 100}
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
		// wantTiming is the timing of the execution, if there is one.
		wantTiming string
		// wantReason and wantNote are in the execution's reason and in a note, and
		// notReason isn't in the reason.
		wantReason, wantNote, notReason string
	}{
		{
			name: "executed before the proposal", executions: []execution{own(safeBlock)},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: safeBlock, wantSafeTxHash: ownHash,
			wantStale: true, wantScanned: &executions.BlockRange{FromBlock: 401, ToBlock: 600}, wantTiming: BeforeProposal,
			wantReason: "ExecutionSuccess", wantNote: "below the Safe's nonce 6 at the Safe block 500",
		},
		{
			name: "executed after the proposal, and failed", executions: []execution{failed},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: 502, wantSafeTxHash: ownHash, wantFailed: true,
			wantScanned: &executions.BlockRange{FromBlock: 400, ToBlock: 599}, wantTiming: DuringVoting, wantReason: "ExecutionFailure",
			wantNote: "The Safe transaction was executed at 2023-11-14T23:53:44Z, during-voting",
		},
		{
			// The execution is in the latest block, which the report still reads.
			name: "executed, from the service", latest: 504, executions: []execution{own(504)},
			service:    map[ethrpc.Hash]map[string]any{ownHash: listing(own(504).txHash, 504)},
			wantStatus: Executed, wantSource: SourceService, wantBlock: 504, wantSafeTxHash: ownHash, wantTiming: AfterFreeze, wantReason: "names transaction",
		},
		{
			// The service names a transaction whose receipt has no log of the execution, so
			// the chain is read instead.
			name: "misreported by the service", executions: []execution{own(502), other(600)},
			service:    map[ethrpc.Hash]map[string]any{ownHash: listing(other(600).txHash, 600)},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: 502, wantSafeTxHash: ownHash, wantTiming: DuringVoting,
			wantScanned: &executions.BlockRange{FromBlock: 400, ToBlock: 599}, wantNote: "which the chain doesn't confirm",
		},
		{
			name: "named by the service, without a receipt", executions: []execution{own(502)},
			service:    map[ethrpc.Hash]map[string]any{ownHash: listing(ethrpc.Hash{31: 0xcc}, 502)},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: 502, wantSafeTxHash: ownHash, wantTiming: DuringVoting,
			wantNote: "getting its receipt failed",
		},
		{
			// The service names an execution after the latest block that the report reads,
			// as a node behind the service's can have it, so the chain is read instead.
			name: "named by the service, after the latest block", executions: []execution{own(1100)},
			service:    map[ethrpc.Hash]map[string]any{ownHash: listing(own(1100).txHash, 1100)},
			wantStatus: NotExecuted, wantSource: SourceNonce, wantNote: "in block 1100, after the latest block 1000",
		},
		{
			// The Safe's nonce is unused, but a delegate call of the transaction set it
			// back, so the scan finds the execution.
			name: "nonce set back", executions: []execution{setBack},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: 502, wantSafeTxHash: ownHash, wantTiming: DuringVoting,
		},
		{
			// The transaction ran before the Safe block, and its delegate call set the
			// nonce back, so the nonce isn't stale, and only the scan of the blocks before
			// the Safe block finds the execution.
			name: "executed before the Safe block, nonce set back", latest: 550, executions: []execution{setBackBefore},
			wantStatus: Executed, wantSource: SourceLogs, wantBlock: 450, wantSafeTxHash: ownHash,
			wantScanned: &executions.BlockRange{FromBlock: 400, ToBlock: 550}, wantTiming: BeforeProposal, wantReason: "in block 450",
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
			wantStatus: OtherTransaction, wantSource: SourceLogs, wantBlock: 503, wantSafeTxHash: rival.Hash(), wantTiming: DuringVoting,
			wantReason: "(source calldata)", wantNote: "The other Safe transaction with the nonce was executed at",
		},
		{
			name: "stale nonce used by another transaction", executions: []execution{decoded(420, &stale), decoded(450, rival)}, startNonce: txNonce - 1,
			wantStatus: OtherTransaction, wantSource: SourceLogs, wantBlock: 450, wantSafeTxHash: rival.Hash(),
			wantStale: true, wantScanned: &executions.BlockRange{FromBlock: 401, ToBlock: 600}, wantTiming: BeforeProposal,
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
			wantTiming: DuringVoting,
		},
		{
			// Another transaction used the nonce before the Safe block, and a delegate call
			// after it set the nonce back, so the Safe's nonce at the end of the scan says
			// nothing about the nonce of the other one, which the Safe's nonce at the Safe
			// block gives.
			name: "stale nonce used by another transaction, then set back", latest: 550,
			executions: []execution{decoded(450, rival), staleReset},
			wantStatus: OtherTransaction, wantSource: SourceLogs, wantBlock: 450, wantSafeTxHash: rival.Hash(),
			wantStale: true, wantScanned: &executions.BlockRange{FromBlock: 401, ToBlock: 550}, wantTiming: BeforeProposal,
			wantReason: "in a scan of blocks 401 to 500",
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
			c := checker(t, chain)
			if test.blocks != 0 {
				c.Blocks = test.blocks
			}
			if test.service != nil {
				c.Service = (&service{transactions: test.service}).serve(t)
			}
			got, err := c.Check(t.Context(), request(), fixed(progress()))
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
			if timing := got.Timing; (timing == nil) != (test.wantTiming == "") || timing != nil && timing.Value != test.wantTiming {
				t.Errorf("Check: got timing %+v, want %q", timing, test.wantTiming)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(data, &top); err != nil || got.State != safenet.StateResolvedApproved || string(top["stateBlock"]) != fmt.Sprint(latestGnosis) {
				t.Errorf("Check: got state %s as of block %s, want the progress's, as of block %d", got.State, top["stateBlock"], latestGnosis)
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

// TestCheckTiming checks where Check places an execution in the request's
// timeline, and its notes on ties. Gnosis Chain block g is at genesis plus 5 g
// seconds and Ethereum Mainnet block m at genesis plus 12 m, so Mainnet block
// 500 has the time of Gnosis Chain block 1200, 505 that of 1212, 510 that of
// 1224, and 515 that of 1236.
func TestCheckTiming(t *testing.T) {
	// arbitrumTime returns the timestamp of block number of a fake Arbitrum One,
	// whose blocks share timestamps, four in each second. Block 24019 is the last
	// one before the proposal, and 24020 has its time.
	arbitrumTime := func(number uint64) time.Time { return time.Unix(genesis+int64(number/4), 0).UTC() }
	tests := []struct {
		name string
		// chainID is the Safe's chain, Ethereum Mainnet if zero, and block the block of
		// the execution. gnosisClock gives Arbitrum One's blocks the times of Gnosis
		// Chain's, so that a block can have the number and time of an event's.
		chainID, block uint64
		gnosisClock    bool
		// proposal and safe are the proposal's block and the Safe block, proposalBlock
		// and safeBlock if zero.
		proposal, safe uint64
		// reveal is the reveal deadline, revealDeadline if zero, and frozen and settled
		// the blocks that froze the request and settled its arbitration, if any.
		reveal, frozen, settled uint64
		// now is the latest Gnosis Chain block, latestGnosis if zero.
		now  uint64
		want string
		// wantTie is in the only note on a tie, if any.
		wantTie string
	}{
		{name: "before the proposal", block: 500, want: BeforeProposal},
		{
			name: "proposal tie", block: 500, proposal: 1200, safe: 499, want: DuringVoting,
			wantTie: "same timestamp as the block of the proposal, so which came first is unknown; it is after the Safe block 499",
		},
		{name: "during the voting", block: 502, frozen: frozenBlock, want: DuringVoting},
		{
			name: "freeze tie", block: 505, frozen: 1212, want: DuringVoting,
			wantTie: "same timestamp as the block of the freeze, so which came first is unknown; it is placed before the freeze",
		},
		{name: "after the freeze", block: 504, frozen: frozenBlock, want: AfterFreeze},
		{name: "reveal deadline tie", block: 505, reveal: 1212, want: DuringVoting, wantTie: "the block of the reveal deadline"},
		{name: "after the voting", block: 506, want: AfterVoting},
		// The request was frozen after its reveal deadline, as when finalize is called
		// late.
		{name: "after the voting, before a later freeze", block: 520, frozen: 1300, want: AfterVoting},
		{name: "after the voting, freeze tie", block: 510, frozen: 1224, want: AfterVoting, wantTie: "placed before the freeze"},
		{
			name: "settlement tie", block: 515, frozen: frozenBlock, settled: 1236, want: AfterFreeze,
			wantTie: "same timestamp as the block of the settlement, so which came first is unknown; it is placed before the settlement",
		},
		{name: "after the settlement", block: 520, frozen: frozenBlock, settled: 1236, want: AfterResolution},
		{name: "reveal deadline at the latest block", block: 505, reveal: 1212, now: 1212, want: DuringVoting, wantTie: "the block of the reveal deadline"},
		{name: "Gnosis Chain, before the proposal", chainID: ethrpc.Gnosis, block: 1200, safe: 1200, want: BeforeProposal},
		{
			name: "Gnosis Chain, in the proposal's block", chainID: ethrpc.Gnosis, block: 1201, safe: 1200, want: DuringVoting,
			wantTie: "The execution is in Gnosis Chain block 1201, the block of the proposal, after the Safe block 1200, so it is placed after the proposal, whichever of their transactions came first.",
		},
		{
			name: "Gnosis Chain, in the freeze's block", chainID: ethrpc.Gnosis, block: 1208, safe: 1200, frozen: 1208, want: DuringVoting,
			wantTie: "The execution is in Gnosis Chain block 1208, the block of the freeze, and is placed before the freeze.",
		},
		{name: "shared timestamps, in the Safe block", chainID: ethrpc.ArbitrumOne, block: 24019, safe: 24019, want: BeforeProposal},
		{
			name: "shared timestamps, after the Safe block", chainID: ethrpc.ArbitrumOne, block: 24020, safe: 24019, want: DuringVoting,
			wantTie: "same timestamp as the block of the proposal, so which came first is unknown; it is after the Safe block 24019",
		},
		{
			name: "another chain, in a block with the number and time of the proposal's", chainID: ethrpc.ArbitrumOne, gnosisClock: true,
			block: 1201, safe: 1200, want: DuringVoting, wantTie: "same timestamp as the block of the proposal, so which came first is unknown",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, p := request(), progress()
			r.Proposal.Block, r.Proposal.SafeBlock = cmp.Or(test.proposal, proposalBlock), cmp.Or(test.safe, safeBlock)
			r.Proposal.Transaction.ChainID = new(big.Int).SetUint64(cmp.Or(test.chainID, ethrpc.Mainnet))
			r.Terms.RevealDeadline = cmp.Or(test.reveal, revealDeadline)
			p.Block, p.Terms, p.State, p.Arbitration = cmp.Or(test.now, latestGnosis), r.Terms, safenet.StatePending, nil
			if test.frozen != 0 {
				p.State, p.Arbitration = safenet.StateFrozen, &safenet.Arbitration{FrozenBlock: test.frozen, Outcome: safenet.OutcomePending}
			}
			if test.settled != 0 {
				p.State, p.Arbitration.Outcome, p.Arbitration.Record = safenet.StateResolvedDenied, safenet.OutcomeInsecure, &safenet.Record{Block: test.settled}
			}
			gnosis := &node{chainID: ethrpc.Gnosis, time: gnosisTime, latest: p.Block, startNonce: txNonce, executions: []execution{own(test.block)}}
			c := &Checker{Gnosis: gnosis.serve(t), Blocks: 100}
			switch test.chainID {
			case ethrpc.Gnosis:
				c.Safe = c.Gnosis
			case ethrpc.ArbitrumOne:
				clock := arbitrumTime
				if test.gnosisClock {
					clock = gnosisTime
				}
				c.Safe = (&node{chainID: ethrpc.ArbitrumOne, time: clock, latest: 30_000, startNonce: txNonce, executions: []execution{own(test.block)}}).serve(t)
			default:
				c.Safe = (&node{latest: 1000, startNonce: txNonce, executions: []execution{own(test.block)}}).serve(t)
			}
			got, err := c.Check(t.Context(), r, fixed(p))
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if got.Timing == nil || got.Timing.Value != test.want || got.Timing.Execution.Block != test.block {
				t.Fatalf("Check: got timing %+v, want %s for block %d", got.Timing, test.want, test.block)
			}
			summary := fmt.Sprintf("The Safe transaction was executed at %s, %s: the proposal was at %s, in Gnosis Chain block %d",
				got.Timing.Execution.Time.Format(time.RFC3339), test.want, got.Timing.Proposal.Time.Format(time.RFC3339), r.Proposal.Block)
			if test.want == BeforeProposal {
				summary += fmt.Sprintf(", and the execution is in or before the Safe block %d", r.Proposal.SafeBlock)
			}
			if !slices.Contains(got.Notes, summary+".") {
				t.Errorf("Check: got notes %q, want %q", got.Notes, summary+".")
			}
			var ties []string
			for _, note := range got.Notes {
				if strings.Contains(note, "placed") {
					ties = append(ties, note)
				}
			}
			if test.wantTie == "" && len(ties) != 0 || test.wantTie != "" && (len(ties) != 1 || !strings.Contains(ties[0], test.wantTie)) {
				t.Errorf("Check: got notes on ties %q, want one containing %q, if any", ties, test.wantTie)
			}
		})
	}
}

// TestCheckTimingAfterSnapshot checks that Check reads the request after the
// Safe's chain, and that an execution later than the block that it read the
// request at has an uncertain timing only while an event after that block can
// still come before it and change the timing. For a Safe on Gnosis Chain, with
// the execution in block 1214, the blocks' order decides: only a freeze or a
// settlement in a block after the one read and before 1214 can, and a deadline
// after the block read is placed by its number. On Ethereum Mainnet, the
// execution is in block 506, between the times of Gnosis Chain blocks 1214 and
// 1215, and the times decide.
func TestCheckTimingAfterSnapshot(t *testing.T) {
	for _, test := range []struct {
		name string
		// gnosis is whether the Safe is on Gnosis Chain. now is the block that the
		// request is read at, state its state there, and reveal its reveal deadline.
		gnosis      bool
		now, reveal uint64
		state       safenet.State
		want        string
		uncertain   bool
		// wantNote is in a note, if it is set.
		wantNote string
	}{
		{"read two blocks before the execution's", true, 1212, 1212, safenet.StatePending, AfterVoting, true, ""},
		// A freeze in block 1214 would be in the execution's block, which places the
		// execution before it.
		{"read the block before the execution's", true, 1213, 1212, safenet.StatePending, AfterVoting, false, ""},
		{"read at the execution's block", true, 1214, 1212, safenet.StatePending, AfterVoting, false, ""},
		{"read before the execution's block, settled", true, 1212, 1212, safenet.StateResolvedApproved, AfterVoting, false, ""},
		// The sentinels approved it before the reveal deadline, which the block that it
		// is read at hasn't reached, but the execution is in a later block.
		{"settled before a reveal deadline after the block read", true, 1212, 1213, safenet.StateResolvedApproved, AfterVoting, false, ""},
		{
			"a reveal deadline in the execution's block, after the block read", true, 1212, 1214, safenet.StatePending, DuringVoting, true,
			"The execution is in Gnosis Chain block 1214, the block of the reveal deadline, and is placed before the reveal deadline.",
		},
		{"on Ethereum Mainnet, read before the execution", false, 1212, 1212, safenet.StatePending, AfterVoting, true, ""},
		{"on Ethereum Mainnet, read before the execution, settled", false, 1212, 1212, safenet.StateResolvedApproved, AfterVoting, false, ""},
		// The time of the reveal deadline is unknown until it is reached.
		{"on Ethereum Mainnet, settled before a reveal deadline after the block read", false, 1212, 1213, safenet.StateResolvedApproved, DuringVoting, true, ""},
		{"on Ethereum Mainnet, read after the execution", false, 1215, 1212, safenet.StatePending, AfterVoting, false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, p := request(), progress()
			r.Terms.RevealDeadline = test.reveal
			p.Block, p.Terms, p.State, p.Arbitration = test.now, r.Terms, test.state, nil
			chain := &node{latest: 506, startNonce: txNonce, executions: []execution{own(506)}}
			note := fmt.Sprintf("The execution is later than Gnosis Chain block %d, at %s, which the request was read at", test.now, gnosisTime(test.now).Format(time.RFC3339))
			if test.gnosis {
				r.Proposal.SafeBlock = proposalBlock - 1
				r.Proposal.Transaction.ChainID = big.NewInt(ethrpc.Gnosis)
				chain = &node{chainID: ethrpc.Gnosis, time: gnosisTime, latest: 1214, startNonce: txNonce, executions: []execution{own(1214)}}
				note = fmt.Sprintf("The execution is in Gnosis Chain block 1214, and the request was read at block %d", test.now)
			}
			read := func(context.Context, ethrpc.Hash) (*safenet.Progress, error) {
				if chain.logsRead.Load() == 0 {
					t.Error("Check read the request before the Safe's logs")
				}
				return p, nil
			}
			got, err := checker(t, chain).Check(t.Context(), r, read)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if got.Timing == nil || got.Timing.Value != test.want || got.Timing.Uncertain != test.uncertain || got.StateBlock != test.now {
				t.Fatalf("Check: got timing %+v as of block %d, want %s as of block %d, uncertain %t", got.Timing, got.StateBlock, test.want, test.now, test.uncertain)
			}
			if has := slices.ContainsFunc(got.Notes, func(n string) bool { return strings.HasPrefix(n, note) }); has != test.uncertain {
				t.Errorf("Check: got notes %q, want one starting %q: %t", got.Notes, note, test.uncertain)
			}
			if test.wantNote != "" && !slices.Contains(got.Notes, test.wantNote) {
				t.Errorf("Check: got notes %q, want %q", got.Notes, test.wantNote)
			}
		})
	}
}

// TestCheckTimingCommitWindow checks that a pending request that Check read in
// the commit window is uncertain for an execution on Gnosis Chain only if a
// freeze can come in a block before the execution's. Reveals open in the block
// after the commit deadline, 1207, so the first freeze is in 1208, which is
// before an execution in 1209 and not before one in 1208, whose own block
// places it before the freeze. A request read after the commit deadline can
// freeze from the block after the one read.
func TestCheckTimingCommitWindow(t *testing.T) {
	for _, test := range []struct {
		name      string
		now, exec uint64
		uncertain bool
	}{
		{"read in the commit window, executed in it", 1203, 1206, false},
		{"executed in the commit deadline's block", 1203, commitDeadline, false},
		{"executed in the first block of the reveals", 1203, commitDeadline + 1, false},
		{"executed a block after the first of the reveals", 1203, commitDeadline + 2, true},
		{"read after the commit deadline, executed in the next block", 1209, 1210, false},
		{"read after the commit deadline, executed two blocks later", 1209, 1211, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, p := request(), progress()
			r.Proposal.SafeBlock = proposalBlock - 1
			r.Proposal.Transaction.ChainID = big.NewInt(ethrpc.Gnosis)
			p.Block, p.Terms, p.State, p.Arbitration = test.now, r.Terms, safenet.StatePending, nil
			chain := &node{chainID: ethrpc.Gnosis, time: gnosisTime, latest: test.exec, startNonce: txNonce, executions: []execution{own(test.exec)}}
			got, err := checker(t, chain).Check(t.Context(), r, fixed(p))
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if got.Timing == nil || got.Timing.Value != DuringVoting || got.Timing.Uncertain != test.uncertain {
				t.Errorf("Check read at block %d, execution in block %d, commit deadline %d: got timing %+v, want during-voting, uncertain %t",
					test.now, test.exec, commitDeadline, got.Timing, test.uncertain)
			}
		})
	}
}

// TestCheckTimingPoints checks the blocks and times of the events that Check
// places an execution against, with the deadlines that the latest Gnosis Chain
// block has reached.
func TestCheckTimingPoints(t *testing.T) {
	commit, reveal := gnosisTime(commitDeadline), gnosisTime(revealDeadline)
	settled := progress()
	pending := progress()
	pending.Block, pending.State, pending.Arbitration = 1210, safenet.StatePending, nil
	for _, test := range []struct {
		name     string
		progress *safenet.Progress
		want     *Timing
	}{
		{
			name: "settled", progress: settled,
			want: &Timing{
				Value:          DuringVoting,
				Execution:      Point{Block: 502, Time: blockTime(502)},
				Proposal:       Point{Block: proposalBlock, Time: gnosisTime(proposalBlock)},
				CommitDeadline: Deadline{Block: commitDeadline, Reached: true, Time: &commit},
				RevealDeadline: Deadline{Block: revealDeadline, Reached: true, Time: &reveal},
				Frozen:         &Point{Block: frozenBlock, Time: gnosisTime(frozenBlock)},
				Resolved:       &Resolution{Point: Point{Block: settledBlock, Time: gnosisTime(settledBlock)}, Outcome: safenet.OutcomeSecure},
				Now:            Point{Block: latestGnosis, Time: gnosisTime(latestGnosis)},
			},
		},
		{
			// Gnosis Chain block 1210 is before the reveal deadline.
			name: "voting", progress: pending,
			want: &Timing{
				Value:          DuringVoting,
				Execution:      Point{Block: 502, Time: blockTime(502)},
				Proposal:       Point{Block: proposalBlock, Time: gnosisTime(proposalBlock)},
				CommitDeadline: Deadline{Block: commitDeadline, Reached: true, Time: &commit},
				RevealDeadline: Deadline{Block: revealDeadline},
				Now:            Point{Block: 1210, Time: gnosisTime(1210)},
			},
		},
	} {
		chain := &node{latest: 1000, startNonce: txNonce, executions: []execution{own(502)}}
		got, err := checker(t, chain).Check(t.Context(), request(), fixed(test.progress))
		if err != nil {
			t.Fatalf("Check %s: %v", test.name, err)
		}
		g, _ := json.Marshal(got.Timing)
		w, _ := json.Marshal(test.want)
		if string(g) != string(w) || got.State != test.progress.State {
			t.Errorf("Check %s: got state %s and timing\n%s\nwant %s and\n%s", test.name, got.State, g, test.progress.State, w)
		}
	}
}

// TestCheckScanFails checks that a scan that the node refuses with a JSON-RPC
// error, as over its range limit, leaves the status unknown, with the node's
// error, while a result that isn't logs, such as null or a log without its
// block number, fails the check, and one without logs is an answer.
func TestCheckScanFails(t *testing.T) {
	chain := &node{latest: 1000, startNonce: txNonce, executions: []execution{own(502)}}
	chain.logsError = map[string]any{"code": -32602, "message": "block range too large"}
	got, err := checker(t, chain).Check(t.Context(), request(), fixed(progress()))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if x := got.Execution; x.Status != Unknown || x.Source != "" || x.Block != nil || !strings.Contains(x.Reason, "block range too large") {
		t.Errorf("Check: got execution %+v, want unknown, with the node's error", x)
	}
	for _, result := range []string{"null", "{}", "[null]", "[{}]"} {
		chain := &node{latest: 1000, startNonce: txNonce, executions: []execution{own(502)}, logsResult: json.RawMessage(result)}
		if got, err := checker(t, chain).Check(t.Context(), request(), fixed(progress())); err == nil {
			t.Errorf("Check with the eth_getLogs result %s: got execution %+v, want an error", result, got.Execution)
		}
	}
	chain = &node{latest: 1000, startNonce: txNonce, logsResult: json.RawMessage("[]")}
	if got, err := checker(t, chain).Check(t.Context(), request(), fixed(progress())); err != nil || got.Execution.Status != NotExecuted {
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
			got, err := checker(t, chain).Check(t.Context(), request(), fixed(progress()))
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
			c := checker(t, chain)
			c.Service = svc.serve(t)
			var warnings []string
			c.Warn = func(err error) {
				if strings.Contains(err.Error(), "Safe Transaction Service") {
					warnings = append(warnings, err.Error())
				}
			}
			got, err := c.Check(t.Context(), request(), fixed(progress()))
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
			got, err := checker(t, chain).Check(t.Context(), request(), fixed(progress()))
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
		// gnosisFailing are the methods that the Gnosis Chain node fails.
		gnosisFailing []string
		// service is the status that the service answers with, http.StatusOK for one
		// that names the execution, or 0 for no service.
		service int
	}{
		{name: "latest block", failing: []string{"eth_blockNumber"}},
		{name: "Gnosis Chain block time", gnosisFailing: []string{"eth_getBlockByNumber"}},
		{name: "nonce", failing: []string{"eth_call"}},
		{name: "logs", failing: []string{"eth_getLogs"}},
		{name: "execution time", failing: []string{"eth_getBlockByNumber"}},
		{name: "receipt", failing: []string{"eth_getTransactionReceipt"}, service: http.StatusOK},
		{name: "service", service: http.StatusInternalServerError},
		{name: "service rate limit", service: http.StatusTooManyRequests},
	} {
		t.Run(test.name, func(t *testing.T) {
			chain := &node{latest: 1000, startNonce: txNonce, executions: []execution{own(502)}, failing: test.failing}
			gnosis := &node{chainID: ethrpc.Gnosis, time: gnosisTime, latest: latestGnosis, failing: test.gnosisFailing}
			c := &Checker{Gnosis: gnosis.serve(t), Safe: chain.serve(t), Blocks: 100}
			switch test.service {
			case 0:
			case http.StatusOK:
				c.Service = (&service{transactions: map[ethrpc.Hash]map[string]any{ownHash: listing(own(502).txHash, 502)}}).serve(t)
			default:
				c.Service = (&service{status: test.service}).serve(t)
			}
			if _, err := c.Check(t.Context(), request(), fixed(progress())); !ethrpc.Transient(err) && !txservice.Unavailable(err) {
				t.Errorf("Check: got error %v, want one without an answer", err)
			}
		})
	}
}

func TestCheckErrors(t *testing.T) {
	otherChain, noNonce, early, beforeSafeBlock, afterSafeBlock := request(), request(), request(), request(), request()
	otherChain.Proposal.Transaction.ChainID = big.NewInt(ethrpc.Gnosis)
	noNonce.Proposal.Transaction.Nonce = nil
	early.Proposal.SafeBlock = 1001
	// Mainnet block 500 is before the proposal, and 501 after it.
	beforeSafeBlock.Proposal.SafeBlock, afterSafeBlock.Proposal.SafeBlock = 499, 501
	otherDeadline := progress()
	otherDeadline.Terms.RevealDeadline++
	for _, test := range []struct {
		name     string
		chain    *node
		request  *safenet.Request
		progress *safenet.Progress
		want     string
	}{
		{"another chain", &node{latest: 1000}, otherChain, progress(), "is for chain 100, not 1"},
		{"no nonce", &node{latest: 1000}, noNonce, progress(), "has no nonce"},
		{"Safe block after the latest", &node{latest: 1000}, early, progress(), "the latest block 1000 of chain 1 is before the Safe block 1001"},
		{"no code", &node{latest: 1000, noCode: true}, request(), progress(), "as a call to an account without code does, so it isn't a Safe"},
		{"other deadlines", &node{latest: 1000}, request(), otherDeadline, "the oracle has the deadlines 1207 and 1214 for request " + request().ID.String() + ", not 1207 and 1213"},
		{
			"execution after the Safe block, before the proposal", &node{latest: 1000, startNonce: txNonce, executions: []execution{own(500)}}, beforeSafeBlock, progress(),
			"the request's Safe block 499 isn't the last block of chain 1 before the proposal's time 2023-11-14T23:53:25Z: the execution in block 500 is at 2023-11-14T23:53:20Z",
		},
		{
			"execution in the Safe block, after the proposal", &node{latest: 1000, startNonce: txNonce, executions: []execution{own(501)}}, afterSafeBlock, progress(),
			"the request's Safe block 501 isn't the last block of chain 1 before the proposal's time",
		},
	} {
		if _, err := checker(t, test.chain).Check(t.Context(), test.request, fixed(test.progress)); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("Check %s: got error %v, want one containing %q", test.name, err, test.want)
		}
	}
}

// TestReportJSON checks the keys of the JSON of a report, which agents read,
// with null for an execution that there is none of, and of a timing, with null
// for an event that hasn't happened.
func TestReportJSON(t *testing.T) {
	report := &Report{
		Request: ethrpc.Hash{31: 1}, State: safenet.StateFrozen, StateBlock: latestGnosis, Safe: safe, ChainID: 1, SafeBlock: safeBlock, SafeTxHash: ownHash,
		Nonce:     Nonce{Transaction: big.NewInt(5), SafeAtSafeBlock: big.NewInt(5), SafeAtLatest: big.NewInt(5), LatestBlock: 1000},
		Execution: Execution{Status: NotExecuted, Source: SourceNonce, Reason: "r", Scanned: &executions.BlockRange{FromBlock: 500, ToBlock: 599}},
		Notes:     []string{NotEvidence},
	}
	got, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"request":"` + report.Request.String() + `","state":"FROZEN","stateBlock":2400,"safe":"` + safe.String() + `","chainId":1,"safeBlock":500,"safeTxHash":"` + ownHash.String() + `",` +
		`"nonce":{"transaction":5,"safeAtSafeBlock":5,"safeAtLatest":5,"latestBlock":1000,"stale":false},` +
		`"execution":{"status":"not-executed","txHash":null,"block":null,"blockTime":null,"success":null,"safeTxHash":null,"source":"nonce","reason":"r",` +
		`"service":null,"scanned":{"fromBlock":500,"toBlock":599}},"timing":null,"notes":["` + NotEvidence + `"]}`
	if string(got) != want {
		t.Errorf("JSON of a report:\ngot  %s\nwant %s", got, want)
	}

	at := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	timing := &Timing{
		Value: AfterResolution, Execution: Point{Block: 9, Time: at}, Proposal: Point{Block: 1, Time: at},
		CommitDeadline: Deadline{Block: 2, Reached: true, Time: &at}, RevealDeadline: Deadline{Block: 30},
		Resolved: &Resolution{Point: Point{Block: 4, Time: at}, Outcome: safenet.OutcomeOutOfScope}, Now: Point{Block: 5, Time: at},
	}
	if got, err = json.Marshal(timing); err != nil {
		t.Fatal(err)
	}
	point := func(block int) string { return fmt.Sprintf(`{"block":%d,"time":"2026-11-26T12:00:00Z"}`, block) }
	want = `{"value":"after-resolution","uncertain":false,"execution":` + point(9) + `,"proposal":` + point(1) + `,` +
		`"commitDeadline":{"block":2,"reached":true,"time":"2026-11-26T12:00:00Z"},"revealDeadline":{"block":30,"reached":false,"time":null},` +
		`"frozen":null,"resolved":{"block":4,"time":"2026-11-26T12:00:00Z","outcome":"out-of-scope"},"now":` + point(5) + `}`
	if string(got) != want {
		t.Errorf("JSON of a timing:\ngot  %s\nwant %s", got, want)
	}
}
