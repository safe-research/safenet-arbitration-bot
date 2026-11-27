// Package txservice is a client for the Safe Transaction Service, which indexes
// the Safe transactions that owners have signed and submitted, executed or not.
//
// The service is not trusted: it is an offchain source (Charter § 3.4). Callers
// check what it returns against onchain data, such as the hash of the Safe
// transaction.
package txservice

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/httpretry"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// DefaultURLs are the base URLs of Safe's public transaction services, by chain
// ID, for the networks that the Charter covers. They need no API key.
var DefaultURLs = map[uint64]string{
	ethrpc.Mainnet:     "https://api.safe.global/tx-service/eth",
	ethrpc.ArbitrumOne: "https://api.safe.global/tx-service/arb1",
	ethrpc.Gnosis:      "https://api.safe.global/tx-service/gno",
}

// maxResponseSize is the largest response that the client accepts. A Safe
// transaction has calldata, which is bounded by the size of a block.
const maxResponseSize = 8 << 20

// ErrNotFound is the error for a transaction or Safe that the service doesn't
// have.
var ErrNotFound = errors.New("not found by the service")

// StatusError is a response with an HTTP error status other than 404 Not Found.
type StatusError struct {
	Status     string
	StatusCode int
	// Body is the start of the response body.
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %s: %q", e.Status, e.Body)
}

// notJSON is a response with HTTP status 200 whose body isn't JSON, such as an
// empty body or a proxy's HTML page, which isn't the service's answer.
type notJSON struct{ error }

func (e notJSON) Unwrap() error { return e.error }

// Unavailable reports whether err is a failure to get an answer from the
// service, such as a rate limit, a server error, a page that isn't JSON, or a
// dropped connection, as ethrpc.Transient is for a node, rather than its
// answer.
func Unavailable(err error) bool {
	if statusErr, ok := errors.AsType[*StatusError](err); ok {
		return statusErr.StatusCode == http.StatusTooManyRequests || statusErr.StatusCode >= 500
	}
	if _, ok := errors.AsType[notJSON](err); ok {
		return true
	}
	return ethrpc.Transient(err)
}

// Client fetches Safe transactions from a Safe Transaction Service.
type Client struct {
	http    *http.Client
	baseURL string
	chainID *big.Int
	// confirming asks for the about page of the server at baseURL once, and
	// confirmErr is its error if it didn't answer as a service.
	confirming sync.Once
	confirmErr error
}

// NewClient returns a client for the service at the base URL baseURL, such as
// "https://api.safe.global/tx-service/eth", which serves the Safes of the chain
// with the given ID.
func NewClient(baseURL string, chainID uint64) *Client {
	return &Client{http: httpretry.Client, baseURL: baseURL, chainID: new(big.Int).SetUint64(chainID)}
}

// URL returns the base URL of the service.
func (c *Client) URL() string {
	return c.baseURL
}

// Transaction returns the Safe transaction that the service has with the Safe
// transaction hash safeTxHash, with its execution, if the service says that it
// was executed, or ErrNotFound. The transaction's fields and its execution are
// as the service reports them, so it may not have the hash it was asked for.
func (c *Client) Transaction(ctx context.Context, safeTxHash ethrpc.Hash) (*Execution, error) {
	u, err := url.JoinPath(c.baseURL, "api", "v1", "multisig-transactions", safeTxHash.String())
	if err != nil {
		return nil, err
	}
	// The service redirects paths without a trailing slash.
	var l listing
	err = c.get(ctx, u+"/", &l)
	// The service answers 404 for a transaction that it doesn't have, but so does
	// any server for a wrong URL.
	if errors.Is(err, ErrNotFound) {
		if err := c.confirm(ctx); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	x := l.execution(c.chainID)
	return &x, nil
}

// confirm returns an error unless the server at the base URL is a service,
// which answers its about page. It asks once, and remembers the answer.
func (c *Client) confirm(ctx context.Context) error {
	c.confirming.Do(func() { c.confirmErr = c.about(ctx) })
	return c.confirmErr
}

// about returns an error unless the server at the base URL answers the about
// page of a service, which has the service's name. Other servers have about
// pages too, such as Safe's Config Service.
func (c *Client) about(ctx context.Context) error {
	u, err := url.JoinPath(c.baseURL, "api", "v1", "about")
	if err != nil {
		return err
	}
	var about struct {
		Name string `json:"name"`
	}
	err = c.get(ctx, u+"/", &about)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("HTTP 404 Not Found: the service at %q has no about page, so it isn't a Safe Transaction Service", c.baseURL)
	}
	if err != nil {
		return fmt.Errorf("getting the about page of the service at %q: %w", c.baseURL, err)
	}
	if about.Name != "Safe Transaction Service" {
		return fmt.Errorf("the about page of the service at %q names %q, so it isn't a Safe Transaction Service", c.baseURL, snippet([]byte(about.Name)))
	}
	return nil
}

