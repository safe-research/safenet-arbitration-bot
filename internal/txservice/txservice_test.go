package txservice

import (
	"cmp"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

var hash = ethrpc.Hash{0: 0xaa, 31: 1}

// serve starts a service that answers requests for hash with status and body,
// and has an about page, and returns a client for it.
func serve(t *testing.T, status int, body string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tx-service/eth/api/v1/about/" {
			w.Write([]byte(`{"name": "Safe Transaction Service"}`))
			return
		}
		// The path is versioned, and has a trailing slash like the service wants.
		if r.URL.Path != "/tx-service/eth/api/v1/multisig-transactions/"+hash.String()+"/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL+"/tx-service/eth", ethrpc.Mainnet)
}

func TestTransaction(t *testing.T) {
	// A response of the service, whose numbers are strings or numbers, with fields
	// that the client doesn't use.
	client := serve(t, http.StatusOK, `{
		"safe": "0x5afE000000000000000000000000000000000001",
		"to": "0xa11ce00000000000000000000000000000000003",
		"value": "1000000000000000000000000000000",
		"data": "0x1234",
		"operation": 1,
		"safeTxGas": 3,
		"baseGas": 4,
		"gasPrice": "5",
		"gasToken": "0x0000000000000000000000000000000000000006",
		"refundReceiver": "0x0000000000000000000000000000000000000007",
		"nonce": 8,
		"executionDate": "2026-09-28T09:24:11Z",
		"dataDecoded": {"method": "transfer"}
	}`)
	got, err := client.Transaction(t.Context(), hash)
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	value, _ := new(big.Int).SetString("1000000000000000000000000000000", 10)
	want := &safenet.SafeTransaction{
		ChainID:        big.NewInt(1),
		Safe:           ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001"),
		To:             ethrpc.MustParseAddress("0xa11ce00000000000000000000000000000000003"),
		Value:          value,
		Data:           []byte{0x12, 0x34},
		Operation:      safenet.OperationDelegateCall,
		SafeTxGas:      big.NewInt(3),
		BaseGas:        big.NewInt(4),
		GasPrice:       big.NewInt(5),
		GasToken:       ethrpc.Address{19: 6},
		RefundReceiver: ethrpc.Address{19: 7},
		Nonce:          big.NewInt(8),
	}
	if g, w := got.Hash(), want.Hash(); g != w {
		t.Errorf("Transaction: got %+v with hash %s, want %+v with hash %s", got, g, want, w)
	}
}

func TestTransactionWithoutData(t *testing.T) {
	client := serve(t, http.StatusOK, `{
		"safe": "0x5afE000000000000000000000000000000000001",
		"to": "0xa11ce00000000000000000000000000000000003",
		"value": "0", "data": null, "operation": 0, "safeTxGas": 0, "baseGas": 0, "gasPrice": "0",
		"gasToken": "0x0000000000000000000000000000000000000000",
		"refundReceiver": "0x0000000000000000000000000000000000000000",
		"nonce": 0
	}`)
	got, err := client.Transaction(t.Context(), hash)
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if len(got.Data) != 0 {
		t.Errorf("Transaction: got data %x, want none", got.Data)
	}
}

func TestTransactionErrors(t *testing.T) {
	if _, err := serve(t, http.StatusNotFound, `{"detail": "Not found."}`).Transaction(t.Context(), hash); !errors.Is(err, ErrNotFound) {
		t.Errorf("Transaction of an unknown hash: got error %v, want ErrNotFound", err)
	}

	// The body of an error is quoted, as it isn't trusted.
	_, err := serve(t, http.StatusInternalServerError, "\x1b[31munavailable").Transaction(t.Context(), hash)
	if err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "\x1b") {
		t.Errorf("Transaction of an unavailable service: got error %q, want one with the status and no escape sequence", err)
	}

	for name, body := range map[string]string{
		"not JSON":        `<html>`,
		"invalid address": `{"safe": "0x12", "value": "0"}`,
		"negative value":  `{"value": "-1"}`,
		"invalid number":  `{"value": "one"}`,
	} {
		if _, err := serve(t, http.StatusOK, body).Transaction(t.Context(), hash); err == nil {
			t.Errorf("Transaction(%s): expected an error", name)
		}
	}

	big := serve(t, http.StatusOK, `{"data": "0x`+strings.Repeat("00", maxResponseSize)+`"}`)
	if _, err := big.Transaction(t.Context(), hash); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("Transaction with a large response: got error %v, want one about its size", err)
	}
}

