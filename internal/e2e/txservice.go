package e2e

import (
	"encoding/json"
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

// TxService is a Safe Transaction Service that lists the executions of a test,
// in the order given, for the Safes that they are of, which are the Safes that
// it knows. Like the service, it lists nothing for a Safe that it doesn't know.
type TxService struct {
	*httptest.Server
	mu         sync.Mutex
	executions []TxServiceExecution
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

func (s *TxService) serve(w http.ResponseWriter, r *http.Request) {
	path, found := strings.CutPrefix(r.URL.Path, "/api/v1/safes/")
	address, endpoint, _ := strings.Cut(path, "/")
	safe, err := ethrpc.ParseAddress(address)
	// The service requires addresses with their checksum.
	if !found || err != nil || safe.String() != address {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	known := false
	results := []map[string]any{}
	for _, e := range s.executions {
		if tx := e.Transaction; tx.Safe == safe {
			known = true
			results = append(results, map[string]any{
				"safe": tx.Safe, "to": tx.To, "value": tx.Value.String(), "data": tx.Data,
				"operation": uint8(tx.Operation), "safeTxGas": tx.SafeTxGas.String(), "baseGas": tx.BaseGas.String(),
				"gasPrice": tx.GasPrice.String(), "gasToken": tx.GasToken, "refundReceiver": tx.RefundReceiver,
				"nonce": tx.Nonce.Uint64(), "safeTxHash": e.SafeTxHash, "transactionHash": e.TxHash,
				"blockNumber": e.Block, "isExecuted": true, "isSuccessful": true,
			})
		}
	}
	s.mu.Unlock()
	switch {
	case endpoint == "" && known:
		json.NewEncoder(w).Encode(map[string]any{"address": safe})
		return
	case endpoint != "multisig-transactions/":
		http.NotFound(w, r)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	page := results[min(offset, len(results)):min(offset+limit, len(results))]
	json.NewEncoder(w).Encode(map[string]any{"count": len(results), "results": page})
}
