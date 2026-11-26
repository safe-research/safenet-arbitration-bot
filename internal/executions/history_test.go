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
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

// listing is an executed Safe transaction as a Safe Transaction Service lists
// it. A zero transaction hash or block is one that the listing doesn't have.
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
// nil, and returns a client for it. It says that the transaction with the hash
// creation created the Safe, if given. It has no transactions by hash, but an
// about page.
func historyService(t *testing.T, listings []listing, creation ...ethrpc.Hash) *txservice.Client {
	t.Helper()
	return historyServiceWith(t, listings, nil, creation...)
}

// historyServiceWith is historyService with the transactions by hash of
// transactions.
func historyServiceWith(t *testing.T, listings []listing, transactions map[ethrpc.Hash]*safenet.SafeTransaction, creation ...ethrpc.Hash) *txservice.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/about/" {
			json.NewEncoder(w).Encode(map[string]any{"name": "Safe Transaction Service"})
			return
		}
		if id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/multisig-transactions/"); ok {
			h, err := ethrpc.ParseHash(strings.TrimSuffix(id, "/"))
			if tx := transactions[h]; tx != nil && err == nil {
				json.NewEncoder(w).Encode(map[string]any{
					"safe": tx.Safe, "to": tx.To, "value": tx.Value.String(), "data": tx.Data,
					"operation": uint8(tx.Operation), "safeTxGas": tx.SafeTxGas.Uint64(), "baseGas": tx.BaseGas.Uint64(),
					"gasPrice": tx.GasPrice.String(), "gasToken": tx.GasToken, "refundReceiver": tx.RefundReceiver,
					"nonce": tx.Nonce.Uint64(),
				})
				return
			}
		}
		if r.URL.Path == "/api/v1/safes/"+safe.String()+"/creation/" && len(creation) > 0 {
			json.NewEncoder(w).Encode(map[string]any{"transactionHash": creation[0], "creator": relayer})
			return
		}
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
				result["transactionHash"] = nil
			}
			if l.block == 0 {
				result["blockNumber"] = nil
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
	if !got.Complete() || got.Nonce.Int64() != 4 || len(got.Missing) != 0 || got.Rejected == nil || len(got.Rejected) != 0 || got.Creation != nil ||
		!reflect.DeepEqual(got.Scanned, []BlockRange{{110, 120}}) {
		t.Errorf("FindAll: got %+v, complete %t; want nonce 4, blocks 110 to 120 scanned, none missing or rejected, complete", got, got.Complete())
	}
	// It reads the logs of each block that the service lists up to the last block,
	// then those of the gap where the execution at nonce 2 must be, since its nonce
	// is only known once it is decoded.
	if want := [][2]uint64{{100, 100}, {110, 110}, {120, 120}, {110, 120}}; !reflect.DeepEqual(c.ranges, want) {
		t.Errorf("FindAll: read the logs of the blocks %v, want %v", c.ranges, want)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: got %v, want none", warnings)
	}
}

func TestFindAllMissing(t *testing.T) {
	const last = 200
	// Five executions, at the nonces 0 to 4, of a Safe deployed at block 90, of
	// which the service only lists those at the nonces 1 and 3 rightly.
	var txs []*safenet.SafeTransaction
	c := &chain{nonce: big.NewInt(5), deployed: 90, txs: make(map[ethrpc.Hash]ethrpc.Transaction)}
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
	var want []Execution
	for n, tx := range txs {
		source := SourceCalldata
		if n == 1 || n == 3 {
			source = SourceService
		}
		want = append(want, executed(tx, hash(byte(n+1)), uint64(100+10*n), source))
	}
	checkExecutions(t, got.Executions, want)
	// The missing executions are between those with the nonces before and after
	// them, after the deployment for the first, and up to the last block for the
	// last.
	scanned := []BlockRange{{90, 110}, {110, 130}, {130, last}}
	if !got.Complete() || !reflect.DeepEqual(got.Scanned, scanned) {
		t.Errorf("FindAll: got scanned %v, missing %v, complete %t; want %v, complete", got.Scanned, got.Missing, got.Complete(), scanned)
	}
	// The first scan starts at the block that the Safe got its code in, which is
	// its creation's, without a transaction.
	if want := (&Creation{Block: 90}); !reflect.DeepEqual(got.Creation, want) {
		t.Errorf("FindAll: got the creation %+v, want %+v", got.Creation, want)
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

func TestFindAllMisreportedBeforeMissing(t *testing.T) {
	const last = 200
	// Four executions, at the nonces 0 to 3. The service misreports the fields of
	// the one at nonce 1, and doesn't list the one at nonce 2 after it, whose block
	// the node fails to scan, so the nonce worked out from the Safe's is wrong for
	// the misreported one.
	var txs []*safenet.SafeTransaction
	c := &chain{nonce: big.NewInt(4), txs: make(map[ethrpc.Hash]ethrpc.Transaction), failingBlocks: []uint64{120}}
	for n := range 4 {
		tx := transaction("calldata", int64(n))
		txs = append(txs, tx)
		c.txs[hash(byte(n+1))] = ethrpc.Transaction{To: &safe, Input: execCall(tx)}
		c.logs = append(c.logs, executionLog(executionSuccessEvent, tx.Hash(), hash(byte(n+1)), true, uint64(100+10*n), 0))
	}
	tampered := *txs[1]
	tampered.Data = []byte("not what was executed")
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{
		listed(txs[0], hash(1), 100),
		{&tampered, txs[1].Hash(), hash(2), 110},
		listed(txs[3], hash(4), 130),
	})}
	f.Warn = func(error) {}
	got, err := f.FindAll(t.Context(), safe, last)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	// The nonce of the misreported one is worked out from the Safe's at the end of
	// its block.
	checkExecutions(t, got.Executions, []Execution{
		executed(txs[0], hash(1), 100, SourceService),
		executed(txs[1], hash(2), 110, SourceCalldata),
		executed(txs[3], hash(4), 130, SourceService),
	})
	if want := []NonceRange{{2, 2}}; !reflect.DeepEqual(got.Missing, want) {
		t.Errorf("FindAll: got missing %v, want %v", got.Missing, want)
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
	// The execution listed without its transaction is in the block that the listing
	// for another Safe names, so only the gap before it is scanned.
	if want := []BlockRange{{120, 150}}; !reflect.DeepEqual(got.Scanned, want) {
		t.Errorf("FindAll: got scanned %v, want %v", got.Scanned, want)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: got %v, want none", warnings)
	}
}

func TestFindAllNonceSetBack(t *testing.T) {
	// The execution at nonce 1 is a delegate call that sets the Safe's nonce back
	// to 0, so nonces 0 and 1 are used twice. The service lists all but the second
	// at nonce 0, so every nonce below the Safe's is used.
	txs := []*safenet.SafeTransaction{
		transaction("first", 0), transaction("reset", 1), transaction("again", 0), transaction("again", 1), transaction("last", 2),
	}
	c := &chain{nonce: big.NewInt(3)}
	var listings []listing
	for i, tx := range txs {
		block := uint64(100 + 10*i)
		c.logs = append(c.logs, executionLog(executionSuccessEvent, tx.Hash(), hash(byte(i+1)), true, block, 0))
		if i != 2 {
			listings = append(listings, listed(tx, hash(byte(i+1)), block))
		}
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, listings)}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	if got.Complete() || len(got.Missing) != 0 {
		t.Errorf("FindAll: got %+v, complete %t; want none missing, incomplete", got, got.Complete())
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "set the Safe's nonce back") {
		t.Errorf("warnings: got %v, want one about the nonce set back", warnings)
	}
}