func TestTransactionAtWrongURL(t *testing.T) {
	// Any server answers a wrong URL with 404, so that only says that the service
	// doesn't have the transaction if the server has the service's about page,
	// which the client asks for once.
	for _, about := range []bool{true, false} {
		var abouts atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/about/" {
				abouts.Add(1)
				if about {
					w.Write([]byte(`{"name": "Safe Transaction Service"}`))
					return
				}
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(server.Close)
		client := NewClient(server.URL, ethrpc.Mainnet)
		for range 2 {
			_, err := client.Transaction(t.Context(), hash)
			if about && !errors.Is(err, ErrNotFound) || !about && (err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "about page")) {
				t.Errorf("about page %t: Transaction got error %v", about, err)
			}
		}
		if abouts.Load() != 1 {
			t.Errorf("about page %t: Transaction asked for the about page %d times, want once", about, abouts.Load())
		}
	}
	// An about page that fails to answer says neither, so the service is
	// unavailable.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/about/" {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	if _, err := NewClient(server.URL, ethrpc.Mainnet).Transaction(t.Context(), hash); !Unavailable(err) {
		t.Errorf("about page failing: Transaction got error %v, want the service unavailable", err)
	}
}

func TestUnavailable(t *testing.T) {
	// A rate limit, a server error, a page that isn't JSON, or a failure to connect
	// isn't the service's answer, unlike a refusal, JSON that doesn't decode, or
	// not having the transaction.
	_, serverError := serve(t, http.StatusInternalServerError, "unavailable").Transaction(t.Context(), hash)
	_, refusal := serve(t, http.StatusForbidden, "forbidden").Transaction(t.Context(), hash)
	_, page := serve(t, http.StatusOK, "<html>").Transaction(t.Context(), hash)
	_, empty := serve(t, http.StatusOK, "").Transaction(t.Context(), hash)
	_, malformed := serve(t, http.StatusOK, `{"value": "one"}`).Transaction(t.Context(), hash)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	_, refused := NewClient(closed.URL, ethrpc.Mainnet).Transaction(t.Context(), hash)
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{"rate limit", &StatusError{Status: "429 Too Many Requests", StatusCode: http.StatusTooManyRequests}, true},
		{"server error", serverError, true},
		{"connection refused", refused, true},
		{"page", page, true},
		{"empty", empty, true},
		{"refusal", refusal, false},
		{"malformed", malformed, false},
		{"not found", ErrNotFound, false},
		{"nil", nil, false},
	} {
		if got := Unavailable(test.err); got != test.want {
			t.Errorf("%s: Unavailable(%v) = %v, want %v", test.name, test.err, got, test.want)
		}
	}
}

func TestDefaultURLs(t *testing.T) {
	for _, chainID := range []uint64{ethrpc.Mainnet, ethrpc.ArbitrumOne, ethrpc.Gnosis} {
		if !strings.HasPrefix(DefaultURLs[chainID], "https://") {
			t.Errorf("DefaultURLs[%d]: got %q, want an HTTPS URL", chainID, DefaultURLs[chainID])
		}
	}
}