// Execution is a Safe transaction that the service has, with the onchain
// transaction and block that it says executed it.
type Execution struct {
	// Transaction is the Safe transaction, with its fields as the service reports
	// them.
	Transaction *safenet.SafeTransaction
	// SafeTxHash, TxHash, and Block are the hash of the Safe transaction, the hash
	// of the onchain transaction that executed it, and its block, as the service
	// reports them. TxHash and Block are zero if the service doesn't report them.
	SafeTxHash ethrpc.Hash
	TxHash     ethrpc.Hash
	Block      uint64
	// Executed is whether the service says that the transaction was executed, as it
	// says of every transaction that Executions lists.
	Executed bool
}

// listing is a Safe transaction as the service lists it, with its execution.
type listing struct {
	transaction
	SafeTxHash      ethrpc.Hash  `json:"safeTxHash"`
	TransactionHash *ethrpc.Hash `json:"transactionHash"`
	BlockNumber     *uint64      `json:"blockNumber"`
	IsExecuted      *bool        `json:"isExecuted"`
}

// execution returns l as an execution of a Safe transaction on the chain with
// the ID chainID.
func (l *listing) execution(chainID *big.Int) Execution {
	x := Execution{SafeTxHash: l.SafeTxHash, Transaction: l.safeTransaction(chainID), Executed: l.IsExecuted != nil && *l.IsExecuted}
	// An execution without its onchain transaction or block is still listed, so
	// that the caller reports it rather than drop it.
	if l.TransactionHash != nil {
		x.TxHash = *l.TransactionHash
	}
	if l.BlockNumber != nil {
		x.Block = *l.BlockNumber
	}
	return x
}

// pageSize is the number of results that the client asks for in each request of
// a listing.
const pageSize = 100

// MaxListed bounds the number of results of a listing.
const MaxListed = 100_000

// Executions returns the Safe transactions of safe that the service lists as
// executed, in the order of their nonces, as it reports them: none of their
// fields, hashes, or blocks are checked. They are the multisig transactions
// that owners signed, so executions by modules aren't included. It returns
// ErrNotFound if the service doesn't know the Safe.
func (c *Client) Executions(ctx context.Context, safe ethrpc.Address) ([]Execution, error) {
	results, err := list[listing](ctx, c, safe, "multisig-transactions", url.Values{"executed": {"true"}, "ordering": {"nonce"}})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		if err := c.known(ctx, safe); err != nil {
			return nil, err
		}
	}
	executions := []Execution{}
	for _, r := range results {
		if r.IsExecuted != nil && !*r.IsExecuted {
			continue
		}
		// The listing is of executed transactions, so one that doesn't say is.
		x := r.execution(c.chainID)
		x.Executed = true
		executions = append(executions, x)
	}
	return executions, nil
}

// Transfer is a transfer into or out of a Safe that the service lists.
type Transfer struct {
	// Type is the service's type of the transfer, such as "ERC20_TRANSFER" or
	// "ETHER_TRANSFER".
	Type   string
	Block  uint64
	TxHash ethrpc.Hash
	// LogIndex is the index in its block of the log of an ERC-20 transfer, from the
	// service's ID of the transfer, or nil if the ID doesn't have it.
	LogIndex *uint64
	// Token is the token's contract, or nil for the native currency.
	Token *ethrpc.Address
	From  ethrpc.Address
	To    ethrpc.Address
	// Value is the amount in the token's or the currency's smallest unit, or nil
	// for a transfer without one, such as of an ERC-721 token.
	Value *big.Int
}

// TransferFilter selects the transfers of a Safe that Transfers lists.
type TransferFilter struct {
	// Kind is "erc20" or "ether", the service's filters for ERC-20 transfers and
	// for transfers of the native currency.
	Kind string
	// From and To are the sender and the recipient of the transfers, if they are
	// set. One of them is the Safe, which keeps the transfers to one direction.
	From, To *ethrpc.Address
	// ToBlock is the last block of the transfers.
	ToBlock uint64
}

// transferPage is the number of transfers that Transfers asks for in each
// request, the most that the service returns at once.
const transferPage = 200

