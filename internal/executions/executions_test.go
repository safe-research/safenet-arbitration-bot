package executions

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

var (
	safe    = ethrpc.MustParseAddress("0x5afe000000000000000000000000000000000001")
	relayer = ethrpc.MustParseAddress("0x7e1a000000000000000000000000000000000002")
	target  = ethrpc.MustParseAddress("0xa11ce00000000000000000000000000000000003")
)

// chain is a fake node for Ethereum Mainnet, with a Safe's logs and the data
// that the decoding sources read.
type chain struct {
	logs []ethrpc.Log
	// nonce is the Safe's nonce at the last block, or nil if reading it fails.
	nonce *big.Int
	// txs are the transactions by hash.
	txs map[ethrpc.Hash]ethrpc.Transaction
	// traces are the callTracer results by transaction hash. A transaction without
	// one fails to trace, like on a node that doesn't serve traces.
	traces map[ethrpc.Hash]any
	// traced counts the trace requests.
	traced atomic.Int64
	// erring are the methods that the node answers with a JSON-RPC error, such as
	// nodeTimeout.
	erring map[string]ethrpc.Error
	// empty makes the Safe's calls return no data, as those to an account without
	// code do.
	empty bool
	// failing are the methods that the node fails, with their HTTP status, such as
	// 500 for an overloaded node, 429 for a rate limit, or 403 or 200 for a page
	// that a proxy sends instead of a JSON-RPC response.
	failing map[string]int
	// resetting are the methods whose responses the node cuts off with a TCP reset,
	// as an overloaded proxy does.
	resetting []string
}

// nodeTimeout is Nethermind's error for a request that ran past its time limit.
var nodeTimeout = ethrpc.Error{Code: -32016, Message: "request was canceled due to enabled timeout."}