// serveExecutions starts a service that knows safe and lists n executed
// transactions of it, in pages, with the ones in broken having no transaction
// hash, the ones after them no block, and one that isn't executed, and returns
// a client for it. Like the service, it lists nothing for any other Safe, which
// it doesn't know.
func serveExecutions(t *testing.T, safe ethrpc.Address, n int, broken ...int) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The address has its checksum, which the service requires, so that of the Safe
		// in another case is another Safe.
		path, _ := strings.CutPrefix(r.URL.Path, "/tx-service/eth/api/v1/safes/")
		address, endpoint, _ := strings.Cut(path, "/")
		q := r.URL.Query()
		offset, err := strconv.Atoi(q.Get("offset"))
		switch {
		case address == safe.String() && endpoint == "":
			json.NewEncoder(w).Encode(map[string]any{"address": safe, "nonce": strconv.Itoa(n)})
			return
		case endpoint != "multisig-transactions/" || err != nil ||
			q.Get("executed") != "true" || q.Get("ordering") != "nonce" || q.Get("limit") != "100":
			http.NotFound(w, r)
			return
		case address != safe.String():
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}})
			return
		}
		results := []map[string]any{}
		for i := offset; i < min(n+1, offset+100); i++ {
			result := map[string]any{
				"safe": safe, "to": safe, "value": "0", "data": nil, "operation": 0, "safeTxGas": 0, "baseGas": 0,
				"gasPrice": "0", "gasToken": ethrpc.Address{}, "refundReceiver": ethrpc.Address{}, "nonce": i,
				"safeTxHash": ethrpc.Hash{0: 0x5a, 31: byte(i)}, "transactionHash": ethrpc.Hash{0: 0x7a, 31: byte(i)},
				"blockNumber": 1000 + i, "isExecuted": true,
			}
			switch {
			case i == n:
				result["isExecuted"], result["transactionHash"], result["blockNumber"] = false, nil, nil
			case slices.Contains(broken, i):
				result["transactionHash"] = nil
			case slices.Contains(broken, i-1):
				result["blockNumber"] = nil
			}
			results = append(results, result)
		}
		json.NewEncoder(w).Encode(map[string]any{"count": n + 1, "results": results})
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL+"/tx-service/eth", ethrpc.Mainnet)
}

func TestExecutions(t *testing.T) {
	safe := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	// More than a page, so that the client asks for the second one, with one
	// without its onchain transaction and one without its block.
	got, err := serveExecutions(t, safe, 150, 7).Executions(t.Context(), safe)
	if err != nil {
		t.Fatalf("Executions: %v", err)
	}
	if len(got) != 150 {
		t.Fatalf("Executions: got %d executions, want 150", len(got))
	}
	for i, x := range got {
		want := Execution{SafeTxHash: ethrpc.Hash{0: 0x5a, 31: byte(i)}, TxHash: ethrpc.Hash{0: 0x7a, 31: byte(i)}, Block: uint64(1000 + i)}
		if i == 7 {
			// It is listed without its onchain transaction, for the caller to report.
			want.TxHash = ethrpc.Hash{}
		}
		if i == 8 {
			want.Block = 0
		}
		if x.SafeTxHash != want.SafeTxHash || x.TxHash != want.TxHash || x.Block != want.Block ||
			x.Transaction == nil || x.Transaction.Nonce.Int64() != int64(i) || x.Transaction.Safe != safe || x.Transaction.ChainID.Int64() != 1 {
			t.Errorf("Executions[%d]: got %+v, want %+v with a transaction of nonce %d", i, x, want, i)
		}
	}

	// A full last page is followed by one with only the transaction that isn't
	// executed.
	if got, err := serveExecutions(t, safe, 99).Executions(t.Context(), safe); err != nil || len(got) != 99 {
		t.Errorf("Executions of a full page: got %d executions and error %v, want 99", len(got), err)
	}
	// A Safe that the service knows can have no executions.
	if got, err := serveExecutions(t, safe, 0).Executions(t.Context(), safe); err != nil || got == nil || len(got) != 0 {
		t.Errorf("Executions of none: got %v and error %v, want an empty list", got, err)
	}
}

func TestExecutionsCap(t *testing.T) {
	// A listing that doesn't end, as spam would make it, fails at the cap, rather
	// than when the server stops, at twice as many results.
	safe := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	page, _ := json.Marshal(map[string]any{"results": slices.Repeat([]map[string]any{{"safe": safe, "nonce": 0}}, pageSize)})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offset, _ := strconv.Atoi(r.URL.Query().Get("offset")); offset > 2*maxListed {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		w.Write(page)
	}))
	t.Cleanup(server.Close)
	if _, err := NewClient(server.URL, ethrpc.Mainnet).Executions(t.Context(), safe); err == nil || !strings.Contains(err.Error(), "lists more than 100000") {
		t.Errorf("Executions of an endless listing: got error %v, want one about the cap", err)
	}
}