func TestFindAllNonceReusedInOneTransaction(t *testing.T) {
	// The execution at nonce 0 is a delegate call that sets the Safe's nonce back
	// to 0 and executes another transaction at nonce 0, which logs first. Every
	// nonce below the Safe's, 1, is used, and the nonce at the end of the block is
	// one above the highest, but nonce 0 is used twice.
	outer, nested := transaction("outer", 0), transaction("nested", 0)
	outer.Operation = safenet.OperationDelegateCall
	c := &chain{nonce: big.NewInt(1), logs: []ethrpc.Log{
		executionLog(executionSuccessEvent, nested.Hash(), hash(1), true, 100, 0),
		executionLog(executionSuccessEvent, outer.Hash(), hash(1), true, 100, 1),
	}}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{listed(nested, hash(1), 100), listed(outer, hash(1), 100)})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	if got.Complete() || len(got.Executions) != 2 || len(got.Missing) != 0 {
		t.Errorf("FindAll: got %+v, complete %t; want two executions, none missing, incomplete", got, got.Complete())
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "set the Safe's nonce back") {
		t.Errorf("warnings: got %v, want one about the nonce set back", warnings)
	}
}

func TestFindAllIncomplete(t *testing.T) {
	// A Safe whose nonce counts an execution that its logs don't have, such as one
	// in a block that the node misses, and a service that lists none.
	tx := transaction("calldata", 0)
	c := &chain{
		nonce: big.NewInt(2), deployed: 50,
		txs:  map[ethrpc.Hash]ethrpc.Transaction{hash(1): {To: &safe, Input: execCall(tx)}},
		logs: []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	// The whole history is scanned. The execution's nonce is worked out from the
	// Safe's, which is wrong, so its calldata doesn't check out, and both nonces
	// are missing.
	if got.Complete() || len(got.Executions) != 1 || got.Executions[0].Transaction != nil ||
		!reflect.DeepEqual(got.Scanned, []BlockRange{{50, 200}}) || !reflect.DeepEqual(got.Missing, []NonceRange{{0, 1}}) {
		t.Errorf("FindAll: got %+v, complete %t; want one unknown execution, from scanning blocks 50 to 200, both nonces missing", got, got.Complete())
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: got %v, want none", warnings)
	}

	// A Safe that executed nothing has a complete history without any scans.
	c = &chain{nonce: big.NewInt(0)}
	f = &Finder{Eth: c.serve(t), Service: historyService(t, []listing{})}
	if got, err := f.FindAll(t.Context(), safe, 200); err != nil || !got.Complete() || len(got.Executions) != 0 || got.Scanned == nil || len(got.Scanned) != 0 || len(c.ranges) != 0 {
		t.Errorf("FindAll of no executions: got %+v, error %v, and log reads %v; want an empty, complete history without reads", got, err, c.ranges)
	}
}

func TestFindAllUnknownSafe(t *testing.T) {
	// The service lists nothing for a Safe that it doesn't know, as it does for one
	// without executions, and only its endpoint for the Safe says that it doesn't
	// know it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/safes/"+safe.String()+"/") && !strings.HasSuffix(r.URL.Path, "/multisig-transactions/") {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"count": 0, "next": nil, "previous": nil, "results": []any{}})
	}))
	t.Cleanup(server.Close)
	first, second := transaction("first", 0), transaction("second", 1)
	c := &chain{
		nonce: big.NewInt(2), deployed: 50,
		txs: map[ethrpc.Hash]ethrpc.Transaction{hash(1): {To: &safe, Input: execCall(first)}, hash(2): {To: &safe, Input: execCall(second)}},
		logs: []ethrpc.Log{
			executionLog(executionSuccessEvent, first.Hash(), hash(1), true, 100, 0),
			executionLog(executionSuccessEvent, second.Hash(), hash(2), true, 110, 0),
		},
	}
	f := &Finder{Eth: c.serve(t), Service: txservice.NewClient(server.URL, ethrpc.Mainnet)}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	// The Safe's nonce shows what the listing leaves out, and the scan from its
	// deployment finds it.
	checkExecutions(t, got.Executions, []Execution{
		executed(first, hash(1), 100, SourceCalldata), executed(second, hash(2), 110, SourceCalldata),
	})
	if !got.Complete() || !reflect.DeepEqual(got.Scanned, []BlockRange{{50, 200}}) {
		t.Errorf("FindAll: got %+v; want both executions, from scanning blocks 50 to 200, complete", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "doesn't know "+safe.String()) {
		t.Errorf("warnings: got %v, want one about the unknown Safe", warnings)
	}
}
func TestFindAllWithoutDeployment(t *testing.T) {
	// A node that has no code for the Safe, such as one that doesn't serve the code
	// of old blocks, leaves the first executions unfound, with a warning.
	tx := transaction("calldata", 1)
	c := &chain{
		nonce: big.NewInt(2),
		logs:  []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{listed(tx, hash(1), 100)})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	if got.Complete() || len(got.Executions) != 1 || len(got.Scanned) != 0 || !reflect.DeepEqual(got.Missing, []NonceRange{{0, 0}}) {
		t.Errorf("FindAll: got %+v; want one execution, no scans, nonce 0 missing", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "finding the block that") || !strings.Contains(warnings[0].Error(), "has no code at block 100") {
		t.Errorf("warnings: got %v, want one about finding the deployment", warnings)
	}
}

func TestFindAllFromCreation(t *testing.T) {
	// The scan for the first executions starts at the Safe's creation, which the
	// service reports and the chain confirms, without searching for its code.
	tx := transaction("calldata", 1)
	first := transaction("calldata", 0)
	creation := hash(7)
	c := &chain{
		nonce: big.NewInt(2), deployed: 80,
		txs: map[ethrpc.Hash]ethrpc.Transaction{hash(2): {To: &safe, Input: execCall(first)}},
		logs: []ethrpc.Log{
			executionLog(executionSuccessEvent, first.Hash(), hash(2), true, 90, 0),
			executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0),
		},
		// The Safe is created by a contract creation transaction.
		receipts: map[ethrpc.Hash]ethrpc.Receipt{hash(7): {TransactionHash: hash(7), BlockNumber: 80, Status: 1, ContractAddress: &safe}},
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{listed(tx, hash(1), 100)}, hash(7))}
	got, err := f.FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	if !got.Complete() || len(got.Executions) != 2 || !reflect.DeepEqual(got.Scanned, []BlockRange{{80, 100}}) ||
		!reflect.DeepEqual(got.Creation, &Creation{80, &creation, nil}) {
		t.Errorf("FindAll: got %+v; want two executions, from scanning blocks 80 to 100, and the creation at block 80", got)
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

	// The creation's receipt, or the code that confirms its block, that the node
	// fails to serve fails FindAll.
	created := map[ethrpc.Hash]ethrpc.Receipt{hash(7): {TransactionHash: hash(7), BlockNumber: 100, Status: 1}}
	for _, method := range []string{"eth_getTransactionReceipt", "eth_getCode"} {
		c = &chain{nonce: big.NewInt(0), failing: map[string]int{method: http.StatusInternalServerError}, receipts: created}
		f = &Finder{Eth: c.serve(t), Service: historyService(t, []listing{}, hash(7))}
		if _, err := f.FindAll(t.Context(), safe, 200); !ethrpc.Transient(err) || !strings.Contains(err.Error(), "creation") {
			t.Errorf("FindAll with %s failing: got error %v, want a transient error about the creation", method, err)
		}
	}
}

