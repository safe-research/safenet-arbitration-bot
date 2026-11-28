package e2e_test

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/e2e"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/executions"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

// historyOutput is the JSON output of `arbot executions -all`.
type historyOutput struct {
	executionsOutput
	History struct {
		Service struct {
			URL        string    `json:"url"`
			ObservedAt time.Time `json:"observedAt"`
		} `json:"service"`
		Nonce         *big.Int                `json:"nonce"`
		Complete      bool                    `json:"complete"`
		MissingNonces []executions.NonceRange `json:"missingNonces"`
		Scanned       []executions.BlockRange `json:"scanned"`
		Rejected      []executions.Rejected   `json:"rejected"`
		Creation      *executions.Creation    `json:"creation"`
	} `json:"history"`
}

// TestExecutionsAll runs `arbot executions -all` against real Safe 1.3.0
// contracts on anvil, with a Safe Transaction Service that lists the Safe's
// executions, one of them wrongly, and leaves one out.
func TestExecutionsAll(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	r := n.Propose(nil)
	mainnet, a := n.Mainnet, n.Artifacts
	safe := r.Transaction.Safe
	// The Mainnet node only keeps the states of its last 128 blocks, so its blocks
	// are mined up to just before the proposal first, and the transactions that
	// follow are in the last of them.
	mainnet.MineUntil(n.Header(r.Block).Time().Add(-3 * time.Minute))
	owner := ethrpc.Address{0: 0x0e, 19: 1}
	target := ethrpc.Address{0: 0xa1, 19: 1}
	mainnet.SetBalance(owner, new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil))
	n.SetupSafe(mainnet, safe, a.SafeSingleton, owner)

	// exec has the owner execute a Safe transaction of the Safe with data, and
	// returns what arbot should report about it, from source, and what the service
	// lists.
	exec := func(data, source string) (executionJSON, e2e.TxServiceExecution) {
		t.Helper()
		tx := safenet.SafeTransaction{
			ChainID: big.NewInt(ethrpc.Mainnet), Safe: safe, To: target, Value: new(big.Int), Data: []byte(data),
			SafeTxGas: new(big.Int), BaseGas: new(big.Int), GasPrice: new(big.Int), Nonce: n.SafeNonce(mainnet, safe),
		}
		receipt := mainnet.Transact(owner, safe, n.ExecCalldata(&tx, owner))
		x := executionJSON{
			Block: uint64(receipt.BlockNumber), BlockTime: mainnet.Header(uint64(receipt.BlockNumber)).Time(), TxHash: receipt.TransactionHash,
			SafeTxHash: tx.Hash(), Success: true, Payment: new(big.Int), Source: source, Transaction: &tx,
		}
		return x, e2e.TxServiceExecution{Transaction: &tx, SafeTxHash: tx.Hash(), TxHash: receipt.TransactionHash, Block: x.Block}
	}
	first, listedFirst := exec("first", "service")
	unlisted, _ := exec("unlisted", "calldata")
	third, listedThird := exec("third", "service")
	misreported, listedMisreported := exec("misreported", "calldata")
	// The service reports a transaction that isn't the one executed, with the hash
	// of the one executed, so the transaction is decoded from the chain.
	listedMisreported.Transaction = &safenet.SafeTransaction{
		ChainID: big.NewInt(ethrpc.Mainnet), Safe: safe, To: target, Value: big.NewInt(1), Data: []byte("other"),
		SafeTxGas: new(big.Int), BaseGas: new(big.Int), GasPrice: new(big.Int), Nonce: misreported.Transaction.Nonce,
	}
	// An execution after the proposal, which the evidence must not include.
	mainnet.MineUntil(n.Header(r.Block).Time())
	_, listedLater := exec("later", "service")
	service := e2e.NewTxService(t)
	service.ListExecutions(listedFirst, listedThird, listedMisreported, listedLater)

	arbot := n.Arbot().WithTxService(t, ethrpc.Mainnet, service.URL)
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	info := arbot.Run(t, "info", "-json", r.ID.String())
	if err := os.WriteFile(path, info, 0o644); err != nil {
		t.Fatal(err)
	}

	// It reads the blocks that the service names rather than a range, so it has no
	// first block.
	out := arbot.Run(t, "executions", "-all", "-json", "-request-file", path)
	got := decode[historyOutput](t, out)
	h := got.History
	if _, ok := decode[map[string]any](t, out)["fromBlock"]; got.Safe != safe || got.ChainID != ethrpc.Mainnet || ok || h.Service.URL != service.URL || h.Service.ObservedAt.IsZero() {
		t.Errorf("executions -all: got Safe %s on chain %d, from %q at %s, want %s on chain 1, from the service, without a first block", got.Safe, got.ChainID, h.Service.URL, h.Service.ObservedAt, safe)
	}
	// The unlisted execution is found by scanning the blocks between the first and
	// the third, and the misreported one in the block that the service names, but
	// as its nonce is unknown until it is decoded, the blocks after the third are
	// scanned too.
	if want := []executionJSON{first, unlisted, third, misreported}; !reflect.DeepEqual(canonical(t, got.Executions), canonical(t, want)) {
		g, _ := json.MarshalIndent(got.Executions, "", "  ")
		w, _ := json.MarshalIndent(want, "", "  ")
		t.Errorf("executions -all:\ngot  %s\nwant %s", g, w)
	}
	if len(h.Rejected) != 1 || h.Rejected[0].SafeTxHash != misreported.SafeTxHash {
		t.Errorf("executions -all: got rejected %+v, want the misreported transaction", h.Rejected)
	}
	scanned := []executions.BlockRange{{FromBlock: first.Block, ToBlock: third.Block}, {FromBlock: third.Block, ToBlock: got.ToBlock}}
	if h.Nonce == nil || h.Nonce.Int64() != 4 || !h.Complete || len(h.MissingNonces) != 0 || !reflect.DeepEqual(h.Scanned, scanned) {
		t.Errorf("executions -all: got nonce %v with missing nonces %v and blocks %v scanned, want 4, complete, with %v scanned", h.Nonce, h.MissingNonces, h.Scanned, scanned)
	}
	// The Safe's account was set up without a transaction, so it has no creation.
	if h.Creation != nil {
		t.Errorf("executions -all: got the creation %+v, want none", h.Creation)
	}

	// The execution of the request's own Safe transaction is marked, here for a
	// request whose Safe transaction is the third.
	var request map[string]any
	if err := json.Unmarshal(info, &request); err != nil {
		t.Fatal(err)
	}
	request["proposal"].(map[string]any)["safeTxHash"] = third.SafeTxHash.String()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	pathThird := filepath.Join(dir, "request-third.json")
	if err := os.WriteFile(pathThird, data, 0o644); err != nil {
		t.Fatal(err)
	}
	own := decode[historyOutput](t, arbot.Run(t, "executions", "-all", "-json", "-request-file", pathThird))
	ownThird := third
	ownThird.Proposal = true
	if !reflect.DeepEqual(canonical(t, own.Executions), canonical(t, []executionJSON{first, unlisted, ownThird, misreported})) || !own.History.Complete {
		t.Errorf("executions -all of the third: got executions %+v, want the third marked", own.Executions)
	}
	ownText := arbot.Run(t, "executions", "-all", "-request-file", pathThird)
	if want := `(?m)^The execution in block \d+, transaction ` + third.TxHash.String() + `, is of the request's own Safe transaction, before the proposal, so it is onchain data \(§ 2\.8\)\.$`; !regexp.MustCompile(want).Match(ownText) {
		t.Errorf("executions -all of the third: output has no line matching %q:\n%s", want, ownText)
	}
	// Without -all, it is marked too, and only it has the key.
	plain := arbot.Run(t, "executions", "-json", "-request-file", pathThird)
	if n := strings.Count(string(plain), `"proposal"`); n != 1 || !slices.ContainsFunc(decode[executionsOutput](t, plain).Executions, func(x executionJSON) bool {
		return x.Proposal && x.SafeTxHash == third.SafeTxHash
	}) {
		t.Errorf("executions of the third: got %s, want the third marked, and no other proposal key", plain)
	}
	// Without a service, -all fails.
	if stderr := n.Arbot().Fail(t, 1, "executions", "-all", "-request-file", path); !strings.Contains(string(stderr), "-all needs the Safe Transaction Service of chain 1") {
		t.Errorf("executions -all without a service: got %q, want an error about the service", stderr)
	}

	text := arbot.Run(t, "executions", "-all", "-request-file", path)
	for _, want := range []string{
		`(?m)^Missing nonces: none$`,
		`(?m)^\d+ +` + first.BlockTime.Format(time.RFC3339) + ` +` + first.TxHash.String() + ` +success +` + first.SafeTxHash.String() + ` +service +0 +` + target.String() + ` +0 +CALL +0x66697273 \(5 bytes\)$`,
		`(?m)^3 +` + misreported.SafeTxHash.String() + ` +` + misreported.TxHash.String() + ` +the transaction's fields don't hash`,
	} {
		if !regexp.MustCompile(want).Match(text) {
			t.Errorf("executions -all: output has no line matching %q:\n%s", want, text)
		}
	}
}