// serve starts the node, and returns a client for it.
func (c *chain) serve(t *testing.T) *ethrpc.Client {
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
		if req.Method == "debug_traceTransaction" {
			c.traced.Add(1)
		}
		if status, ok := c.failing[req.Method]; ok {
			w.Header().Set("Retry-After", "0")
			http.Error(w, http.StatusText(status), status)
			return
		}
		if slices.Contains(c.resetting, req.Method) {
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijacking the connection: %v", err)
				return
			}
			buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n{")
			buf.Flush()
			conn.(*net.TCPConn).SetLinger(0)
			conn.Close()
			return
		}
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		fail := func(message string) { res["error"] = map[string]any{"code": -32000, "message": message} }
		if rpcErr, ok := c.erring[req.Method]; ok {
			res["error"] = rpcErr
			json.NewEncoder(w).Encode(res)
			return
		}
		switch req.Method {
		case "eth_chainId":
			res["result"] = "0x1"
		case "eth_getLogs":
			var filter ethrpc.LogFilter
			json.Unmarshal(req.Params[0], &filter)
			logs := []ethrpc.Log{}
			for _, log := range c.logs {
				if log.BlockNumber >= filter.FromBlock && log.BlockNumber <= filter.ToBlock &&
					slices.Contains(filter.Addresses, log.Address) && slices.Contains(filter.Topics[0], log.Topics[0]) {
					logs = append(logs, log)
				}
			}
			res["result"] = logs
		case "eth_call":
			var call ethrpc.CallRequest
			var block ethrpc.BlockNumber
			json.Unmarshal(req.Params[0], &call)
			json.Unmarshal(req.Params[1], &block)
			if c.empty {
				res["result"] = "0x"
				break
			}
			if c.nonce == nil || call.To != safe || block != 200 || string(call.Data) != string(solabi.Call(nonceSelector)) {
				fail("execution reverted")
				break
			}
			word := solabi.Uint(c.nonce)
			res["result"] = ethrpc.Bytes(word[:])
		case "eth_getTransactionByHash":
			var hash ethrpc.Hash
			json.Unmarshal(req.Params[0], &hash)
			if tx, ok := c.txs[hash]; ok {
				res["result"] = tx
			} else {
				res["result"] = nil
			}
		case "debug_traceTransaction":
			var hash ethrpc.Hash
			json.Unmarshal(req.Params[0], &hash)
			if trace, ok := c.traces[hash]; ok {
				res["result"] = trace
			} else {
				res["error"] = map[string]any{"code": -32601, "message": "the method debug_traceTransaction does not exist/is not available"}
			}
		default:
			res["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(server.Close)
	eth, err := ethrpc.NewClient(t.Context(), ethrpc.Mainnet, server.URL)
	if err != nil {
		t.Fatalf("ethrpc.NewClient: %v", err)
	}
	return eth
}

// transaction returns a Safe transaction of the Safe, on Mainnet, that calls
// target with data and uses nonce.
func transaction(data string, nonce int64) *safenet.SafeTransaction {
	return &safenet.SafeTransaction{
		ChainID:        big.NewInt(1),
		Safe:           safe,
		To:             target,
		Value:          big.NewInt(nonce),
		Data:           []byte(data),
		Operation:      safenet.Operation(nonce % 2),
		SafeTxGas:      big.NewInt(3),
		BaseGas:        big.NewInt(4),
		GasPrice:       big.NewInt(5),
		GasToken:       ethrpc.Address{19: 6},
		RefundReceiver: ethrpc.Address{19: 7},
		Nonce:          big.NewInt(nonce),
	}
}

// fields returns the ABI values of a Safe transaction's fields, without its
// nonce, followed by extra.
func fields(tx *safenet.SafeTransaction, extra ...any) []any {
	return append([]any{
		tx.To, tx.Value, []byte(tx.Data), uint64(tx.Operation), tx.SafeTxGas, tx.BaseGas, tx.GasPrice, tx.GasToken, tx.RefundReceiver, []byte("signatures"),
	}, extra...)
}

// execCall returns the calldata of a call to execTransaction with tx.
func execCall(tx *safenet.SafeTransaction) ethrpc.Bytes {
	return solabi.Call(execTransaction, fields(tx)...)
}

// eventLog returns the SafeMultiSigTransaction log that SafeL2 logs for tx.
func eventLog(tx *safenet.SafeTransaction, hash ethrpc.Hash, block, index uint64) ethrpc.Log {
	nonce := solabi.Uint(tx.Nonce)
	info := append(nonce[:], make([]byte, 64)...)
	return ethrpc.Log{
		Address:         safe,
		Topics:          []ethrpc.Hash{multiSigEvent},
		Data:            solabi.Encode(fields(tx, info)...),
		BlockNumber:     ethrpc.BlockNumber(block),
		TransactionHash: hash,
		LogIndex:        ethrpc.Quantity(index),
	}
}

// executionLog returns the log that a Safe logs when it executes the
// transaction with the hash safeTxHash: with the hash as a topic like Safe
// 1.4.1 and later do, or in the data like 1.3.0.
func executionLog(event, safeTxHash, hash ethrpc.Hash, indexed bool, block, index uint64) ethrpc.Log {
	log := ethrpc.Log{
		Address:         safe,
		Topics:          []ethrpc.Hash{event},
		BlockNumber:     ethrpc.BlockNumber(block),
		TransactionHash: hash,
		LogIndex:        ethrpc.Quantity(index),
	}
	payment := solabi.Uint(big.NewInt(9))
	if indexed {
		log.Topics = append(log.Topics, safeTxHash)
		log.Data = payment[:]
	} else {
		log.Data = append(safeTxHash[:], payment[:]...)
	}
	return log
}

func hash(n byte) ethrpc.Hash { return ethrpc.Hash{0: 0xaa, 31: n} }

// service starts a Safe Transaction Service that has the transactions by Safe
// transaction hash, and fails requests for those in failing, and returns a
// client for it.
func service(t *testing.T, transactions map[ethrpc.Hash]*safenet.SafeTransaction, failing ...ethrpc.Hash) *txservice.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/multisig-transactions/")
		h, err := ethrpc.ParseHash(strings.TrimSuffix(id, "/"))
		tx := transactions[h]
		switch {
		case slices.Contains(failing, h):
			http.Error(w, "\x1b[31munavailable", http.StatusInternalServerError)
		case !ok || err != nil || tx == nil:
			http.NotFound(w, r)
		default:
			// Numbers are JSON numbers or strings, and data is null when empty.
			json.NewEncoder(w).Encode(map[string]any{
				"safe": tx.Safe, "to": tx.To, "value": tx.Value.String(), "data": tx.Data,
				"operation": uint8(tx.Operation), "safeTxGas": tx.SafeTxGas.Uint64(), "baseGas": tx.BaseGas.Uint64(),
				"gasPrice": tx.GasPrice.String(), "gasToken": tx.GasToken, "refundReceiver": tx.RefundReceiver,
				"nonce": tx.Nonce.Uint64(),
			})
		}
	}))
	t.Cleanup(server.Close)
	return txservice.NewClient(server.URL, ethrpc.Mainnet)
}