func TestFindAllNonceUnanswered(t *testing.T) {
	// Four executions, at the nonces 0 to 3, of which the service lists the first
	// and the last. The others are in block 110, whose state the node doesn't keep,
	// or whose nonce it fails to read in time, which leaves out their calldata with
	// one warning, so they come from the service, which has them by hash.
	var txs []*safenet.SafeTransaction
	for n := range 4 {
		txs = append(txs, transaction("calldata", int64(n)))
	}
	for _, rpcErr := range []ethrpc.Error{missingState, nodeTimeout} {
		c := &chain{nonce: big.NewInt(4), txs: make(map[ethrpc.Hash]ethrpc.Transaction), erringCalls: map[uint64]ethrpc.Error{110: rpcErr}}
		var want []Execution
		// The blocks and log indexes of the executions.
		for n, at := range [][2]uint64{{100, 0}, {110, 0}, {110, 1}, {120, 0}} {
			c.txs[hash(byte(n+1))] = ethrpc.Transaction{To: &safe, Input: execCall(txs[n])}
			c.logs = append(c.logs, executionLog(executionSuccessEvent, txs[n].Hash(), hash(byte(n+1)), true, at[0], at[1]))
			x := executed(txs[n], hash(byte(n+1)), at[0], SourceService)
			x.LogIndex = at[1]
			want = append(want, x)
		}
		listings := []listing{listed(txs[0], hash(1), 100), listed(txs[3], hash(4), 120)}
		byHash := map[ethrpc.Hash]*safenet.SafeTransaction{txs[1].Hash(): txs[1], txs[2].Hash(): txs[2]}
		f := &Finder{Eth: c.serve(t), Service: historyServiceWith(t, listings, byHash)}
		var warnings []error
		f.Warn = func(err error) { warnings = append(warnings, err) }
		got, err := f.FindAll(t.Context(), safe, 200)
		// The one at nonce 1 is a delegate call, which could have changed the Safe's
		// nonce, so without its nonce at block 110, the history isn't complete. A read
		// that timed out says nothing about the nonce, though, so FindAll fails.
		wantWarnings := []string{"reading the nonce of " + safe.String() + " at block 110", "at the end of 1 blocks with a delegate call is unknown"}
		if ethrpc.Transient(&rpcErr) {
			if !ethrpc.Transient(err) || !strings.Contains(err.Error(), rpcErr.Message) {
				t.Errorf("%v: FindAll got %+v, %v; want the node's error", &rpcErr, got, err)
			}
			wantWarnings = wantWarnings[:1]
		} else {
			if err != nil {
				t.Fatalf("%v: FindAll: %v", &rpcErr, err)
			}
			checkExecutions(t, got.Executions, want)
			if got.Complete() {
				t.Errorf("%v: FindAll: got %+v, want an incomplete history", &rpcErr, got)
			}
		}
		ok := len(warnings) == len(wantWarnings)
		for i := 0; ok && i < len(warnings); i++ {
			ok = strings.Contains(warnings[i].Error(), wantWarnings[i])
		}
		if !ok {
			t.Errorf("%v: warnings %v, want %q", &rpcErr, warnings, wantWarnings)
		}

		// Without one of them in the service, FindAll fails, since the calldata might
		// have it.
		delete(byHash, txs[2].Hash())
		f = &Finder{Eth: f.Eth, Service: historyServiceWith(t, listings, byHash), Warn: func(error) {}}
		if got, err := f.FindAll(t.Context(), safe, 200); err == nil || !strings.Contains(err.Error(), rpcErr.Message) || ethrpc.Transient(err) != ethrpc.Transient(&rpcErr) {
			t.Errorf("%v without the third transaction in the service: FindAll got %+v, %v, want the node's error", &rpcErr, got, err)
		}
	}
}

