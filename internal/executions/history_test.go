package executions

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

// listing is an executed Safe transaction as a Safe Transaction Service lists
// it. A listing without a transaction hash has neither it nor a block.
type listing struct {
	tx         *safenet.SafeTransaction
	safeTxHash ethrpc.Hash
	txHash     ethrpc.Hash
	block      uint64
}

// listed returns the listing of tx, executed in the onchain transaction hash at
// block.
func listed(tx *safenet.SafeTransaction, hash ethrpc.Hash, block uint64) listing {
	return listing{tx, tx.Hash(), hash, block}
}

// historyService starts a Safe Transaction Service that knows the Safe and
// lists its executions, as the service lists them, or fails to if listings is
// nil, and returns a client for it. It has no transactions by hash.
func historyService(t *testing.T, listings []listing) *txservice.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/safes/"+safe.String()+"/" {
			json.NewEncoder(w).Encode(map[string]any{"address": safe})
			return
		}
		if r.URL.Path != "/api/v1/safes/"+safe.String()+"/multisig-transactions/" {
			http.NotFound(w, r)
			return
		}
		if listings == nil {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		results := []map[string]any{}
		for _, l := range listings {
			tx := l.tx
			result := map[string]any{
				"safe": tx.Safe, "to": tx.To, "value": tx.Value.String(), "data": tx.Data,
				"operation": uint8(tx.Operation), "safeTxGas": tx.SafeTxGas.Uint64(), "baseGas": tx.BaseGas.Uint64(),
				"gasPrice": tx.GasPrice.String(), "gasToken": tx.GasToken, "refundReceiver": tx.RefundReceiver,
				"nonce": tx.Nonce.Uint64(), "safeTxHash": l.safeTxHash, "transactionHash": l.txHash, "blockNumber": l.block,
				"isExecuted": true,
			}
			if l.txHash == (ethrpc.Hash{}) {
				result["transactionHash"], result["blockNumber"] = nil, nil
			}
			results = append(results, result)
		}
		json.NewEncoder(w).Encode(map[string]any{"count": len(results), "results": results})
	}))
	t.Cleanup(server.Close)
	return txservice.NewClient(server.URL, ethrpc.Mainnet)
}

// executed returns what Find reports about an execution of tx in the onchain
// transaction hash at block, from source.
func executed(tx *safenet.SafeTransaction, hash ethrpc.Hash, block uint64, source Source) Execution {
	return Execution{block, hash, 0, tx.Hash(), true, big.NewInt(9), source, tx}
}

// checkExecutions reports the executions in got that differ from want. They are
// compared as JSON, since a computed big.Int zero isn't DeepEqual to
// big.NewInt(0).
func checkExecutions(t *testing.T, got, want []Execution) {
	t.Helper()
	for i := range max(len(got), len(want)) {
		var g, w []byte
		if i < len(got) {
			g, _ = json.Marshal(got[i])
		}
		if i < len(want) {
			w, _ = json.Marshal(want[i])
		}
		if string(g) != string(w) {
			t.Errorf("execution %d:\ngot  %s\nwant %s", i, g, w)
		}
	}
}

// checkRejected reports the rejected executions in got that differ from want,
// which has the start of the reason of each by Safe transaction hash, in order.
func checkRejected(t *testing.T, got []Rejected, want []ethrpc.Hash, reasons []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("rejected: got %+v, want %d", got, len(want))
		return
	}
	for i, r := range got {
		if r.SafeTxHash != want[i] || !strings.Contains(r.Reason, reasons[i]) {
			t.Errorf("rejected %d: got %s with reason %q, want %s with one containing %q", i, r.SafeTxHash, r.Reason, want[i], reasons[i])
		}
	}
}

