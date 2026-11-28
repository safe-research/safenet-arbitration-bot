package e2e

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// TxServiceExecution is an execution that a TxService lists, with the hashes
// and block that it reports, which can be wrong.
type TxServiceExecution struct {
	Transaction *safenet.SafeTransaction
	SafeTxHash  ethrpc.Hash
	TxHash      ethrpc.Hash
	Block       uint64
}

// TxServiceTransfer is a transfer that a TxService lists, which can be wrong.
type TxServiceTransfer struct {
	// Type is the service's type, such as "ERC20_TRANSFER" or "ETHER_TRANSFER".
	Type   string
	Block  uint64
	TxHash ethrpc.Hash
	// Token is nil for the native currency.
	Token    *ethrpc.Address
	From, To ethrpc.Address
	Value    *big.Int
}

// TxService is a Safe Transaction Service that lists the executions and
// transfers of a test, in the order given, for the Safes that they are of,
// which are the Safes that it knows, and has the executions by their Safe
// transaction hashes. Like the service, it lists nothing for a Safe that it
// doesn't know, and filters the transfers by kind, sender, recipient, and
// block.
type TxService struct {
	*httptest.Server
	mu         sync.Mutex
	executions []TxServiceExecution
	transfers  []TxServiceTransfer
}

// NewTxService starts a TxService that lists nothing.
func NewTxService(tb testing.TB) *TxService {
	tb.Helper()
	s := &TxService{}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	tb.Cleanup(s.Close)
	return s
}

// ListExecutions adds the executions to those that the service lists.
func (s *TxService) ListExecutions(executions ...TxServiceExecution) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executions = append(s.executions, executions...)
}

// ListTransfers adds the transfers to those that the service lists.
func (s *TxService) ListTransfers(transfers ...TxServiceTransfer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transfers = append(s.transfers, transfers...)
}

// result returns the execution as the service encodes it.
func (e *TxServiceExecution) result() map[string]any {
	tx := e.Transaction
	return map[string]any{
		"safe": tx.Safe, "to": tx.To, "value": tx.Value.String(), "data": tx.Data,
		"operation": uint8(tx.Operation), "safeTxGas": tx.SafeTxGas.String(), "baseGas": tx.BaseGas.String(),
		"gasPrice": tx.GasPrice.String(), "gasToken": tx.GasToken, "refundReceiver": tx.RefundReceiver,
		"nonce": tx.Nonce.Uint64(), "safeTxHash": e.SafeTxHash, "transactionHash": e.TxHash,
		"blockNumber": e.Block, "isExecuted": true, "isSuccessful": true,
	}
}

func (s *TxService) serve(w http.ResponseWriter, r *http.Request) {
	// A transaction by its Safe transaction hash, as the first execution listed
	// with it.
	if hash, ok := strings.CutPrefix(r.URL.Path, "/api/v1/multisig-transactions/"); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, e := range s.executions {
			if e.SafeTxHash.String()+"/" == hash {
				json.NewEncoder(w).Encode(e.result())
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	path, found := strings.CutPrefix(r.URL.Path, "/api/v1/safes/")
	address, endpoint, _ := strings.Cut(path, "/")
	safe, err := ethrpc.ParseAddress(address)
	// The service requires addresses with their checksum.
	if !found || err != nil || safe.String() != address {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	before, err := strconv.ParseUint(q.Get("block_number__lt"), 10, 64)
	if err != nil {
		before = ^uint64(0)
	}
	kinds := map[string]string{"ERC20_TRANSFER": "erc20", "ETHER_TRANSFER": "ether"}
	s.mu.Lock()
	known := false
	executions, transfers := []map[string]any{}, []map[string]any{}
	for _, e := range s.executions {
		if e.Transaction.Safe == safe {
			known = true
			executions = append(executions, e.result())
		}
	}
	for _, t := range s.transfers {
		if t.From != safe && t.To != safe {
			continue
		}
		known = true
		if q.Get(kinds[t.Type]) == "true" && t.Block < before &&
			(q.Get("_from") == "" || q.Get("_from") == t.From.String()) && (q.Get("to") == "" || q.Get("to") == t.To.String()) {
			transfers = append(transfers, map[string]any{
				"type": t.Type, "blockNumber": t.Block, "transactionHash": t.TxHash, "tokenAddress": t.Token,
				"from": t.From, "to": t.To, "value": t.Value.String(),
			})
		}
	}
	s.mu.Unlock()
	var results []map[string]any
	switch {
	case endpoint == "" && known:
		json.NewEncoder(w).Encode(map[string]any{"address": safe})
		return
	case endpoint == "multisig-transactions/" && known:
		results = executions
	case endpoint == "transfers/" && known:
		results = transfers
	case endpoint != "multisig-transactions/" && endpoint != "transfers/":
		http.NotFound(w, r)
		return
	default:
		results = []map[string]any{}
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	page := results[min(offset, len(results)):min(offset+limit, len(results))]
	json.NewEncoder(w).Encode(map[string]any{"count": len(results), "results": page})
}