func TestFindNested(t *testing.T) {
	// The execution at nonce 1 executes the one at nonce 2 on the Safe itself, so
	// the nested one logs first, and the nonces that their positions give are each
	// other's. The outer one is in the calldata, and the nested one only in a
	// trace.
	first, nested := transaction("first", 0), transaction("nested", 2)
	outer := transaction("", 1)
	outer.To, outer.Data, outer.Operation = safe, execCall(nested), safenet.OperationCall
	c := &chain{
		nonce: big.NewInt(3),
		txs: map[ethrpc.Hash]ethrpc.Transaction{
			hash(1): {To: &safe, Input: execCall(first)}, hash(2): {To: &safe, Input: execCall(outer)},
		},
		traces: map[ethrpc.Hash]any{hash(2): map[string]any{"type": "CALL", "to": safe, "input": execCall(outer), "calls": []any{
			map[string]any{"type": "CALL", "to": safe, "input": execCall(nested)},
		}}},
		logs: []ethrpc.Log{
			executionLog(executionSuccessEvent, first.Hash(), hash(1), true, 100, 0),
			executionLog(executionSuccessEvent, nested.Hash(), hash(2), true, 110, 0),
			executionLog(executionSuccessEvent, outer.Hash(), hash(2), true, 110, 1),
		},
	}
	eth := c.serve(t)
	executionOfOuter := executed(outer, hash(2), 110, SourceCalldata)
	executionOfOuter.LogIndex = 1
	want := []Execution{
		executed(first, hash(1), 100, SourceCalldata), executed(nested, hash(2), 110, SourceTrace), executionOfOuter,
	}
	got, err := (&Finder{Eth: eth}).Find(t.Context(), safe, 0, 200)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	checkExecutions(t, got, want)

	// FindAll finds them in the gap that the service leaves, and works out their
	// nonces from the Safe's at the end of their block.
	want[0].Source = SourceService
	h, err := (&Finder{Eth: eth, Service: historyService(t, []listing{listed(first, hash(1), 100)})}).FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	checkExecutions(t, h.Executions, want)
	if !h.Complete() {
		t.Errorf("FindAll: got %+v, want a complete history", h)
	}

	// With the nested one listed too, the outer one is decoded first, trying the
	// nonce that its position gives, which is the nested one's, before its own.
	want[1].Source = SourceService
	h, err = (&Finder{Eth: eth, Service: historyService(t, []listing{listed(first, hash(1), 100), listed(nested, hash(2), 110)})}).FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll with the nested one listed: %v", err)
	}
	checkExecutions(t, h.Executions, want)
}

func TestFindAllRepeatedCalldata(t *testing.T) {
	// One onchain transaction makes four calls to execTransaction, of A at nonce 0,
	// which executes B at 1 and C at 2 on the Safe itself, then of A again at 3,
	// with the same calldata. B and C log first, and the service lists them, so A
	// at nonce 0, at the third position, is the first to be decoded from the trace,
	// trying nonce 2 first: the first call of A matches A at 3, and the last one
	// must still match A at 0.
	a, b, c := transaction("a", 0), transaction("b", 1), transaction("c", 2)
	again := *a
	again.Nonce = big.NewInt(3)
	var calls []any
	for _, tx := range []*safenet.SafeTransaction{a, b, c, &again} {
		calls = append(calls, map[string]any{"type": "CALL", "to": safe, "input": execCall(tx)})
	}
	node := &chain{
		nonce:  big.NewInt(4),
		txs:    map[ethrpc.Hash]ethrpc.Transaction{hash(1): {To: &relayer, Input: ethrpc.Bytes{1}}},
		traces: map[ethrpc.Hash]any{hash(1): map[string]any{"type": "CALL", "to": relayer, "input": "0x01", "calls": calls}},
	}
	for n, tx := range []*safenet.SafeTransaction{b, c, a, &again} {
		node.logs = append(node.logs, executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 110, uint64(n)))
	}
	f := &Finder{Eth: node.serve(t), Service: historyService(t, []listing{listed(b, hash(1), 110), listed(c, hash(1), 110)})}
	h, err := f.FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	want := []Execution{
		executed(b, hash(1), 110, SourceService), executed(c, hash(1), 110, SourceService),
		executed(a, hash(1), 110, SourceTrace), executed(&again, hash(1), 110, SourceTrace),
	}
	for n := range want {
		want[n].LogIndex = uint64(n)
	}
	checkExecutions(t, h.Executions, want)
}