// Transfers returns the transfers of safe that filter selects and the service
// lists, from the newest, as it reports them: none of them are checked. The
// service filters them, so the transfers of other kinds and directions don't
// count towards MaxListed. It returns ErrNotFound if the service doesn't know
// the Safe.
func (c *Client) Transfers(ctx context.Context, safe ethrpc.Address, filter TransferFilter) ([]Transfer, error) {
	// The service rejects addresses without their EIP-55 checksum.
	base, err := url.JoinPath(c.baseURL, "api", "v1", "safes", safe.String(), "transfers")
	if err != nil {
		return nil, err
	}
	query := url.Values{
		filter.Kind:        {"true"},
		"block_number__lt": {strconv.FormatUint(filter.ToBlock+1, 10)},
		"limit":            {strconv.Itoa(transferPage)},
	}
	if filter.From != nil {
		query.Set("_from", filter.From.String())
	}
	if filter.To != nil {
		query.Set("to", filter.To.String())
	}
	transfers := []Transfer{}
	// The service sorts the transfers by their time alone, which several blocks can
	// share, and lists only the newest of a query, so pages by offset can list a
	// transfer twice or not at all. Each request is for the first page of the
	// transfers up to the oldest time of the page before, whose transfers are
	// listed again in full, since the page may have cut them off.
	var before *time.Time
	for {
		if before != nil {
			query.Set("execution_date__lte", before.UTC().Format(time.RFC3339Nano))
		}
		var page struct {
			Next    *string `json:"next"`
			Results []struct {
				Type            string          `json:"type"`
				BlockNumber     uint64          `json:"blockNumber"`
				TransactionHash ethrpc.Hash     `json:"transactionHash"`
				TokenAddress    *ethrpc.Address `json:"tokenAddress"`
				From            ethrpc.Address  `json:"from"`
				To              ethrpc.Address  `json:"to"`
				Value           *number         `json:"value"`
				ExecutionDate   time.Time       `json:"executionDate"`
				TransferID      string          `json:"transferId"`
			} `json:"results"`
		}
		if err := c.getListing(ctx, base+"/?"+query.Encode(), &page); err != nil {
			return nil, err
		}
		// A request after the first is for the transfers up to the oldest time of the
		// page before, so it lists those at that time again.
		if len(page.Results) == 0 && before != nil {
			return nil, fmt.Errorf("the service lists no transfers up to %s, though it listed some at that time", before.Format(time.RFC3339))
		}
		if len(page.Results) == 0 && page.Next != nil {
			return nil, errors.New("the service lists no transfers, but has a next page")
		}
		var oldest time.Time
		for i, r := range page.Results {
			if before != nil && r.ExecutionDate.After(*before) {
				return nil, fmt.Errorf("the service lists a transfer at %s, after the %s that it was asked for", r.ExecutionDate.Format(time.RFC3339), before.Format(time.RFC3339))
			}
			if i == 0 || r.ExecutionDate.Before(oldest) {
				oldest = r.ExecutionDate
			}
		}
		// A service can list fewer transfers than it is asked for, and then has a next
		// page.
		full := len(page.Results) == transferPage || page.Next != nil
		listed := len(transfers)
		for _, r := range page.Results {
			if full && r.ExecutionDate.Equal(oldest) {
				continue
			}
			t := Transfer{Type: r.Type, Block: r.BlockNumber, TxHash: r.TransactionHash, Token: r.TokenAddress, From: r.From, To: r.To}
			if r.Value != nil {
				t.Value = r.Value.Int
			}
			// The ID of a transfer that a log shows is "e", the transaction hash in hex,
			// and the log index.
			if index, ok := strings.CutPrefix(r.TransferID, "e"+hex.EncodeToString(r.TransactionHash[:])); ok {
				if n, err := strconv.ParseUint(index, 10, 64); err == nil {
					t.LogIndex = &n
				}
			}
			transfers = append(transfers, t)
		}
		if full && len(transfers) == listed {
			return nil, fmt.Errorf("the service lists %d transfers or more at %s, more than it lists at once", len(page.Results), oldest.Format(time.RFC3339))
		}
		if len(transfers) > MaxListed {
			return nil, fmt.Errorf("the service lists more than %d transfers", MaxListed)
		}
		if !full {
			break
		}
		before = &oldest
	}
	if len(transfers) == 0 {
		if err := c.known(ctx, safe); err != nil {
			return nil, err
		}
	}
	return transfers, nil
}

// list returns the results of the listing of safe at endpoint, such as
// "multisig-transactions", with the query, in pages of pageSize, up to
// MaxListed.
func list[T any](ctx context.Context, c *Client, safe ethrpc.Address, endpoint string, query url.Values) ([]T, error) {
	// The service rejects addresses without their EIP-55 checksum.
	base, err := url.JoinPath(c.baseURL, "api", "v1", "safes", safe.String(), endpoint)
	if err != nil {
		return nil, err
	}
	query.Set("limit", strconv.Itoa(pageSize))
	results := []T{}
	for offset := 0; ; {
		// The pages are by offset rather than the service's next links, so that every
		// request is to the service.
		query.Set("offset", strconv.Itoa(offset))
		var page struct {
			Next    *string `json:"next"`
			Results []T     `json:"results"`
		}
		if err := c.getListing(ctx, base+"/?"+query.Encode(), &page); err != nil {
			return nil, err
		}
		results = append(results, page.Results...)
		if len(results) > MaxListed {
			return nil, fmt.Errorf("the service lists more than %d %s", MaxListed, endpoint)
		}
		// A service can list fewer results than it is asked for, and then has a next
		// page.
		if page.Next == nil && len(page.Results) < pageSize {
			return results, nil
		}
		if len(page.Results) == 0 {
			return nil, fmt.Errorf("the service lists no %s at offset %d, but has a next page", endpoint, offset)
		}
		offset += len(page.Results)
	}
}

