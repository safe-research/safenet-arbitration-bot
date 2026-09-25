package safenet

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

var update = flag.Bool("update", false, "update the golden files in testdata")

// fixture is a snapshot of the Gnosis Chain deployment as of a block: the call
// results, logs, and block headers that Pending and Request read.
//
// testdata/gnosis.json was recorded through a JSON-RPC proxy in front of
// https://rpc.gnosischain.com, while running `arbot pending` and `arbot info`
// for the requests in TestRequest, and for an unknown request ID, at the
// fixture's block.
type fixture struct {
	Block  ethrpc.BlockNumber `json:"block"`
	Calls  []fixtureCall      `json:"calls"`
	Logs   []ethrpc.Log       `json:"logs"`
	Blocks []ethrpc.Block     `json:"blocks"`
}

// fixtureCall is an eth_call and its result, or its error if it reverted.
type fixtureCall struct {
	To     ethrpc.Address `json:"to"`
	Data   ethrpc.Bytes   `json:"data"`
	Result ethrpc.Bytes   `json:"result"`
	Error  *ethrpc.Error  `json:"error"`
}

func loadFixture(t *testing.T) *fixture {
	t.Helper()
	data, err := os.ReadFile("testdata/gnosis.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	return &f
}

// open returns a Safenet that reads the default deployment from a node serving
// the fixture.
func (f *fixture) open(t *testing.T) *Safenet {
	t.Helper()
	return New(f.serve(t), DefaultOracle, DefaultConsensus)
}

// serve returns a client for a node that serves the fixture. A request that the
// fixture has no answer for fails the test.
func (f *fixture) serve(t *testing.T) *ethrpc.Client {
	t.Helper()
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
		result, rpcErr, err := f.handle(req.Method, req.Params)
		if err != nil {
			// The handler runs on the server's goroutine, where t.Fatal must not be called,
			// so it only reports the error.
			t.Errorf("%s: %v", req.Method, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
		if rpcErr != nil {
			response = map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": rpcErr}
		}
		json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)

	eth, err := ethrpc.NewClient(t.Context(), ethrpc.Gnosis, server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return eth
}

// handle answers a JSON-RPC request from the fixture. It returns an error for
// requests that the fixture has no answer for, including ones for blocks after
// the fixture's block.
func (f *fixture) handle(method string, params []json.RawMessage) (any, *ethrpc.Error, error) {
	switch method {
	case "eth_chainId":
		return "0x64", nil, nil
	case "eth_call":
		var call ethrpc.CallRequest
		var block ethrpc.BlockNumber
		if err := decodeParams(params, &call, &block); err != nil {
			return nil, nil, err
		}
		if block != f.Block {
			return nil, nil, fmt.Errorf("call at block %d, but the fixture is at block %d", block, f.Block)
		}
		for _, c := range f.Calls {
			if c.To == call.To && bytes.Equal(c.Data, call.Data) {
				if c.Error != nil {
					return nil, c.Error, nil
				}
				return c.Result, nil, nil
			}
		}
		return nil, nil, fmt.Errorf("no recorded call to %s with data %s", call.To, call.Data)
	case "eth_getLogs":
		var filter ethrpc.LogFilter
		if err := decodeParams(params, &filter); err != nil {
			return nil, nil, err
		}
		if filter.ToBlock > f.Block {
			return nil, nil, fmt.Errorf("logs up to block %d, but the fixture is at block %d", filter.ToBlock, f.Block)
		}
		logs := []ethrpc.Log{}
		for _, log := range f.Logs {
			if matches(filter, log) {
				logs = append(logs, log)
			}
		}
		return logs, nil, nil
	case "eth_getBlockByNumber":
		var number ethrpc.BlockNumber
		var full bool
		if err := decodeParams(params, &number, &full); err != nil {
			return nil, nil, err
		}
		for _, block := range f.Blocks {
			if block.Number == number {
				return block, nil, nil
			}
		}
		return nil, nil, fmt.Errorf("no recorded block %d", number)
	}
	return nil, nil, fmt.Errorf("unexpected method")
}

func decodeParams(params []json.RawMessage, values ...any) error {
	if len(params) != len(values) {
		return fmt.Errorf("got %d parameters, want %d", len(params), len(values))
	}
	for i, value := range values {
		if err := json.Unmarshal(params[i], value); err != nil {
			return fmt.Errorf("parameter %d: %w", i, err)
		}
	}
	return nil
}

// matches reports whether filter selects log.
func matches(filter ethrpc.LogFilter, log ethrpc.Log) bool {
	if log.BlockNumber < filter.FromBlock || log.BlockNumber > filter.ToBlock {
		return false
	}
	if len(filter.Addresses) > 0 && !slices.Contains(filter.Addresses, log.Address) {
		return false
	}
	for i, topics := range filter.Topics {
		if topics != nil && (i >= len(log.Topics) || !slices.Contains(topics, log.Topics[i])) {
			return false
		}
	}
	return true
}

// log returns the fixture's log of event whose first indexed topic is topic.
func (f *fixture) log(t *testing.T, event, topic ethrpc.Hash) *ethrpc.Log {
	t.Helper()
	for i, log := range f.Logs {
		if log.Topics[0] == event && log.Topics[1] == topic {
			return &f.Logs[i]
		}
	}
	t.Fatalf("fixture has no log of event %s with topic %s", event, topic)
	return nil
}

// removeLogs removes the fixture's logs of event whose first indexed topic is
// topic.
func (f *fixture) removeLogs(t *testing.T, event, topic ethrpc.Hash) {
	t.Helper()
	n := len(f.Logs)
	f.Logs = slices.DeleteFunc(f.Logs, func(log ethrpc.Log) bool {
		return log.Topics[0] == event && log.Topics[1] == topic
	})
	if len(f.Logs) == n {
		t.Fatalf("fixture has no log of event %s with topic %s", event, topic)
	}
}

// Requests in the fixture.
var (
	frozenID              = mustParseHash("0x062be5de4a4ca7123b0894a48807d09ca0e445c282e48dc3601e141bd32b48cb")
	frozenSafeTxHash      = mustParseHash("0xea6f04a866f107949f795b60dbcbe9563ab67ee232bbb14c7b85644dc9fce221")
	insecureID            = mustParseHash("0x3c25a142c4a7e1eaac635f2f271acc4d7d9ed9109c54321b25556ca68e5dfa6b")
	secureID              = mustParseHash("0x68f9225d10c8f8bf8b8a13821d7bcfe70a4f65c63121d154ef13c01bcc17c36c")
	outOfScopeID          = mustParseHash("0xdcc1fd09f0f6e226179f2bbaa852d45f1df3dbf5f18916b100165511e8888eed")
	arbitrationTimedOutID = mustParseHash("0x9638bb644c8626d39168db94daca90cfb2908e174c5fb6284adfbdd547812945")
	unanimousID           = mustParseHash("0x6873954d892a95127ddca0f8972902134b0d509e1d40190854b9ba3b1e7e8fda")
	requestTimedOutID     = mustParseHash("0xbf8a51ff22a488e078e455e1a7579f5112b14b54cc4ca1cb7bed6efa6e8ede38")
)

func TestPending(t *testing.T) {
	f := loadFixture(t)
	disputes, err := f.open(t).Pending(t.Context(), f.Block)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}

	if len(disputes) != 38 {
		t.Errorf("Pending: got %d disputes, want 38", len(disputes))
	}
	if want := (Dispute{RequestID: frozenID, FrozenBlock: 48385788, Deadline: 48436188}); len(disputes) > 0 && disputes[0] != want {
		t.Errorf("Pending: got first dispute %+v, want %+v", disputes[0], want)
	}
	if !slices.IsSortedFunc(disputes, func(a, b Dispute) int { return cmp.Compare(a.Deadline, b.Deadline) }) {
		t.Error("Pending: disputes are not ordered by deadline")
	}
	for _, d := range disputes {
		if d.Deadline < uint64(f.Block) {
			t.Errorf("Pending: dispute %s has deadline %d, before block %d", d.RequestID, d.Deadline, f.Block)
		}
	}

	// These requests were frozen and then settled within the scanned blocks.
	for _, id := range []ethrpc.Hash{insecureID, secureID, outOfScopeID} {
		f.log(t, disputeTriggeredEvent, id)
		if slices.ContainsFunc(disputes, func(d Dispute) bool { return d.RequestID == id }) {
			t.Errorf("Pending: got settled dispute %s", id)
		}
	}
}

func TestPendingNone(t *testing.T) {
	f := loadFixture(t)
	f.Logs = nil
	disputes, err := f.open(t).Pending(t.Context(), f.Block)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if disputes == nil || len(disputes) != 0 {
		t.Errorf("Pending: got %#v, want an empty slice", disputes)
	}
}

func TestRequest(t *testing.T) {
	tests := []struct {
		name  string
		id    ethrpc.Hash
		state State
		// outcome is empty for a request without an arbitration.
		outcome Outcome
		votes   int
	}{
		{"frozen", frozenID, StateFrozen, OutcomePending, 2},
		{"insecure", insecureID, StateResolvedDenied, OutcomeInsecure, 2},
		{"secure", secureID, StateResolvedApproved, OutcomeSecure, 3},
		{"out of scope", outOfScopeID, StateTimedOut, OutcomeOutOfScope, 2},
		{"arbitration timed out", arbitrationTimedOutID, StateTimedOut, OutcomeTimedOut, 3},
		{"unanimous", unanimousID, StateResolvedApproved, "", 1},
		{"no reveals", requestTimedOutID, StateTimedOut, "", 1},
	}

	f := loadFixture(t)
	sn := f.open(t)
	var requests []*Request
	for _, test := range tests {
		request, err := sn.Request(t.Context(), test.id, f.Block)
		if err != nil {
			t.Errorf("Request(%s): %v", test.name, err)
			continue
		}
		requests = append(requests, request)

		if request.State != test.state {
			t.Errorf("Request(%s): got state %s, want %s", test.name, request.State, test.state)
		}
		var outcome Outcome
		if request.Arbitration != nil {
			outcome = request.Arbitration.Outcome
		}
		if outcome != test.outcome {
			t.Errorf("Request(%s): got outcome %q, want %q", test.name, outcome, test.outcome)
		}
		if len(request.Votes) != test.votes {
			t.Errorf("Request(%s): got %d votes, want %d", test.name, len(request.Votes), test.votes)
		}
	}

	// Encode the requests as jq formats them, which `just check-testdata` checks.
	var got bytes.Buffer
	encoder := json.NewEncoder(&got)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(requests); err != nil {
		t.Fatal(err)
	}
	golden := "testdata/requests.golden.json"
	if *update {
		if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("Request: JSON differs from %s; run the tests with -update to see the difference", golden)
	}
}

func TestRequestNotFound(t *testing.T) {
	f := loadFixture(t)
	id := ethrpc.Hash{31: 1}
	if _, err := f.open(t).Request(t.Context(), id, f.Block); !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("Request: got error %v, want %v", err, ErrRequestNotFound)
	}
}

func TestRequestWrongConsensus(t *testing.T) {
	f := loadFixture(t)
	sn := New(f.serve(t), DefaultOracle, ethrpc.Address{19: 1})
	wantError(t, sn, frozenID, f.Block, "has PROPOSER")
}

func TestRequestProposalMismatch(t *testing.T) {
	t.Run("request ID", func(t *testing.T) {
		f := loadFixture(t)
		// Changing the epoch changes the request ID that the proposal hashes to.
		log := f.log(t, transactionProposedEvent, frozenSafeTxHash)
		log.Data[31] ^= 1
		wantError(t, f.open(t), frozenID, f.Block, "no proposal in block 48385778 hashes to the request ID")
	})
	t.Run("Safe transaction hash", func(t *testing.T) {
		f := loadFixture(t)
		// Changing the Safe transaction's nonce, its last field, changes its hash but
		// not the logged hash, which the request ID commits to.
		log := f.log(t, transactionProposedEvent, frozenSafeTxHash)
		tx := binary.BigEndian.Uint64(log.Data[2*32+24 : 3*32])
		log.Data[tx+12*32-1] ^= 1
		wantError(t, f.open(t), frozenID, f.Block, "Safe transaction hashes to")
	})
}

func TestRequestVoteMismatch(t *testing.T) {
	f := loadFixture(t)
	f.removeLogs(t, revealedEvent, frozenID)
	wantError(t, f.open(t), frozenID, f.Block, "logs have 2 commits, 0 reveals")
}

func TestRequestArbitrationMismatch(t *testing.T) {
	t.Run("not frozen", func(t *testing.T) {
		f := loadFixture(t)
		f.removeLogs(t, disputeTriggeredEvent, frozenID)
		wantError(t, f.open(t), frozenID, f.Block, "no DisputeTriggered log")
	})
	t.Run("not settled", func(t *testing.T) {
		f := loadFixture(t)
		f.removeLogs(t, disputeResolvedEvent, insecureID)
		wantError(t, f.open(t), insecureID, f.Block, "is RESOLVED_DENIED, but its arbitration logs have outcome pending")
	})
	t.Run("wrong outcome", func(t *testing.T) {
		f := loadFixture(t)
		// Change the ruling of the RESOLVED_DENIED request from insecure to secure.
		log := f.log(t, disputeResolvedEvent, insecureID)
		log.Data[31] = byte(StateResolvedApproved)
		wantError(t, f.open(t), insecureID, f.Block, "is RESOLVED_DENIED, but its arbitration logs have outcome secure")
	})
}

// wantError checks that Request fails for id with an error containing want.
func wantError(t *testing.T, sn *Safenet, id ethrpc.Hash, block ethrpc.BlockNumber, want string) {
	t.Helper()
	_, err := sn.Request(t.Context(), id, block)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Request: got error %v, want one containing %q", err, want)
	}
}