func TestDecodeCostPerExecution(t *testing.T) {
	// The nonces that a block's executions may have used are worked out once for
	// the block, each execution tries the nonce that its position gives first, and
	// each call of a trace the nonce after the last match, and stops at its match,
	// so an execution costs as much to decode however many its block has: in their
	// own onchain transactions, which the calldata has, or in one, which only a
	// trace has. The cost is counted in allocations, which each nonce worked out or
	// tried makes, from a decoder that has only read the Safe's nonce. As in
	// decodeSources, every execution gets its nonces, even one whose trace is
	// cached.
	eth := (&chain{}).serve(t)
	allocs := func(k int, trace bool) float64 {
		var executions []Execution
		var inputs []ethrpc.Bytes
		for j := range k {
			tx := transaction("batched", int64(j))
			e := Execution{Block: 100, TxHash: hash(1), SafeTxHash: tx.Hash()}
			if !trace {
				e.TxHash = ethrpc.Hash{0: 0xee, 31: byte(j)}
			}
			executions, inputs = append(executions, e), append(inputs, execCall(tx))
		}
		decode := func() (found int) {
			d := &decoder{
				Finder: &Finder{Eth: eth}, safe: safe, to: 200, executions: executions,
				nexts: map[uint64]*big.Int{200: big.NewInt(int64(k))},
			}
			for j := range k {
				nonces, _ := d.nonces(t.Context(), j)
				switch {
				case !trace:
					found += len(d.byHash(j, inputs[j:j+1], nonces))
				case j == 0:
					found += len(d.byHash(j, inputs, nonces))
				}
			}
			return found
		}
		if found := decode(); found != k {
			t.Fatalf("trace %t: found %d of %d executions", trace, found, k)
		}
		return testing.AllocsPerRun(5, func() { decode() }) / float64(k)
	}
	for _, trace := range []bool{false, true} {
		if one, many := allocs(1, trace), allocs(64, trace); many > 2*one {
			t.Errorf("trace %t: %.1f allocations for each of 64 executions in a block, want about the %.1f of one", trace, many, one)
		}
	}
}

func TestFindAllNonceAfterDelegateCall(t *testing.T) {
	// The execution at nonce 1 in block 110 is a delegate call. In the first case,
	// it sets the nonce back to 1, which the service hides by leaving out the
	// execution that uses it again, so only the Safe's nonce at the end of the
	// block shows it.
	callOnly := ethrpc.MustParseAddress("0x9641d764fc13c8B624c04430C7356C1C7C8102e2")
	for _, test := range []struct {
		name string
		// again is whether nonce 1 is used again in block 120, and to the account that
		// the delegate call is to.
		again bool
		to    ethrpc.Address
		// singleton is the singleton at the end of block 110, if it isn't Safe 1.3.0.
		singleton ethrpc.Address
		complete  bool
		warning   string
	}{
		{"set back", true, target, ethrpc.Address{}, false, "the Safe's nonce at the end of block 110, which has a delegate call, is 1 rather than 2"},
		{"kept", false, target, ethrpc.Address{}, true, ""},
		// The delegate call replaced the singleton, whose nonce() may be anything.
		{"singleton replaced", false, target, target, false, "the Safe's singleton at the end of block 110, which has a delegate call, is " + target.String()},
		// The Safe ran on the official 1.1.1 singleton then, before it upgraded.
		{"older singleton", false, target, ethrpc.MustParseAddress("0x34CfAC646f301356fAa8B21e94227e3583Fe3F5F"), true, ""},
		// MultiSendCallOnly makes no delegate calls, so the nonce isn't read, which the
		// node would fail to.
		{"MultiSendCallOnly", false, callOnly, ethrpc.Address{}, true, ""},
	} {
		txs := []*safenet.SafeTransaction{transaction("first", 0), transaction("delegate call", 1), transaction("last", 2)}
		txs[1].To = test.to
		c := &chain{nonce: big.NewInt(3)}
		if test.singleton != (ethrpc.Address{}) {
			c.singletons = map[uint64]ethrpc.Address{110: test.singleton}
		}
		if test.to == callOnly {
			c.erringCalls = map[uint64]ethrpc.Error{110: missingState}
		}
		var listings []listing
		for n, block := range []uint64{100, 110, 130} {
			c.logs = append(c.logs, executionLog(executionSuccessEvent, txs[n].Hash(), hash(byte(n+1)), true, block, 0))
			listings = append(listings, listed(txs[n], hash(byte(n+1)), block))
		}
		if test.again {
			again := transaction("again", 1)
			c.logs = append(c.logs, executionLog(executionSuccessEvent, again.Hash(), hash(9), true, 120, 0))
		}
		f := &Finder{Eth: c.serve(t), Service: historyService(t, listings)}
		var warnings []error
		f.Warn = func(err error) { warnings = append(warnings, err) }
		got, err := f.FindAll(t.Context(), safe, 200)
		if err != nil {
			t.Fatalf("%s: FindAll: %v", test.name, err)
		}
		if got.Complete() != test.complete || len(got.Missing) != 0 {
			t.Errorf("%s: FindAll got %+v, complete %t; want none missing, complete %t", test.name, got, got.Complete(), test.complete)
		}
		if test.warning == "" && len(warnings) != 0 || test.warning != "" && (len(warnings) != 1 || !strings.Contains(warnings[0].Error(), test.warning)) {
			t.Errorf("%s: warnings %v, want %q", test.name, warnings, test.warning)
		}
	}
}

func TestOlderSingleton(t *testing.T) {
	// The 1.1.1 singleton is only official on Ethereum Mainnet and Gnosis Chain,
	// and a § 2.1 singleton isn't one of the older ones.
	singleton111 := ethrpc.MustParseAddress("0x34CfAC646f301356fAa8B21e94227e3583Fe3F5F")
	singleton130 := ethrpc.MustParseAddress("0xd9Db270c1B5E3Bd161E8c8503c55cEABeE709552")
	for _, test := range []struct {
		address ethrpc.Address
		chainID uint64
		want    bool
	}{
		{singleton111, ethrpc.Mainnet, true}, {singleton111, ethrpc.Gnosis, true}, {singleton111, ethrpc.ArbitrumOne, false},
		{singleton130, ethrpc.Mainnet, false},
	} {
		if got := olderSingleton(test.address, test.chainID); got != test.want {
			t.Errorf("olderSingleton(%s, %d) = %t, want %t", test.address, test.chainID, got, test.want)
		}
	}
}