func TestFind(t *testing.T) {
	const last = 200
	// Five executions, at the nonces 10 to 14, and a Safe whose nonce is 15 at the
	// last block.
	viaEvent := transaction("event", 10)
	viaCalldata := transaction("calldata", 11)
	viaTrace := transaction("trace", 12)
	viaService := transaction("service", 13)
	unknown := transaction("unknown", 14)

	// A transaction that a relayer makes, with a call to the Safe that reverted
	// before the one that succeeded, and calls that aren't to the Safe.
	trace := func(tx *safenet.SafeTransaction) map[string]any {
		call := func(to ethrpc.Address, input ethrpc.Bytes, errMsg string, calls ...any) map[string]any {
			return map[string]any{"type": "CALL", "to": to, "input": input, "error": errMsg, "calls": calls}
		}
		other := transaction("other", 99)
		return call(relayer, nil, "",
			call(safe, execCall(other), "execution reverted", call(safe, execCall(tx), "")),
			call(target, execCall(tx), ""),
			call(relayer, nil, "", call(safe, execCall(tx), "")),
		)
	}
	c := &chain{
		nonce: big.NewInt(15),
		txs: map[ethrpc.Hash]ethrpc.Transaction{
			hash(1): {To: &relayer, Input: []byte("not a Safe call")},
			hash(2): {To: &safe, Input: execCall(viaCalldata)},
			hash(3): {To: &relayer, Input: []byte("relay")},
			hash(4): {To: &relayer, Input: []byte("relay")},
			hash(5): {To: &safe, Input: execCall(transaction("another transaction", 14))},
		},
		traces: map[ethrpc.Hash]any{hash(3): trace(viaTrace)},
	}
	c.logs = []ethrpc.Log{
		// A Safe 1.4.1 L2.
		eventLog(viaEvent, hash(1), 100, 0),
		executionLog(executionSuccessEvent, viaEvent.Hash(), hash(1), true, 100, 1),
		// A Safe 1.3.0, which doesn't index the hash, and a failed transaction.
		executionLog(executionFailureEvent, viaCalldata.Hash(), hash(2), false, 110, 0),
		executionLog(executionSuccessEvent, viaTrace.Hash(), hash(3), true, 120, 3),
		executionLog(executionSuccessEvent, viaService.Hash(), hash(4), true, 130, 0),
		executionLog(executionSuccessEvent, unknown.Hash(), hash(5), true, 140, 0),
		// Logs that aren't executions: another Safe's, a removed one, and malformed
		// ones.
		func() ethrpc.Log {
			l := executionLog(executionSuccessEvent, hash(7), hash(6), true, 150, 0)
			l.Address = relayer
			return l
		}(),
		func() ethrpc.Log {
			l := executionLog(executionSuccessEvent, hash(7), hash(6), true, 150, 1)
			l.Removed = true
			return l
		}(),
		func() ethrpc.Log {
			l := executionLog(executionSuccessEvent, hash(7), hash(6), true, 150, 2)
			l.Data = nil
			return l
		}(),
		// An execution after the last block.
		executionLog(executionSuccessEvent, hash(8), hash(6), true, last+1, 0),
	}
	f := &Finder{
		Eth: c.serve(t),
		Service: service(t, map[ethrpc.Hash]*safenet.SafeTransaction{
			viaService.Hash(): viaService,
			// A service that reports another transaction than the one asked for.
			unknown.Hash(): transaction("not what was executed", 14),
		}),
	}

	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.Find(t.Context(), safe, 0, last)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	want := []Execution{
		{100, hash(1), 1, viaEvent.Hash(), true, big.NewInt(9), SourceEvent, viaEvent},
		{110, hash(2), 0, viaCalldata.Hash(), false, big.NewInt(9), SourceCalldata, viaCalldata},
		{120, hash(3), 3, viaTrace.Hash(), true, big.NewInt(9), SourceTrace, viaTrace},
		{130, hash(4), 0, viaService.Hash(), true, big.NewInt(9), SourceService, viaService},
		{140, hash(5), 0, unknown.Hash(), true, big.NewInt(9), "", nil},
	}
	if !reflect.DeepEqual(got, want) {
		for i := range got {
			if i >= len(want) || !reflect.DeepEqual(got[i], want[i]) {
				g, _ := json.Marshal(got[i])
				w, _ := json.Marshal(want[min(i, len(want)-1)])
				t.Errorf("execution %d:\ngot  %s\nwant %s", i, g, w)
			}
		}
		if len(got) != len(want) {
			t.Errorf("got %d executions, want %d", len(got), len(want))
		}
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: got %v, want none", warnings)
	}
}