// known returns ErrNotFound if the service doesn't know safe. The service lists
// nothing for a Safe that it doesn't know, rather than fail, so an empty
// listing is only one of a Safe without such entries if the service knows the
// Safe.
func (c *Client) known(ctx context.Context, safe ethrpc.Address) error {
	u, err := url.JoinPath(c.baseURL, "api", "v1", "safes", safe.String())
	if err != nil {
		return err
	}
	return c.get(ctx, u+"/", &struct{}{})
}

// Creation returns the hash of the transaction that the service says created
// the Safe, or ErrNotFound.
func (c *Client) Creation(ctx context.Context, safe ethrpc.Address) (ethrpc.Hash, error) {
	u, err := url.JoinPath(c.baseURL, "api", "v1", "safes", safe.String(), "creation")
	if err != nil {
		return ethrpc.Hash{}, err
	}
	var result struct {
		TransactionHash *ethrpc.Hash `json:"transactionHash"`
	}
	if err := c.get(ctx, u+"/", &result); err != nil {
		return ethrpc.Hash{}, err
	}
	if result.TransactionHash == nil {
		return ethrpc.Hash{}, ErrNotFound
	}
	return *result.TransactionHash, nil
}

// getListing is get for a page of a listing, which is never ErrNotFound: the
// service lists nothing for a Safe that it doesn't know, rather than fail, so a
// listing that it doesn't have is at a wrong URL.
func (c *Client) getListing(ctx context.Context, u string, v any) error {
	err := c.get(ctx, u, v)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("HTTP 404 Not Found: the service at %q has no such listing", c.baseURL)
	}
	return err
}

// get fetches the JSON document at u, and decodes it into v. It returns
// ErrNotFound if the service doesn't have it.
func (c *Client) get(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return &StatusError{Status: resp.Status, StatusCode: resp.StatusCode, Body: snippet(body)}
	case len(body) > maxResponseSize:
		return fmt.Errorf("response exceeds %d bytes", maxResponseSize)
	}
	if err := json.Unmarshal(body, v); err != nil {
		// JSON that doesn't decode is the service's answer, if a malformed one, but a
		// body that isn't JSON, such as an error page, isn't an answer.
		if !json.Valid(body) {
			err = notJSON{err}
		}
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// transaction is a Safe transaction as the service encodes it, with the fields
// that the client uses.
type transaction struct {
	Safe           ethrpc.Address `json:"safe"`
	To             ethrpc.Address `json:"to"`
	Value          number         `json:"value"`
	Data           *ethrpc.Bytes  `json:"data"`
	Operation      uint8          `json:"operation"`
	SafeTxGas      number         `json:"safeTxGas"`
	BaseGas        number         `json:"baseGas"`
	GasPrice       number         `json:"gasPrice"`
	GasToken       ethrpc.Address `json:"gasToken"`
	RefundReceiver ethrpc.Address `json:"refundReceiver"`
	Nonce          number         `json:"nonce"`
}

// safeTransaction returns t as a Safe transaction on the chain with the ID
// chainID.
func (t *transaction) safeTransaction(chainID *big.Int) *safenet.SafeTransaction {
	tx := &safenet.SafeTransaction{
		ChainID:        new(big.Int).Set(chainID),
		Safe:           t.Safe,
		To:             t.To,
		Value:          t.Value.Int,
		Operation:      safenet.Operation(t.Operation),
		SafeTxGas:      t.SafeTxGas.Int,
		BaseGas:        t.BaseGas.Int,
		GasPrice:       t.GasPrice.Int,
		GasToken:       t.GasToken,
		RefundReceiver: t.RefundReceiver,
		Nonce:          t.Nonce.Int,
	}
	if t.Data != nil {
		tx.Data = *t.Data
	}
	return tx
}

// number is an integer that the service encodes as a JSON number or as a
// string, depending on the field.
type number struct {
	*big.Int
}

func (n *number) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		s = string(bytes.TrimSpace(data))
	}
	n.Int = new(big.Int)
	if _, ok := n.SetString(s, 10); !ok || n.Sign() < 0 {
		return fmt.Errorf("invalid integer %q", s)
	}
	return nil
}

// snippet returns the start of a response body, for an error message, which
// quotes it since it isn't trusted.
func snippet(body []byte) string {
	const limit = 200
	if len(body) > limit {
		body = body[:limit]
	}
	return string(bytes.TrimSpace(body))
}