func TestFindAllSingletonUnanswered(t *testing.T) {
	// The execution at nonce 1 in block 110 is a delegate call that keeps the
	// nonce, but the node doesn't answer for the Safe's singleton there, which
	// leaves the history incomplete, unless the read timed out, which fails
	// FindAll.
	txs := []*safenet.SafeTransaction{transaction("first", 0), transaction("delegate call", 1), transaction("last", 2)}
	for _, rpcErr := range []ethrpc.Error{missingState, nodeTimeout} {
		c := &chain{nonce: big.NewInt(3), erring: map[string]ethrpc.Error{"eth_getStorageAt": rpcErr}}
		var listings []listing
		for n, block := range []uint64{100, 110, 130} {
			c.logs = append(c.logs, executionLog(executionSuccessEvent, txs[n].Hash(), hash(byte(n+1)), true, block, 0))
			listings = append(listings, listed(txs[n], hash(byte(n+1)), block))
		}
		var warnings []error
		f := &Finder{Eth: c.serve(t), Service: historyService(t, listings), Warn: func(err error) { warnings = append(warnings, err) }}
		got, err := f.FindAll(t.Context(), safe, 200)
		if ethrpc.Transient(&rpcErr) {
			if !ethrpc.Transient(err) || !strings.Contains(err.Error(), rpcErr.Message) {
				t.Errorf("%v: FindAll got %+v, %v; want the node's error", &rpcErr, got, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%v: FindAll: %v", &rpcErr, err)
		}
		if got.Complete() || len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "at the end of 1 blocks with a delegate call is unknown") {
			t.Errorf("%v: FindAll got %+v, warnings %v; want an incomplete history, with a warning about block 110", &rpcErr, got, warnings)
		}
	}
}

func TestFindAllUnknownDelegateCall(t *testing.T) {
	// The execution in block 110 is a delegate call that sets the nonce back to 1,
	// and the one in block 120 uses nonce 1 again. The service lists neither, and
	// the nonce at the end of block 110 doesn't give the hash of the delegate
	// call's calldata, so it is unknown, and every nonce has a known execution.
	first, reset, again, last := transaction("first", 0), transaction("reset", 1), transaction("again", 1), transaction("last", 2)
	c := &chain{nonce: big.NewInt(3), txs: map[ethrpc.Hash]ethrpc.Transaction{
		hash(2): {To: &safe, Input: execCall(reset)}, hash(3): {To: &safe, Input: execCall(again)},
	}}
	for n, tx := range []*safenet.SafeTransaction{first, reset, again, last} {
		c.logs = append(c.logs, executionLog(executionSuccessEvent, tx.Hash(), hash(byte(n+1)), true, uint64(100+10*n), 0))
	}
	var warnings []error
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{listed(first, hash(1), 100), listed(last, hash(4), 130)})}
	f.Warn = func(err error) { warnings = append(warnings, err) }
	h, err := f.FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	if len(h.Executions) != 4 || h.Executions[1].Transaction != nil || len(h.Missing) != 0 || h.Complete() {
		t.Errorf("FindAll got %+v, complete %t; want 4 executions, the second unknown, none missing, incomplete", h, h.Complete())
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "an execution found is unknown") {
		t.Errorf("warnings %v, want one about the unknown execution", warnings)
	}
}

func TestFindAllGapScanFailure(t *testing.T) {
	// The service lists the first and third executions, and the node fails to serve
	// the blocks between them, as a public node times out a long scan. The second
	// nonce stays missing, with a warning, and the range isn't scanned.
	first, second, third := transaction("first", 0), transaction("second", 1), transaction("third", 2)
	c := &chain{
		nonce: big.NewInt(3),
		logs: []ethrpc.Log{
			executionLog(executionSuccessEvent, first.Hash(), hash(1), true, 100, 0),
			executionLog(executionSuccessEvent, second.Hash(), hash(2), true, 105, 0),
			executionLog(executionSuccessEvent, third.Hash(), hash(3), true, 110, 0),
		},
		failingBlocks: []uint64{105},
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{listed(first, hash(1), 100), listed(third, hash(3), 110)})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	if got.Complete() || len(got.Executions) != 2 || len(got.Scanned) != 0 || !reflect.DeepEqual(got.Missing, []NonceRange{{1, 1}}) {
		t.Errorf("FindAll: got %+v; want two executions, nothing scanned, nonce 1 missing", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "scanning blocks 100 to 110") {
		t.Errorf("warnings: got %v, want one about the scan", warnings)
	}
}

func TestFindAllEventNonce(t *testing.T) {
	// A SafeL2's SafeMultiSigTransaction event has the nonce of an execution that
	// the service misreports, so the gap scan for the next nonce starts at its
	// block rather than at that of the execution before it.
	const last = 200
	txs := []*safenet.SafeTransaction{transaction("first", 0), transaction("second", 1), transaction("third", 2), transaction("fourth", 3)}
	c := &chain{nonce: big.NewInt(4)}
	for n, block := range []uint64{100, 120, 130, 140} {
		if n == 1 || n == 2 {
			c.logs = append(c.logs, eventLog(txs[n], hash(byte(n+1)), block, 0))
		}
		c.logs = append(c.logs, executionLog(executionSuccessEvent, txs[n].Hash(), hash(byte(n+1)), true, block, 1))
	}
	tampered := *txs[1]
	tampered.Data = []byte("not what was executed")
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{
		listed(txs[0], hash(1), 100), {&tampered, txs[1].Hash(), hash(2), 120}, listed(txs[3], hash(4), 140),
	})}
	got, err := f.FindAll(t.Context(), safe, last)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	var want []Execution
	for n, block := range []uint64{100, 120, 130, 140} {
		source := SourceService
		if n == 1 || n == 2 {
			source = SourceEvent
		}
		x := executed(txs[n], hash(byte(n+1)), block, source)
		x.LogIndex = 1
		want = append(want, x)
	}
	checkExecutions(t, got.Executions, want)
	if !got.Complete() || !reflect.DeepEqual(got.Scanned, []BlockRange{{120, 140}}) {
		t.Errorf("FindAll: got %+v; want the four executions, from scanning blocks 120 to 140, complete", got)
	}
	checkRejected(t, got.Rejected, []ethrpc.Hash{txs[1].Hash()}, []string{"fields don't hash"})
}

