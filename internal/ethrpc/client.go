// Package ethrpc is a minimal Ethereum JSON-RPC client over HTTP.
package ethrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// Client sends JSON-RPC requests to a single Ethereum node over HTTP.
type Client struct {
	chainID uint64
	url     string
	http    *http.Client
	id      atomic.Uint64
}

// connectTimeout bounds how long NewClient waits for each default RPC node to
// report its chain ID, so that one unresponsive node doesn't stall it.
var connectTimeout = 1 * time.Second

// NewClient returns a client for the chain with the given ID, using the node
// at url. If url is empty, the client uses the chain's default RPC URLs (see
// DefaultRPCs): it tries them one at a time, in order, and uses the first node
// that answers with the expected chain ID.
//
// It checks that the node serves the expected chain (eth_chainId), since the
// node may come from an untrusted list, and chains share contract addresses
// and call encodings, so a wrong chain would go unnoticed otherwise.
func NewClient(ctx context.Context, chainID uint64, url string) (*Client, error) {
	if url != "" {
		c := &Client{chainID: chainID, url: url, http: http.DefaultClient}
		if err := c.checkChainID(ctx); err != nil {
			return nil, err
		}
		return c, nil
	}

	urls, err := DefaultRPCs(ctx, chainID)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, url := range urls {
		c := &Client{chainID: chainID, url: url, http: http.DefaultClient}
		checkCtx, cancel := context.WithTimeout(ctx, connectTimeout)
		err := c.checkChainID(checkCtx)
		cancel()
		if err == nil {
			return c, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		errs = append(errs, err)
	}
	return nil, fmt.Errorf("no default RPC node for chain %d: %w", chainID, errors.Join(errs...))
}

// checkChainID returns an error unless the client's node serves the client's
// chain.
func (c *Client) checkChainID(ctx context.Context) error {
	var chainID Quantity
	if err := c.request(ctx, &chainID, "eth_chainId"); err != nil {
		return fmt.Errorf("%s: %w", c.url, err)
	}
	if uint64(chainID) != c.chainID {
		return fmt.Errorf("%s: serves chain %d, want chain %d", c.url, chainID, c.chainID)
	}
	return nil
}

// ChainID returns the ID of the chain that the client's node serves.
func (c *Client) ChainID() uint64 {
	return c.chainID
}

// Call executes a message call against the state at the given block without
// creating a transaction (eth_call), and returns the call's return data.
func (c *Client) Call(ctx context.Context, call CallRequest, block BlockNumber) (Bytes, error) {
	var result Bytes
	err := c.request(ctx, &result, "eth_call", call, block)
	return result, err
}

// BlockNumber returns the number of the node's most recent block
// (eth_blockNumber).
func (c *Client) BlockNumber(ctx context.Context) (BlockNumber, error) {
	var result BlockNumber
	err := c.request(ctx, &result, "eth_blockNumber")
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