func TestFindWithoutNonce(t *testing.T) {
	// Without the nonce, the calldata and traces can't be checked against the hash,
	// so they aren't used, and the service has the transaction.
	tx := transaction("calldata", 11)
	c := &chain{
		txs:  map[ethrpc.Hash]ethrpc.Transaction{hash(1): {To: &safe, Input: execCall(tx)}},
		logs: []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
	}
	f := &Finder{Eth: c.serve(t), Service: service(t, map[ethrpc.Hash]*safenet.SafeTransaction{tx.Hash(): tx})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }

	got, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 || got[0].Source != SourceService || !reflect.DeepEqual(got[0].Transaction, tx) {
		t.Errorf("Find: got %+v, want the transaction from the service", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "reading the nonce") {
		t.Errorf("warnings: got %v, want one about reading the nonce", warnings)
	}

	// Without a service either, the transaction is unknown.
	f.Service = nil
	if got, _ = f.Find(t.Context(), safe, 0, 200); len(got) != 1 || got[0].Transaction != nil || got[0].Source != "" {
		t.Errorf("Find without a service: got %+v, want an unknown transaction", got)
	}
}

func TestFindWithNonceBeforeTheSearch(t *testing.T) {
	// A nonce that is lower than the number of executions can't be right.
	tx := transaction("calldata", 0)
	c := &chain{
		nonce: big.NewInt(0),
		txs:   map[ethrpc.Hash]ethrpc.Transaction{hash(1): {To: &safe, Input: execCall(tx)}},
		logs:  []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
	}
	f := &Finder{Eth: c.serve(t)}
	got, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 || got[0].Transaction != nil {
		t.Errorf("Find: got %+v, want an unknown transaction", got)
	}
}

func TestFindServiceFailure(t *testing.T) {
	// A service that fails, rate limits, or can't be reached says nothing about the
	// transaction, so Find fails rather than report it as unknown, with an error
	// that doesn't have the service's response as is.
	tx := transaction("service", 1)
	c := &chain{logs: []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)}}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	for name, s := range map[string]*txservice.Client{
		"500":                service(t, nil, tx.Hash()),
		"429":                statusService(t, http.StatusTooManyRequests),
		"connection refused": txservice.NewClient(closed.URL, ethrpc.Mainnet),
	} {
		got, err := (&Finder{Eth: c.serve(t), Service: s}).Find(t.Context(), safe, 0, 200)
		if !txservice.Unavailable(err) || strings.Contains(fmt.Sprint(err), "\x1b") {
			t.Errorf("%s: Find got %+v, %v; want an error that the service is unavailable, without its response as is", name, got, err)
		}
	}

	// A service that refuses the request answers it, so the transaction is unknown,
	// with a warning.
	f := &Finder{Eth: c.serve(t), Service: statusService(t, http.StatusForbidden)}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil || len(got) != 1 || got[0].Transaction != nil || !slices.ContainsFunc(warnings, func(w error) bool { return strings.Contains(w.Error(), "403") }) {
		t.Errorf("Find: got %+v, %v, warnings %v; want an unknown transaction with a warning with the HTTP status", got, err, warnings)
	}
}