func TestFindAll(t *testing.T) {
	const last = 200
	// Four executions, at the nonces 0 to 3, and a Safe whose nonce is 4 at the
	// last block. The service lists three of them, with one after them, and the one
	// at nonce 2 is in the block of the one at nonce 3, in another transaction.
	viaService := transaction("service", 0)
	viaEvent := transaction("event", 1)
	viaCalldata := transaction("calldata", 2)
	viaService2 := transaction("service again", 3)
	later := transaction("later", 4)
	c := &chain{
		nonce: big.NewInt(4),
		txs: map[ethrpc.Hash]ethrpc.Transaction{
			hash(3): {To: &safe, Input: execCall(viaCalldata)},
		},
		logs: []ethrpc.Log{
			executionLog(executionSuccessEvent, viaService.Hash(), hash(1), false, 100, 0),
			eventLog(viaEvent, hash(2), 110, 0),
			executionLog(executionSuccessEvent, viaEvent.Hash(), hash(2), true, 110, 1),
			executionLog(executionSuccessEvent, viaCalldata.Hash(), hash(3), true, 120, 0),
			executionLog(executionSuccessEvent, viaService2.Hash(), hash(4), true, 120, 1),
			executionLog(executionSuccessEvent, later.Hash(), hash(5), true, last+1, 0),
		},
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{
		listed(viaService, hash(1), 100), listed(viaEvent, hash(2), 110), listed(viaService2, hash(4), 120), listed(later, hash(5), last+1),
	})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }

	got, err := f.FindAll(t.Context(), safe, last)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	// A transaction that the service lists is taken from it, unless the Safe's
	// SafeMultiSigTransaction event has it, and one that it doesn't list is decoded
	// from the chain, with the nonce worked out from the Safe's.
	executionWithEvent := executed(viaEvent, hash(2), 110, SourceEvent)
	executionWithEvent.LogIndex = 1
	executionViaService2 := executed(viaService2, hash(4), 120, SourceService)
	executionViaService2.LogIndex = 1
	checkExecutions(t, got.Executions, []Execution{
		executed(viaService, hash(1), 100, SourceService),
		executionWithEvent,
		executed(viaCalldata, hash(3), 120, SourceCalldata),
		executionViaService2,
	})
	if !got.Complete() || got.Nonce.Int64() != 4 || len(got.Missing) != 0 || got.Rejected == nil || len(got.Rejected) != 0 {
		t.Errorf("FindAll: got %+v, complete %t; want nonce 4, none missing or rejected, complete", got, got.Complete())
	}
	// It reads the logs of each block that the service lists up to the last block.
	if want := [][2]uint64{{100, 100}, {110, 110}, {120, 120}}; !reflect.DeepEqual(c.ranges, want) {
		t.Errorf("FindAll: read the logs of the blocks %v, want %v", c.ranges, want)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: got %v, want none", warnings)
	}
}

func TestFindAllMissing(t *testing.T) {
	const last = 200
	// Five executions, at the nonces 0 to 4, of which the service only lists those
	// at the nonces 1 and 3 rightly.
	var txs []*safenet.SafeTransaction
	c := &chain{nonce: big.NewInt(5), txs: make(map[ethrpc.Hash]ethrpc.Transaction)}
	for n := range 5 {
		tx := transaction("calldata", int64(n))
		txs = append(txs, tx)
		c.txs[hash(byte(n+1))] = ethrpc.Transaction{To: &safe, Input: execCall(tx)}
		c.logs = append(c.logs, executionLog(executionSuccessEvent, tx.Hash(), hash(byte(n+1)), true, uint64(100+10*n), 0))
	}
	tampered := *txs[4]
	tampered.Data = []byte("not what was executed")
	never := transaction("never", 2)
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{
		listed(txs[1], hash(2), 110),
		listed(txs[3], hash(4), 130),
		// A transaction whose fields don't have its hash, so it says nothing about its
		// nonce, but whose block has the execution that it misreports.
		{&tampered, txs[4].Hash(), hash(5), 140},
		// A transaction that wasn't executed at the block that the service says.
		listed(never, hash(9), 150),
	})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }

	got, err := f.FindAll(t.Context(), safe, last)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	// The executions in the blocks that the service names are found, and the
	// misreported one is decoded from the chain. Those in the other blocks aren't
	// found, so their nonces are missing.
	checkExecutions(t, got.Executions, []Execution{
		executed(txs[1], hash(2), 110, SourceService),
		executed(txs[3], hash(4), 130, SourceService),
		executed(txs[4], hash(5), 140, SourceCalldata),
	})
	if want := []NonceRange{{0, 0}, {2, 2}}; got.Complete() || !reflect.DeepEqual(got.Missing, want) {
		t.Errorf("FindAll: got missing %v, complete %t; want %v, incomplete", got.Missing, got.Complete(), want)
	}
	checkRejected(t, got.Rejected, []ethrpc.Hash{txs[4].Hash(), never.Hash()},
		[]string{"fields don't hash", "the Safe logged no execution of it in block 150"})
	// The block without the Safe's executions is read again before the listing is
	// rejected.
	reads := 0
	for _, r := range c.ranges {
		if r == [2]uint64{150, 150} {
			reads++
		}
	}
	if reads != 2 {
		t.Errorf("FindAll: read block 150 %d times, want 2", reads)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: got %v, want none", warnings)
	}
}