func TestExecutionsPages(t *testing.T) {
	// A service that lists fewer results than it is asked for, as one with a
	// smaller maximum page does, says that there are more with its next link.
	safe := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	const n, perPage = 150, 40
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		results := []map[string]any{}
		for i := offset; i < min(n, offset+perPage); i++ {
			results = append(results, map[string]any{"safe": safe, "nonce": i, "transactionHash": ethrpc.Hash{31: byte(i)}, "blockNumber": i + 1})
		}
		var next any
		if offset+perPage < n {
			next = "https://example.invalid/next"
		}
		json.NewEncoder(w).Encode(map[string]any{"count": n, "next": next, "results": results})
	}))
	t.Cleanup(server.Close)
	got, err := NewClient(server.URL, ethrpc.Mainnet).Executions(t.Context(), safe)
	if err != nil || len(got) != n {
		t.Fatalf("Executions: got %d executions and error %v, want %d", len(got), err, n)
	}
	for i, x := range got {
		if x.Transaction.Nonce.Int64() != int64(i) {
			t.Fatalf("Executions[%d]: got nonce %s, want %d", i, x.Transaction.Nonce, i)
		}
	}
}

func TestExecutionsAtCap(t *testing.T) {
	// A listing of exactly the cap is listed in full.
	safe := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	page, _ := json.Marshal(map[string]any{"results": slices.Repeat([]map[string]any{{"safe": safe, "nonce": 0}}, pageSize)})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offset, _ := strconv.Atoi(r.URL.Query().Get("offset")); offset >= maxListed {
			w.Write([]byte(`{"results": []}`))
			return
		}
		w.Write(page)
	}))
	t.Cleanup(server.Close)
	if got, err := NewClient(server.URL, ethrpc.Mainnet).Executions(t.Context(), safe); err != nil || len(got) != maxListed {
		t.Errorf("Executions of %d: got %d and error %v", maxListed, len(got), err)
	}
}

func TestExecutionsErrors(t *testing.T) {
	safe := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	// A service for another Safe lists nothing for this one, and doesn't know it.
	other := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000002")
	if _, err := serveExecutions(t, other, 5).Executions(t.Context(), safe); !errors.Is(err, ErrNotFound) {
		t.Errorf("Executions of an unknown Safe: got error %v, want ErrNotFound", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	if _, err := NewClient(server.URL, ethrpc.Mainnet).Executions(t.Context(), safe); err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "500") {
		t.Errorf("Executions from a failing service: got error %v, want one with the status", err)
	}
	// A next page after a page without results would never end, so the service
	// fails after a few.
	pages := make(chan struct{}, 3)
	endless := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case pages <- struct{}{}:
			w.Write([]byte(`{"next": "https://example.invalid/next", "results": []}`))
		default:
			http.Error(w, "asked again for an empty page", http.StatusTeapot)
		}
	}))
	t.Cleanup(endless.Close)
	if _, err := NewClient(endless.URL, ethrpc.Mainnet).Executions(t.Context(), safe); err == nil || !strings.Contains(err.Error(), "has a next page") {
		t.Errorf("Executions of an endless listing without results: got error %v, want one about the next page", err)
	}
	// The service lists nothing for a Safe that it doesn't know, so a listing that
	// isn't found is at a wrong URL.
	missing := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(missing.Close)
	if _, err := NewClient(missing.URL, ethrpc.Mainnet).Executions(t.Context(), safe); err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "404") {
		t.Errorf("Executions at a wrong URL: got error %v, want one with the status", err)
	}
}