// statusService starts a Safe Transaction Service that answers every request
// with the HTTP status, and returns a client for it.
func statusService(t *testing.T, status int) *txservice.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		http.Error(w, http.StatusText(status), status)
	}))
	t.Cleanup(server.Close)
	return txservice.NewClient(server.URL, ethrpc.Mainnet)
}

func TestFindNodeFailure(t *testing.T) {
	// A node that fails rather than answers says nothing about the execution, so
	// Find fails instead of reporting an unknown transaction, and stops reading.
	tx := transaction("calldata", 1)
	execLog := executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)
	for _, method := range []string{"eth_call", "eth_getTransactionByHash"} {
		for _, status := range []int{http.StatusInternalServerError, http.StatusForbidden, http.StatusOK} {
			c := &chain{
				logs:    []ethrpc.Log{execLog},
				nonce:   big.NewInt(2),
				txs:     map[ethrpc.Hash]ethrpc.Transaction{hash(1): {Hash: hash(1), To: &relayer, Input: execCall(tx)}},
				failing: map[string]int{method: status},
			}
			f := &Finder{Eth: c.serve(t)}
			got, err := f.Find(t.Context(), safe, 0, 200)
			if !ethrpc.Transient(err) || c.traced.Load() != 0 {
				t.Errorf("%s failing with %d: Find got %+v, %v after %d traces, want a transient error and no trace",
					method, status, got, err, c.traced.Load())
			}
		}
	}
}

func TestFindNodeError(t *testing.T) {
	// The Safe logged the execution, so an error of the node, such as for a block
	// whose state it doesn't have, is no answer about its nonce or the transaction
	// that the node logged, even if it isn't transient.
	tx := transaction("calldata", 1)
	execLog := executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)
	for _, method := range []string{"eth_call", "eth_getTransactionByHash"} {
		for _, rpcErr := range []ethrpc.Error{
			{Code: -32000, Message: "missing trie node 7b2ad5 (path ) state 0x7b2ad5 is not available"},
			{Code: -32000, Message: "header not found"},
			{Code: -32603, Message: "server overloaded"},
			{Code: -32000, Message: "transaction indexing is in progress"},
		} {
			c := &chain{
				logs:   []ethrpc.Log{execLog},
				nonce:  big.NewInt(2),
				txs:    map[ethrpc.Hash]ethrpc.Transaction{hash(1): {Hash: hash(1), To: &relayer, Input: execCall(tx)}},
				erring: map[string]ethrpc.Error{method: rpcErr},
			}
			got, err := (&Finder{Eth: c.serve(t)}).Find(t.Context(), safe, 0, 200)
			if err == nil || c.traced.Load() != 0 {
				t.Errorf("%s answering %v: Find got %+v, %v after %d traces, want an error and no trace", method, &rpcErr, got, err, c.traced.Load())
			}
		}
	}
}

func TestFindNonceOfCode(t *testing.T) {
	// The Safe's code can still revert or halt on nonce(), such as after a delegate
	// call replaced its singleton, or return nothing, after it self-destructed,
	// which leaves out the calldata and traces with a warning.
	tx := transaction("calldata", 11)
	for name, c := range map[string]*chain{
		"revert": {erring: map[string]ethrpc.Error{"eth_call": {Code: 3, Message: "execution reverted"}}},
		"halt":   {erring: map[string]ethrpc.Error{"eth_call": {Code: -32000, Message: "invalid opcode: INVALID"}}},
		"empty":  {nonce: big.NewInt(12), empty: true},
	} {
		c.txs = map[ethrpc.Hash]ethrpc.Transaction{hash(1): {To: &safe, Input: execCall(tx)}}
		c.logs = []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)}
		f := &Finder{Eth: c.serve(t), Service: service(t, map[ethrpc.Hash]*safenet.SafeTransaction{tx.Hash(): tx})}
		var warnings []error
		f.Warn = func(err error) { warnings = append(warnings, err) }
		got, err := f.Find(t.Context(), safe, 0, 200)
		if err != nil || len(got) != 1 || got[0].Source != SourceService || len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "reading the nonce") {
			t.Errorf("%s: Find got %+v, %v, warnings %v; want the transaction from the service, with a warning about the nonce", name, got, err, warnings)
		}
	}
}