// TestCreationFromService checks the creation that a Safe Transaction Service
// names against an anvil node's receipts. Any transaction in the block where
// the account got its code passes the code check, so the transaction is only
// kept if its receipt has the account as the contract that it created, and
// otherwise the creation has its block alone.
func TestCreationFromService(t *testing.T) {
	t.Parallel()
	node := e2e.StartAnvil(t, ethrpc.Mainnet, "--prune-history=50")
	deployer := ethrpc.Address{0: 0xde, 19: 1}
	bystander := ethrpc.Address{0: 0xb5, 19: 1}
	for _, a := range []ethrpc.Address{deployer, bystander} {
		node.SetBalance(a, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	}
	// A contract creation, whose init code returns the code 0x6080, and another
	// transaction in the same block.
	node.SetAutomine(false)
	var created ethrpc.Hash
	node.RPC(&created, "eth_sendTransaction", map[string]any{"from": deployer, "data": ethrpc.Bytes{0x61, 0x60, 0x80, 0x60, 0x00, 0x52, 0x60, 0x02, 0x60, 0x1e, 0xf3}})
	other := node.Send(bystander, bystander, nil, nil)
	node.MinePending()
	node.SetAutomine(true)
	node.Mine(1)
	receipt, err := node.GetTransactionReceipt(t.Context(), created)
	if err != nil || receipt.ContractAddress == nil {
		t.Fatalf("receipt of the contract creation: got %+v, %v, want one with the contract", receipt, err)
	}
	account, block := *receipt.ContractAddress, receipt.BlockNumber

	for _, test := range []struct {
		name    string
		named   ethrpc.Hash
		want    *ethrpc.Hash
		warning string
	}{
		{"the creation", created, &created, ""},
		{"another transaction", other, nil, "neither creates the account nor logs a ProxyCreation event of it"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"transactionHash": %q}`, test.named)
			}))
			t.Cleanup(service.Close)
			var warnings []string
			f := &executions.Finder{
				Eth: node.Client, Service: txservice.NewClient(service.URL, ethrpc.Mainnet),
				Warn: func(err error) { warnings = append(warnings, err.Error()) },
			}
			got, err := f.CreationFromService(t.Context(), account, block+1)
			if err != nil || got == nil || got.Block != uint64(block) || !reflect.DeepEqual(got.TxHash, test.want) || got.Factory != nil {
				t.Errorf("CreationFromService: got %+v, %v, want block %d and transaction %v", got, err, block, test.want)
			}
			if test.warning == "" && len(warnings) != 0 || test.warning != "" && (len(warnings) != 1 || !strings.Contains(warnings[0], test.warning)) {
				t.Errorf("warnings: got %q, want %q", warnings, test.warning)
			}
		})
	}
}