func TestFindAllRejected(t *testing.T) {
	const last = 200
	// The Safe's nonce is 7 at the last block. The executions at the nonces 0 and 1
	// are confirmed, 2 to 4 aren't executed, and 5 and 6 are executed but listed
	// wrongly.
	ok0, ok1 := transaction("first", 0), transaction("second", 1)
	wrongHash := transaction("wrong hash", 3)
	noLog := transaction("no log", 4)
	wrongTx := transaction("wrong transaction", 5)
	noTx := transaction("no transaction", 6)
	otherSafe := transaction("other Safe", 6)
	otherSafe.Safe = relayer
	later := transaction("later", 7)

	foreign := executionLog(executionSuccessEvent, ok0.Hash(), hash(1), true, 120, 0)
	foreign.Address = relayer
	c := &chain{
		nonce: big.NewInt(7),
		logs: []ethrpc.Log{
			// The two first executions are in one onchain transaction, a batch, with a log
			// of another contract before them. Safe 1.3.0 has the hash in the data.
			foreign,
			executionLog(executionFailureEvent, ok1.Hash(), hash(1), false, 120, 1),
			executionLog(executionSuccessEvent, ok0.Hash(), hash(1), true, 120, 2),
			executionLog(executionSuccessEvent, wrongTx.Hash(), hash(15), true, 150, 0),
			executionLog(executionSuccessEvent, noTx.Hash(), hash(16), true, 160, 0),
		},
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{
		listed(ok0, hash(1), 120),
		listed(ok1, hash(1), 120),
		{wrongHash, hash(9), hash(3), 130},
		listed(noLog, hash(4), 140),
		listed(wrongTx, hash(5), 150),
		{noTx, noTx.Hash(), ethrpc.Hash{}, 0},
		listed(otherSafe, hash(6), 160),
		listed(later, hash(7), last+1),
	})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }

	got, err := f.FindAll(t.Context(), safe, last)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	failure := executed(ok1, hash(1), 120, SourceService)
	failure.LogIndex, failure.Success = 1, false
	success := executed(ok0, hash(1), 120, SourceService)
	success.LogIndex = 2
	// The executions listed with the wrong transaction or none are found all the
	// same, in the blocks that the service names.
	checkExecutions(t, got.Executions, []Execution{
		failure, success,
		executed(wrongTx, hash(15), 150, SourceService),
		executed(noTx, hash(16), 160, SourceService),
	})
	checkRejected(t, got.Rejected,
		[]ethrpc.Hash{hash(9), noLog.Hash(), wrongTx.Hash(), noTx.Hash(), otherSafe.Hash()},
		[]string{
			"fields don't hash", "no execution of it in block 140", "in the transaction " + hash(15).String(),
			"no onchain transaction or block", "for the Safe " + relayer.String(),
		})
	if got.Complete() || got.Nonce.Int64() != 7 || !reflect.DeepEqual(got.Missing, []NonceRange{{2, 4}}) {
		t.Errorf("FindAll: got nonce %s with missing %v, complete %t; want 7 with 2 to 4 missing, incomplete", got.Nonce, got.Missing, got.Complete())
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: got %v, want none", warnings)
	}
}

func TestFindAllIncomplete(t *testing.T) {
	// A Safe whose nonce counts an execution that the service doesn't list, and a
	// service that knows the Safe but lists none of its executions.
	tx := transaction("calldata", 0)
	c := &chain{
		nonce: big.NewInt(2),
		txs:   map[ethrpc.Hash]ethrpc.Transaction{hash(1): {To: &safe, Input: execCall(tx)}},
		logs:  []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	// No block is named, so none is read, and both nonces are missing.
	if got.Complete() || len(got.Executions) != 0 || !reflect.DeepEqual(got.Missing, []NonceRange{{0, 1}}) || len(c.ranges) != 0 {
		t.Errorf("FindAll: got %+v, complete %t, with the logs of %v read; want no executions or reads, both nonces missing", got, got.Complete(), c.ranges)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: got %v, want none", warnings)
	}

	// A Safe that executed nothing has a complete history without any reads.
	c = &chain{nonce: big.NewInt(0)}
	f = &Finder{Eth: c.serve(t), Service: historyService(t, []listing{})}
	if got, err := f.FindAll(t.Context(), safe, 200); err != nil || !got.Complete() || len(got.Executions) != 0 || len(c.ranges) != 0 {
		t.Errorf("FindAll of no executions: got %+v, error %v, and log reads %v; want an empty, complete history without reads", got, err, c.ranges)
	}
}

func TestFindAllUnknownSafe(t *testing.T) {
	// The service lists nothing for a Safe that it doesn't know, as it does for one
	// without executions, and only its endpoint for the Safe says that it doesn't
	// know it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/safes/"+safe.String()+"/" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"count": 0, "next": nil, "previous": nil, "results": []any{}})
	}))
	t.Cleanup(server.Close)
	c := &chain{nonce: big.NewInt(3)}
	f := &Finder{Eth: c.serve(t), Service: txservice.NewClient(server.URL, ethrpc.Mainnet)}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	// The Safe's nonce shows what the listing leaves out.
	if got.Complete() || len(got.Executions) != 0 || !reflect.DeepEqual(got.Missing, []NonceRange{{0, 2}}) {
		t.Errorf("FindAll: got %+v; want no executions, nonces 0 to 2 missing", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "doesn't know "+safe.String()) {
		t.Errorf("warnings: got %v, want one about the unknown Safe", warnings)
	}
}