func TestFindTransactionNotIndexed(t *testing.T) {
	// A node that only indexes recent transactions doesn't have an older one, which
	// leaves out its calldata with a warning.
	tx := transaction("calldata", 1)
	c := &chain{
		logs:  []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
		nonce: big.NewInt(2),
	}
	f := &Finder{Eth: c.serve(t), Service: service(t, map[ethrpc.Hash]*safenet.SafeTransaction{tx.Hash(): tx})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil || len(got) != 1 || got[0].Source != SourceService || len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "not found") {
		t.Errorf("Find: got %+v, %v, warnings %v; want the transaction from the service, with a warning", got, err, warnings)
	}
}

func TestFindNonceTimeout(t *testing.T) {
	// A nonce that the node didn't read in time says nothing about the Safe.
	tx := transaction("calldata", 1)
	c := &chain{
		logs:   []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
		nonce:  big.NewInt(2),
		txs:    map[ethrpc.Hash]ethrpc.Transaction{hash(1): {Hash: hash(1), To: &safe, Input: execCall(tx)}},
		erring: map[string]ethrpc.Error{"eth_call": nodeTimeout},
	}
	if got, err := (&Finder{Eth: c.serve(t)}).Find(t.Context(), safe, 0, 200); !ethrpc.Transient(err) {
		t.Errorf("Find: got %+v, %v; want a transient error", got, err)
	}
}

func TestFindTraceRefused(t *testing.T) {
	// Some nodes refuse debug_traceTransaction with an HTTP error status, such as
	// 401, 406 or 503, which leaves the transaction unknown with a warning rather
	// than failing.
	tx := transaction("relayed", 1)
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotAcceptable, http.StatusServiceUnavailable} {
		c := &chain{
			logs:    []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
			nonce:   big.NewInt(2),
			txs:     map[ethrpc.Hash]ethrpc.Transaction{hash(1): {Hash: hash(1), To: &relayer, Input: execCall(tx)}},
			failing: map[string]int{"debug_traceTransaction": status},
		}
		f := &Finder{Eth: c.serve(t)}
		var warnings []error
		f.Warn = func(err error) { warnings = append(warnings, err) }
		got, err := f.Find(t.Context(), safe, 0, 200)
		if err != nil || len(got) != 1 || got[0].Transaction != nil || len(warnings) != 1 {
			t.Errorf("HTTP %d: Find got %+v, %v, warnings %v; want an unknown transaction with one warning", status, got, err, warnings)
		}
	}
}

func TestFindTraceRateLimit(t *testing.T) {
	// A node that rate limits traces may serve them, so Find fails rather than
	// report an unknown transaction.
	tx := transaction("relayed", 1)
	c := &chain{
		logs:    []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
		nonce:   big.NewInt(2),
		txs:     map[ethrpc.Hash]ethrpc.Transaction{hash(1): {Hash: hash(1), To: &relayer, Input: execCall(tx)}},
		failing: map[string]int{"debug_traceTransaction": http.StatusTooManyRequests},
	}
	if got, err := (&Finder{Eth: c.serve(t)}).Find(t.Context(), safe, 0, 200); !ethrpc.Transient(err) {
		t.Errorf("Find: got %+v, %v; want a transient error", got, err)
	}
}

