package ethrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// serve starts a JSON-RPC server that checks each request against want and
// answers with the JSON response object reply, with the request's ID filled
// in.
func serve(t *testing.T, want, reply string) *Client {
	t.Helper()
	wantRequest := canonical(t, want)
	response := decode(t, reply)

	// The handler runs on the server's goroutine, where t.Fatal must not be
	// called, so it only reports errors.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content type: got %q, want application/json", ct)
		}

		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id := req["id"]
		delete(req, "id")
		if got, _ := json.Marshal(req); string(got) != wantRequest {
			t.Errorf("request:\n got %s\nwant %s", got, wantRequest)
		}

		response["jsonrpc"], response["id"] = "2.0", id
		json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL)
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// canonical re-encodes the JSON object s as compact JSON with sorted keys, the
// same form that encoding a decoded request produces.
func canonical(t *testing.T, s string) string {
	t.Helper()
	data, err := json.Marshal(decode(t, s))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCall(t *testing.T) {
	client := serve(t,
		`{
			"jsonrpc": "2.0",
			"method": "eth_call",
			"params": [
				{"to": "0x00000000000000000000000000000000000000aa", "data": "0x12345678"},
				"0x10"
			]
		}`,
		`{"result": "0xcafe"}`,
	)

	result, err := client.Call(t.Context(), CallRequest{
		To:   Address{19: 0xaa},
		Data: Bytes{0x12, 0x34, 0x56, 0x78},
	}, 16)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !bytes.Equal(result, []byte{0xca, 0xfe}) {
		t.Errorf("result: got %s, want 0xcafe", result)
	}
}

func TestCallIncludesFrom(t *testing.T) {
	client := serve(t,
		`{
			"jsonrpc": "2.0",
			"method": "eth_call",
			"params": [
				{
					"from": "0x00000000000000000000000000000000000000bb",
					"to": "0x00000000000000000000000000000000000000aa"
				},
				"0x0"
			]
		}`,
		`{"result": "0x"}`,
	)

	result, err := client.Call(t.Context(), CallRequest{From: Address{19: 0xbb}, To: Address{19: 0xaa}}, 0)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("result: got %s, want 0x", result)
	}
}

func TestCallRPCError(t *testing.T) {
	client := serve(t,
		`{
			"jsonrpc": "2.0",
			"method": "eth_call",
			"params": [{"to": "0x0000000000000000000000000000000000000000"}, "0x1"]
		}`,
		`{"error": {"code": 3, "message": "execution reverted", "data": "0x08c379a0"}}`,
	)

	_, err := client.Call(t.Context(), CallRequest{}, 1)
	var rpcErr *Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("Call: got %v, want an *Error", err)
	}
	if rpcErr.Code != 3 || rpcErr.Message != "execution reverted" || string(rpcErr.Data) != `"0x08c379a0"` {
		t.Errorf("error: got %+v", rpcErr)
	}
}

func TestBlockNumber(t *testing.T) {
	client := serve(t,
		`{"jsonrpc": "2.0", "method": "eth_blockNumber", "params": []}`,
		`{"result": "0x2625a00"}`,
	)

	number, err := client.BlockNumber(t.Context())
	if err != nil {
		t.Fatalf("BlockNumber: %v", err)
	}
	if number != 40_000_000 {
		t.Errorf("BlockNumber: got %d, want 40000000", number)
	}
}

func TestRequestHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	if _, err := NewClient(server.URL).Call(t.Context(), CallRequest{}, 1); err == nil {
		t.Fatal("Call: expected an error")
	}
}

func TestRequestMismatchedID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"jsonrpc": "2.0", "id": 1000, "result": "0x"}`)
	}))
	t.Cleanup(server.Close)

	if _, err := NewClient(server.URL).Call(t.Context(), CallRequest{}, 1); err == nil {
		t.Fatal("Call: expected an error")
	}
}