func TestFindAllReusedNonce(t *testing.T) {
	// A delegate call reset the Safe's nonce, so two executions use nonce 1. The
	// one at nonce 2, which the service doesn't list, is after the first of them,
	// so the gap scan for it starts there.
	const last = 200
	txs := []*safenet.SafeTransaction{transaction("first", 0), transaction("second", 1), transaction("third", 2), transaction("again", 1), transaction("fourth", 3)}
	blocks := []uint64{100, 110, 120, 150, 160}
	c := &chain{nonce: big.NewInt(4)}
	var listings []listing
	for i, tx := range txs {
		if i == 2 {
			c.logs = append(c.logs, eventLog(tx, hash(byte(i+1)), blocks[i], 0))
		} else {
			listings = append(listings, listed(tx, hash(byte(i+1)), blocks[i]))
		}
		c.logs = append(c.logs, executionLog(executionSuccessEvent, tx.Hash(), hash(byte(i+1)), true, blocks[i], 1))
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, listings)}
	got, err := f.FindAll(t.Context(), safe, last)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	// They are all found, but the reused nonce shows that the nonce doesn't count
	// them.
	if got.Complete() || len(got.Executions) != 5 || !reflect.DeepEqual(got.Scanned, []BlockRange{{110, 160}}) {
		t.Errorf("FindAll: got %+v; want the five executions, from scanning blocks 110 to 160, incomplete", got)
	}
}

func TestFindNotASafe(t *testing.T) {
	// A call to an account without code returns no data, which says that it isn't a
	// Safe. Find only needs the nonce for some sources, so it warns, and FindAll
	// fails, since it needs the nonce.
	tx := transaction("first", 0)
	c := &chain{empty: true, logs: []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)}}
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

func TestFindAllUnlistedInListedBlock(t *testing.T) {
	const last = 200
	// Three executions, at the nonces 0 to 2. Block 100 has the ones at nonces 0
	// and 1, of which the service only lists the first, and the service doesn't
	// list block 110, which has the one at nonce 2, and which the node fails to
	// scan.
	var txs []*safenet.SafeTransaction
	c := &chain{nonce: big.NewInt(3), txs: make(map[ethrpc.Hash]ethrpc.Transaction), failingBlocks: []uint64{110}}
	for n := range 3 {
		tx := transaction("calldata", int64(n))
		txs = append(txs, tx)
		c.txs[hash(byte(n+1))] = ethrpc.Transaction{To: &safe, Input: execCall(tx)}
	}
	c.logs = []ethrpc.Log{
		executionLog(executionSuccessEvent, txs[0].Hash(), hash(1), true, 100, 0),
		executionLog(executionSuccessEvent, txs[1].Hash(), hash(2), true, 100, 1),
		executionLog(executionSuccessEvent, txs[2].Hash(), hash(3), true, 110, 0),
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{listed(txs[0], hash(1), 100)})}
	f.Warn = func(error) {}
	got, err := f.FindAll(t.Context(), safe, last)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	second := executed(txs[1], hash(2), 100, SourceCalldata)
	second.LogIndex = 1
	checkExecutions(t, got.Executions, []Execution{executed(txs[0], hash(1), 100, SourceService), second})
	if want := []NonceRange{{2, 2}}; !reflect.DeepEqual(got.Missing, want) {
		t.Errorf("FindAll: got missing %v, want %v", got.Missing, want)
	}
}

func TestFindAllListedWithoutTxHash(t *testing.T) {
	const last = 200
	// The service lists two executions with their blocks but not their onchain
	// transactions, one of them after the last block.
	first, later := transaction("first", 0), transaction("later", 1)
	c := &chain{
		nonce: big.NewInt(1),
		logs: []ethrpc.Log{
			executionLog(executionSuccessEvent, first.Hash(), hash(1), true, 170, 0),
			executionLog(executionSuccessEvent, later.Hash(), hash(2), true, last+1, 0),
		},
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{
		{first, first.Hash(), ethrpc.Hash{}, 170},
		{later, later.Hash(), ethrpc.Hash{}, last + 1},
	})}
	got, err := f.FindAll(t.Context(), safe, last)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	// The block is read all the same, and only the listing up to the last block is
	// rejected.
	checkExecutions(t, got.Executions, []Execution{executed(first, hash(1), 170, SourceService)})
	checkRejected(t, got.Rejected, []ethrpc.Hash{first.Hash()}, []string{"no onchain transaction"})
}