func TestFindAllErrors(t *testing.T) {
	c := &chain{nonce: big.NewInt(1)}
	eth := c.serve(t)
	if _, err := (&Finder{Eth: eth}).FindAll(t.Context(), safe, 200); err == nil || !strings.Contains(err.Error(), "needs the Safe Transaction Service") {
		t.Errorf("FindAll without a service: got error %v, want one about the service", err)
	}
	f := &Finder{Eth: eth, Service: historyService(t, nil)}
	if _, err := f.FindAll(t.Context(), safe, 200); err == nil || !strings.Contains(err.Error(), "listing the executions") || !strings.Contains(err.Error(), "500") {
		t.Errorf("FindAll with a failing service: got error %v, want one about listing the executions", err)
	}
	f = &Finder{Eth: (&chain{}).serve(t), Service: historyService(t, []listing{})}
	if _, err := f.FindAll(t.Context(), safe, 200); err == nil || !strings.Contains(err.Error(), "reading the nonce") {
		t.Errorf("FindAll without the nonce: got error %v, want one about reading it", err)
	}
}

func TestFindAllNodeFailure(t *testing.T) {
	// The node fails to serve the logs of the second block that the service names,
	// as a node does that rate limits the client midway. That says nothing about
	// the history, so FindAll fails rather than reject the listing.
	ok, failing := transaction("first", 0), transaction("second", 1)
	c := &chain{
		nonce: big.NewInt(2),
		logs: []ethrpc.Log{
			executionLog(executionSuccessEvent, ok.Hash(), hash(1), true, 100, 0),
			executionLog(executionSuccessEvent, failing.Hash(), hash(2), true, 110, 0),
		},
		failingBlocks: []uint64{110},
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{listed(ok, hash(1), 100), listed(failing, hash(2), 110)})}
	if got, err := f.FindAll(t.Context(), safe, 200); !ethrpc.Transient(err) {
		t.Errorf("FindAll with a failing block: got %+v, %v, want a transient error", got, err)
	}
}

func TestFindNotASafe(t *testing.T) {
	// A call to an account without code returns no data, which says that it isn't a
	// Safe. Find only needs the nonce for some sources, so it warns, and FindAll
	// fails, since it needs the nonce.
	tx := transaction("first", 0)
	c := &chain{noCode: true, logs: []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)}}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{listed(tx, hash(1), 100)})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	const want = "the call returned no data, as a call to an account without code does, so it isn't a Safe"
	if _, err := f.Find(t.Context(), safe, 100, 200); err != nil || len(warnings) != 1 || !strings.Contains(warnings[0].Error(), want) {
		t.Errorf("Find: got error %v and warnings %v, want one warning containing %q", err, warnings, want)
	}
	if _, err := f.FindAll(t.Context(), safe, 200); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("FindAll: got error %v, want one containing %q", err, want)
	}
}

func TestMissing(t *testing.T) {
	executions := []Execution{
		{Transaction: transaction("", 1)}, {Transaction: transaction("", 2)}, {Transaction: transaction("", 5)},
		{Transaction: transaction("", 5)}, {Transaction: transaction("", 9)}, {},
	}
	if got, want := missing(executions, 8), []NonceRange{{0, 0}, {3, 4}, {6, 7}}; !reflect.DeepEqual(got, want) {
		t.Errorf("missing: got %v, want %v", got, want)
	}
	if got := missing(nil, 0); got == nil || len(got) != 0 {
		t.Errorf("missing with nonce 0: got %v, want none", got)
	}
}