func TestCreation(t *testing.T) {
	safe := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/safes/" + safe.String() + "/creation/":
			w.Write([]byte(`{"created": "2026-07-05T21:24:23Z", "transactionHash": "` + hash.String() + `", "factoryAddress": "0x0000000000000000000000000000000000000001"}`))
		case "/api/v1/safes/" + (ethrpc.Address{19: 2}).String() + "/creation/":
			w.Write([]byte(`{"transactionHash": null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := NewClient(server.URL, ethrpc.Mainnet)

	if got, err := client.Creation(t.Context(), safe); err != nil || got != hash {
		t.Errorf("Creation: got %s, %v, want %s", got, err, hash)
	}
	for _, unknown := range []ethrpc.Address{{19: 1}, {19: 2}} {
		if _, err := client.Creation(t.Context(), unknown); !errors.Is(err, ErrNotFound) {
			t.Errorf("Creation of %s: got error %v, want ErrNotFound", unknown, err)
		}
	}
}

// serveTransfers starts a service that knows safe and lists the transfers, as
// the service does: it filters them by the query's kind, sender, recipient,
// block, and time, sorts them by their time alone, with those of one time in
// another order for each query, as the service's order allows, and lists the
// first page of them. Like the service, it lists nothing for any other Safe,
// which it doesn't know. It fails after 100 queries, so that a client that
// doesn't stop fails fast. It returns a client for it, and the queries for the
// transfers of safe.
func serveTransfers(t *testing.T, safe ethrpc.Address, transfers []map[string]any) (*Client, *[]url.Values) {
	t.Helper()
	var mu sync.Mutex
	var queries []url.Values
	types := map[string]string{"erc20": "ERC20_TRANSFER", "ether": "ETHER_TRANSFER"}
	date := func(x map[string]any) time.Time {
		d, _ := time.Parse(time.RFC3339, x["executionDate"].(string))
		return d
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		path, _ := strings.CutPrefix(r.URL.Path, "/api/v1/safes/")
		address, endpoint, _ := strings.Cut(path, "/")
		switch {
		case address == safe.String() && endpoint == "":
			json.NewEncoder(w).Encode(map[string]any{"address": safe})
			return
		case endpoint != "transfers/":
			http.NotFound(w, r)
			return
		case address != safe.String():
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}})
			return
		}
		mu.Lock()
		queries = append(queries, q)
		n := len(queries)
		mu.Unlock()
		if n > 100 {
			http.Error(w, "too many queries", http.StatusInternalServerError)
			return
		}
		before, err := strconv.ParseUint(q.Get("block_number__lt"), 10, 64)
		latest := time.Unix(1<<40, 0)
		if q.Has("execution_date__lte") {
			latest, _ = time.Parse(time.RFC3339, q.Get("execution_date__lte"))
		}
		if err != nil || q.Get("limit") != "200" || q.Has("offset") {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		results := []map[string]any{}
		for _, x := range transfers {
			kind := (q.Get("erc20") == "true" && x["type"] == types["erc20"]) || (q.Get("ether") == "true" && x["type"] == types["ether"])
			if kind && x["blockNumber"].(uint64) < before && !date(x).After(latest) &&
				(q.Get("_from") == "" || q.Get("_from") == x["from"].(ethrpc.Address).String()) &&
				(q.Get("to") == "" || q.Get("to") == x["to"].(ethrpc.Address).String()) {
				results = append(results, x)
			}
		}
		slices.SortStableFunc(results, func(a, b map[string]any) int {
			tie := cmp.Compare(a["blockNumber"].(uint64), b["blockNumber"].(uint64))
			if n%2 == 0 {
				tie = -tie
			}
			return cmp.Or(date(b).Compare(date(a)), tie)
		})
		json.NewEncoder(w).Encode(map[string]any{"count": len(results), "results": results[:min(len(results), transferPage)]})
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL, ethrpc.Mainnet), &queries
}

// transferAt returns a transfer as the service lists it, in block, four blocks
// a second, as on Arbitrum One.
func transferAt(kind string, block uint64, hash ethrpc.Hash, token *ethrpc.Address, from, to ethrpc.Address, value string) map[string]any {
	return map[string]any{
		"type": kind, "blockNumber": block, "transactionHash": hash, "tokenAddress": token, "from": from, "to": to, "value": value,
		"executionDate": time.Unix(int64(block/4), 0).UTC().Format(time.RFC3339),
	}
}

func TestTransfers(t *testing.T) {
	safe := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	token := ethrpc.MustParseAddress("0x7000000000000000000000000000000000000001")
	bob := ethrpc.MustParseAddress("0xb0b0000000000000000000000000000000000004")
	// An ERC-20 transfer out of the Safe in each of the blocks 1000 to 2001, so
	// that the pages end in the middle of the transfers of one time, then one to
	// bob and one after the last block, and native and ERC-20 transfers into the
	// Safe.
	var transfers []map[string]any
	for b := uint64(1000); b <= 2001; b++ {
		transfers = append(transfers, transferAt("ERC20_TRANSFER", b, ethrpc.Hash{30: byte(b >> 8), 31: byte(b)}, &token, safe, ethrpc.Address{18: byte(b >> 8), 19: byte(b)}, strconv.FormatUint(b, 10)))
	}
	transfers = append(transfers,
		transferAt("ERC20_TRANSFER", 1600, ethrpc.Hash{0: 1}, &token, safe, bob, "7"),
		transferAt("ERC20_TRANSFER", 2002, ethrpc.Hash{0: 2}, &token, safe, bob, "8"),
		transferAt("ETHER_TRANSFER", 1500, ethrpc.Hash{0: 3}, nil, bob, safe, "9"),
		transferAt("ERC20_TRANSFER", 1500, ethrpc.Hash{0: 4}, &token, bob, safe, "10"),
	)
	client, queries := serveTransfers(t, safe, transfers)

	got, err := client.Transfers(t.Context(), safe, TransferFilter{Kind: "erc20", From: &safe, ToBlock: 2001})
	if err != nil {
		t.Fatalf("Transfers: %v", err)
	}
	if len(got) != 1003 {
		t.Fatalf("Transfers: got %d transfers, want the 1,003 out of the Safe up to block 2001", len(got))
	}
	seen := make(map[ethrpc.Hash]bool)
	for _, x := range got {
		if seen[x.TxHash] || x.From != safe || x.Type != "ERC20_TRANSFER" || x.Token == nil || *x.Token != token || x.Block > 2001 {
			t.Fatalf("Transfers: got %+v, twice or not an ERC-20 transfer out of the Safe up to block 2001", x)
		}
		seen[x.TxHash] = true
	}
	// Each query after the first is for the transfers up to an earlier time. The
	// filters are the service's.
	var times []string
	for _, q := range *queries {
		times = append(times, q.Get("execution_date__lte"))
		if q.Get("erc20") != "true" || q.Get("_from") != safe.String() || q.Has("to") || q.Get("block_number__lt") != "2002" {
			t.Errorf("Transfers: got the query %v, want one for ERC-20 transfers from the Safe before block 2002", q)
		}
	}
	if len(times) < 6 || times[0] != "" || !slices.IsSortedFunc(times[1:], func(a, b string) int { return strings.Compare(b, a) }) || len(slices.Compact(slices.Clone(times))) != len(times) {
		t.Errorf("Transfers: listed the transfers up to the times %q, want none, then earlier and earlier ones", times)
	}

	if got, err := client.Transfers(t.Context(), safe, TransferFilter{Kind: "erc20", From: &safe, To: &bob, ToBlock: 2001}); err != nil || len(got) != 1 || got[0].Value.Int64() != 7 {
		t.Errorf("Transfers to bob: got %+v, %v, want the transfer of 7", got, err)
	}
	if got, err := client.Transfers(t.Context(), safe, TransferFilter{Kind: "ether", To: &safe, ToBlock: 2001}); err != nil || len(got) != 1 || got[0].Token != nil || got[0].Value.Int64() != 9 {
		t.Errorf("native Transfers into the Safe: got %+v, %v, want the transfer of 9", got, err)
	}
	// A Safe that the service knows can have none.
	if got, err := client.Transfers(t.Context(), safe, TransferFilter{Kind: "ether", To: &safe, ToBlock: 1000}); err != nil || got == nil || len(got) != 0 {
		t.Errorf("Transfers of none: got %v, %v, want an empty list", got, err)
	}
}

func TestTransfersErrors(t *testing.T) {
	safe := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	// More transfers at one time than the service lists at once.
	var transfers []map[string]any
	for i := range transferPage {
		transfers = append(transfers, transferAt("ERC20_TRANSFER", 1000, ethrpc.Hash{30: byte(i >> 8), 31: byte(i)}, &safe, safe, safe, "1"))
	}
	client, _ := serveTransfers(t, safe, transfers)
	if _, err := client.Transfers(t.Context(), safe, TransferFilter{Kind: "erc20", From: &safe, ToBlock: 2000}); err == nil || !strings.Contains(err.Error(), "200 transfers or more at 1970-01-01T00:04:10Z") {
		t.Errorf("Transfers of a full second: got error %v, want one about the second", err)
	}
	// The service lists nothing for a Safe that it doesn't know.
	other := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000002")
	if _, err := client.Transfers(t.Context(), other, TransferFilter{Kind: "erc20", From: &other, ToBlock: 2000}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Transfers of an unknown Safe: got error %v, want ErrNotFound", err)
	}
	// A service that leaves out the time filter lists the same transfers again.
	var page []map[string]any
	for b := range uint64(transferPage) {
		page = append(page, transferAt("ERC20_TRANSFER", 4000-4*b, ethrpc.Hash{30: byte(b >> 8), 31: byte(b)}, &safe, safe, safe, "1"))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"results": page})
	}))
	t.Cleanup(server.Close)
	if _, err := NewClient(server.URL, ethrpc.Mainnet).Transfers(t.Context(), safe, TransferFilter{Kind: "erc20", From: &safe, ToBlock: 5000}); err == nil || !strings.Contains(err.Error(), "after the") {
		t.Errorf("Transfers from a service without the time filter: got error %v, want one about a transfer after the time asked for", err)
	}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	if _, err := NewClient(server.URL, ethrpc.Mainnet).Transfers(t.Context(), safe, TransferFilter{Kind: "erc20", From: &safe, ToBlock: 2000}); err == nil ||
		errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "500") {
		t.Errorf("Transfers from a failing service: got error %v, want one with the status", err)
	}
	// The service lists nothing for a Safe that it doesn't know, so a listing that
	// isn't found is at a wrong URL.
	server = httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	if _, err := NewClient(server.URL, ethrpc.Mainnet).Transfers(t.Context(), safe, TransferFilter{Kind: "erc20", From: &safe, ToBlock: 2000}); err == nil ||
		errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "404") {
		t.Errorf("Transfers at a wrong URL: got error %v, want one with the status", err)
	}
}

func TestTransfersShortPages(t *testing.T) {
	// A service that lists fewer transfers than it is asked for, as one with a
	// smaller maximum page does, says that there are more with its next link.
	safe := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	const perPage = 100
	var transfers []map[string]any
	for b := uint64(1000); b < 1500; b++ {
		transfers = append(transfers, transferAt("ERC20_TRANSFER", b, ethrpc.Hash{30: byte(b >> 8), 31: byte(b)}, &safe, safe, safe, "1"))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		latest := time.Unix(1<<40, 0)
		if q := r.URL.Query(); q.Has("execution_date__lte") {
			latest, _ = time.Parse(time.RFC3339, q.Get("execution_date__lte"))
		}
		results := []map[string]any{}
		for i := len(transfers) - 1; i >= 0; i-- {
			if d, _ := time.Parse(time.RFC3339, transfers[i]["executionDate"].(string)); !d.After(latest) {
				results = append(results, transfers[i])
			}
		}
		var next any
		if len(results) > perPage {
			next = "https://example.invalid/next"
		}
		json.NewEncoder(w).Encode(map[string]any{"count": len(results), "next": next, "results": results[:min(len(results), perPage)]})
	}))
	t.Cleanup(server.Close)
	got, err := NewClient(server.URL, ethrpc.Mainnet).Transfers(t.Context(), safe, TransferFilter{Kind: "erc20", From: &safe, ToBlock: 2000})
	if err != nil || len(got) != len(transfers) {
		t.Errorf("Transfers: got %d transfers and error %v, want %d", len(got), err, len(transfers))
	}
	// More transfers at one time than such a service lists at once.
	transfers = nil
	for i := range 150 {
		transfers = append(transfers, transferAt("ERC20_TRANSFER", 1000, ethrpc.Hash{30: byte(i >> 8), 31: byte(i)}, &safe, safe, safe, "1"))
	}
	if _, err := NewClient(server.URL, ethrpc.Mainnet).Transfers(t.Context(), safe, TransferFilter{Kind: "erc20", From: &safe, ToBlock: 2000}); err == nil ||
		!strings.Contains(err.Error(), "100 transfers or more at 1970-01-01T00:04:10Z") {
		t.Errorf("Transfers of a full second: got error %v, want one about the 100 at that second", err)
	}
}