func TestCreationFromService(t *testing.T) {
	const last = 200
	factory130 := ethrpc.MustParseAddress("0xa6B71E26C5e0845f74c812102Ca7114b6a896AB2")
	factory141 := ethrpc.MustParseAddress("0x4e1DCf7AD4e460CfD30791CCC4F9c8a4f820ec67")
	factory111 := ethrpc.MustParseAddress("0x76E2cFc1F5Fa8F6a5b3fC4c8F4788F0116861F9B")
	// proxyCreation returns a ProxyCreation log of emitter for proxy, with the
	// proxy as a topic like 1.4.1, or in the data like 1.3.0, and proxyCreation111
	// one with the proxy alone, like 1.0.0 and 1.1.1.
	proxyCreationEvent := solabi.Event("ProxyCreation(address,address)")
	proxyCreation111 := func(emitter, proxy ethrpc.Address) ethrpc.Log {
		return ethrpc.Log{Address: emitter, Topics: []ethrpc.Hash{solabi.Event("ProxyCreation(address)")}, Data: solabi.Encode(proxy)}
	}
	proxyCreation := func(emitter, proxy ethrpc.Address, indexed bool) ethrpc.Log {
		if !indexed {
			return ethrpc.Log{Address: emitter, Topics: []ethrpc.Hash{proxyCreationEvent}, Data: solabi.Encode(proxy, target)}
		}
		var topic ethrpc.Hash
		copy(topic[12:], proxy[:])
		return ethrpc.Log{Address: emitter, Topics: []ethrpc.Hash{proxyCreationEvent, topic}, Data: make([]byte, 32)}
	}
	receipt := func(contract *ethrpc.Address, logs ...ethrpc.Log) map[ethrpc.Hash]ethrpc.Receipt {
		return map[ethrpc.Hash]ethrpc.Receipt{hash(1): {TransactionHash: hash(1), BlockNumber: 100, Status: 1, ContractAddress: contract, Logs: logs}}
	}
	creation := hash(1)
	// A 1.4.1 factory's log for the Safe, after another contract's for another
	// proxy.
	created := receipt(nil, proxyCreation(relayer, target, true), proxyCreation(factory141, safe, true))
	const unbound = "neither creates the account nor logs a ProxyCreation event of it from an official proxy factory"
	tests := []struct {
		name     string
		deployed uint64
		creation []ethrpc.Hash
		receipts map[ethrpc.Hash]ethrpc.Receipt
		want     *Creation
		warning  string
	}{
		{"by a 1.4.1 factory", 100, []ethrpc.Hash{hash(1)}, created, &Creation{100, &creation, &factory141}, ""},
		{"by a 1.3.0 factory", 100, []ethrpc.Hash{hash(1)}, receipt(nil, proxyCreation(factory130, safe, false)), &Creation{100, &creation, &factory130}, ""},
		{"by a 1.1.1 factory", 100, []ethrpc.Hash{hash(1)}, receipt(nil, proxyCreation111(factory111, safe)), &Creation{100, &creation, &factory111}, ""},
		{"by a contract creation", 100, []ethrpc.Hash{hash(1)}, receipt(&safe), &Creation{100, &creation, nil}, ""},
		// The service names another transaction of the block, which the code alone
		// can't tell from the creation.
		{"another transaction", 100, []ethrpc.Hash{hash(1)}, receipt(nil), &Creation{100, nil, nil}, unbound},
		{"another contract's ProxyCreation", 100, []ethrpc.Hash{hash(1)}, receipt(&target, proxyCreation(relayer, safe, true)), &Creation{100, nil, nil}, unbound},
		{"a factory's ProxyCreation of another proxy", 100, []ethrpc.Hash{hash(1)}, receipt(nil, proxyCreation(factory130, target, false)), &Creation{100, nil, nil}, unbound},
		{"another contract's ProxyCreation of the proxy alone", 100, []ethrpc.Hash{hash(1)}, receipt(nil, proxyCreation111(relayer, safe)), &Creation{100, nil, nil}, unbound},
		{"code before", 90, []ethrpc.Hash{hash(1)}, created, nil, "has code at block 99"},
		{"no code", 0, []ethrpc.Hash{hash(1)}, created, nil, "no code at block 100"},
		{"later", 100, []ethrpc.Hash{hash(1)}, map[ethrpc.Hash]ethrpc.Receipt{hash(1): {BlockNumber: last + 1}}, nil, ""},
		{"no receipt", 100, []ethrpc.Hash{hash(1)}, nil, nil, "receipt"},
		{"unknown", 100, nil, nil, nil, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := &chain{deployed: test.deployed, receipts: test.receipts}
			f := &Finder{Eth: c.serve(t), Service: historyService(t, nil, test.creation...)}
			var warnings []error
			f.Warn = func(err error) { warnings = append(warnings, err) }
			got, err := f.CreationFromService(t.Context(), safe, last)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Errorf("CreationFromService: got %+v, %v, want %+v", got, err, test.want)
			}
			switch {
			case test.warning == "" && len(warnings) != 0:
				t.Errorf("warnings: got %v, want none", warnings)
			case test.warning != "" && (len(warnings) != 1 || !strings.Contains(warnings[0].Error(), test.warning)):
				t.Errorf("warnings: got %v, want one containing %q", warnings, test.warning)
			}
		})
	}
}

func TestCreationFromServiceFailure(t *testing.T) {
	// A service that fails or rate limits says nothing about the creation, so it
	// fails rather than leave the creation unknown, and one that refuses the
	// request answers it, with a warning.
	c := &chain{deployed: 100}
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		got, err := (&Finder{Eth: c.serve(t), Service: statusService(t, status)}).CreationFromService(t.Context(), safe, 200)
		if !txservice.Unavailable(err) {
			t.Errorf("CreationFromService with HTTP %d: got %+v, %v; want an error that the service is unavailable", status, got, err)
		}
	}
	f := &Finder{Eth: c.serve(t), Service: statusService(t, http.StatusForbidden)}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.CreationFromService(t.Context(), safe, 200)
	if got != nil || err != nil || len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "403") {
		t.Errorf("CreationFromService with HTTP 403: got %+v, %v, warnings %v; want nil with a warning with the status", got, err, warnings)
	}
}

func TestFindAllRecoveredBeforeFailedScan(t *testing.T) {
	// The service only lists the execution at nonce 1. The scan before it finds the
	// one at nonce 0, and the scan after it, for the one at nonce 2, fails, so the
	// nonce of the one recovered is worked out from its own block.
	txs := []*safenet.SafeTransaction{transaction("first", 0), transaction("second", 1), transaction("third", 2)}
	c := &chain{nonce: big.NewInt(3), deployed: 50, txs: make(map[ethrpc.Hash]ethrpc.Transaction), failingBlocks: []uint64{120}}
	for n, block := range []uint64{100, 110, 120} {
		c.txs[hash(byte(n+1))] = ethrpc.Transaction{To: &safe, Input: execCall(txs[n])}
		c.logs = append(c.logs, executionLog(executionSuccessEvent, txs[n].Hash(), hash(byte(n+1)), true, block, 0))
	}
	f := &Finder{Eth: c.serve(t), Service: historyService(t, []listing{listed(txs[1], hash(2), 110)})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.FindAll(t.Context(), safe, 200)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	checkExecutions(t, got.Executions, []Execution{
		executed(txs[0], hash(1), 100, SourceCalldata), executed(txs[1], hash(2), 110, SourceService),
	})
	if want := []NonceRange{{2, 2}}; !reflect.DeepEqual(got.Missing, want) {
		t.Errorf("FindAll: got missing %v, want %v", got.Missing, want)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "scanning blocks 110 to 200") {
		t.Errorf("warnings: got %v, want one about the scan after nonce 1", warnings)
	}
}