func TestFindTraceTimeout(t *testing.T) {
	// A trace that times out is the node's answer, as one that it doesn't serve is,
	// so the transaction comes from the service.
	tx := transaction("relayed", 1)
	c := &chain{
		logs:   []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
		nonce:  big.NewInt(2),
		txs:    map[ethrpc.Hash]ethrpc.Transaction{hash(1): {Hash: hash(1), To: &relayer, Input: execCall(tx)}},
		erring: map[string]ethrpc.Error{"debug_traceTransaction": nodeTimeout},
	}
	f := &Finder{Eth: c.serve(t), Service: service(t, map[ethrpc.Hash]*safenet.SafeTransaction{tx.Hash(): tx})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil || len(got) != 1 || got[0].Source != SourceService || len(warnings) != 0 {
		t.Errorf("Find: got %+v, %v, warnings %v; want the transaction from the service", got, err, warnings)
	}
}

func TestFindConnectionReset(t *testing.T) {
	// A response that the node cuts off says nothing about the transaction, so Find
	// fails rather than report it as unknown.
	tx := transaction("calldata", 1)
	c := &chain{
		logs:      []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
		nonce:     big.NewInt(2),
		txs:       map[ethrpc.Hash]ethrpc.Transaction{hash(1): {Hash: hash(1), To: &safe, Input: execCall(tx)}},
		resetting: []string{"eth_getTransactionByHash"},
	}
	if got, err := (&Finder{Eth: c.serve(t)}).Find(t.Context(), safe, 0, 200); !ethrpc.Transient(err) {
		t.Errorf("Find: got %+v, %v; want a transient error", got, err)
	}
}

func TestFindReturnsEmptyList(t *testing.T) {
	f := &Finder{Eth: (&chain{}).serve(t)}
	got, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("Find: got %#v, want an empty list", got)
	}
}

func TestFindLogsFailure(t *testing.T) {
	f := &Finder{Eth: (&chain{}).serve(t)}
	f.Eth = brokenClient(t)
	if _, err := f.Find(t.Context(), safe, 0, 200); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("listing Safe transactions of %s", safe)) {
		t.Errorf("Find: got error %v, want one about listing Safe transactions", err)
	}
}

// brokenClient returns a client for a node that answers every request but the
// chain ID with an error.
func brokenClient(t *testing.T) *ethrpc.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if req.Method == "eth_chainId" {
			res["result"] = "0x1"
		} else {
			res["error"] = map[string]any{"code": -32000, "message": "broken"}
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(server.Close)
	eth, err := ethrpc.NewClient(t.Context(), ethrpc.Mainnet, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return eth
}

func TestDecodeCall(t *testing.T) {
	tx := transaction("calldata", 3)
	tx.Nonce = nil
	got := decodeCall(safe, 1, execCall(transaction("calldata", 3)))
	if !reflect.DeepEqual(got, tx) {
		t.Errorf("decodeCall: got %+v, want %+v", got, tx)
	}
	if selector := fmt.Sprintf("%x", execTransaction); selector != "6a761202" {
		t.Errorf("execTransaction selector: got %s, want 6a761202", selector)
	}

	call := execCall(tx)
	for name, input := range map[string][]byte{
		"empty":           nil,
		"other function":  solabi.Call(solabi.Selector("transfer(address,uint256)"), target, uint64(1)),
		"truncated":       call[:len(call)-40],
		"invalid address": append(call[:4:4], append(make([]byte, 0), call[4:]...)...),
	} {
		if name == "invalid address" {
			input[4] = 1
		}
		if got := decodeCall(safe, 1, input); got != nil {
			t.Errorf("decodeCall(%s): got %+v, want nil", name, got)
		}
	}
}

func TestMatches(t *testing.T) {
	tx := transaction("data", 5)
	if !matches(tx, tx.Hash()) {
		t.Error("matches: a transaction doesn't match its hash")
	}
	if matches(tx, hash(1)) || matches(nil, tx.Hash()) {
		t.Error("matches: a transaction matches another hash, or nil matches")
	}
	for name, change := range map[string]func(*safenet.SafeTransaction){
		"no nonce":          func(tx *safenet.SafeTransaction) { tx.Nonce = nil },
		"no value":          func(tx *safenet.SafeTransaction) { tx.Value = nil },
		"wide gas price":    func(tx *safenet.SafeTransaction) { tx.GasPrice = new(big.Int).Lsh(big.NewInt(1), 256) },
		"negative base gas": func(tx *safenet.SafeTransaction) { tx.BaseGas = big.NewInt(-1) },
		"unknown operation": func(tx *safenet.SafeTransaction) { tx.Operation = 2 },
	} {
		tx := transaction("data", 5)
		change(tx)
		if matches(tx, transaction("data", 5).Hash()) {
			t.Errorf("matches(%s): got true, want false", name)
		}
	}
}
