// Package ethrpc is a minimal Ethereum JSON-RPC client over HTTP.
package ethrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

// Client sends JSON-RPC requests to a single Ethereum node over HTTP.
type Client struct {
	url  string
	http *http.Client
	id   atomic.Uint64
}

// NewClient returns a client for the node at url.
func NewClient(url string) *Client {
	return &Client{url: url, http: http.DefaultClient}
}

// Call executes a message call against the state at the given block without
// creating a transaction (eth_call), and returns the call's return data.
func (c *Client) Call(ctx context.Context, call CallRequest, block BlockNumber) (Bytes, error) {
	var result Bytes
	err := c.request(ctx, &result, "eth_call", call, block)
	return result, err
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// request calls method with params and decodes its result into result. A
// JSON-RPC error response is returned as an *Error.
func (c *Client) request(ctx context.Context, result any, method string, params ...any) error {
	if params == nil {
		params = []any{}
	}
	id := c.id.Add(1)
	body, err := json.Marshal(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("%s: encoding request: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: HTTP %s: %s", method, resp.Status, bytes.TrimSpace(snippet))
	}

	var res response
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return fmt.Errorf("%s: decoding response: %w", method, err)
	}
	if res.ID != id {
		return fmt.Errorf("%s: response ID %d does not match request ID %d", method, res.ID, id)
	}
	if res.Error != nil {
		return fmt.Errorf("%s: %w", method, res.Error)
	}
	if len(res.Result) == 0 {
		return fmt.Errorf("%s: response has neither result nor error", method)
	}
	if err := json.Unmarshal(res.Result, result); err != nil {
		return fmt.Errorf("%s: decoding result: %w", method, err)
	}
	return nil
}
