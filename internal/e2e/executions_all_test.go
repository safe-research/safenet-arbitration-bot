package e2e_test

import (
	"encoding/json"
	"math/big"
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
		Rejected      []executions.Rejected   `json:"rejected"`
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
			Block: uint64(receipt.BlockNumber), TxHash: receipt.TransactionHash, SafeTxHash: tx.Hash(),
			Success: true, Payment: new(big.Int), Source: source, Transaction: &tx,
		}
		return x, e2e.TxServiceExecution{Transaction: &tx, SafeTxHash: tx.Hash(), TxHash: receipt.TransactionHash, Block: x.Block}
	}
	first, listedFirst := exec("first", "service")
	exec("unlisted", "calldata")
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
	// The misreported execution is found in the block that the service names, and
	// the unlisted one, in a block that it doesn't name, isn't found.
	if want := []executionJSON{first, third, misreported}; !reflect.DeepEqual(canonical(t, got.Executions), canonical(t, want)) {
		g, _ := json.MarshalIndent(got.Executions, "", "  ")
		w, _ := json.MarshalIndent(want, "", "  ")
		t.Errorf("executions -all:\ngot  %s\nwant %s", g, w)
	}
	if len(h.Rejected) != 1 || h.Rejected[0].SafeTxHash != misreported.SafeTxHash {
		t.Errorf("executions -all: got rejected %+v, want the misreported transaction", h.Rejected)
	}
	missing := []executions.NonceRange{{From: 1, To: 1}}
	if h.Nonce == nil || h.Nonce.Int64() != 4 || h.Complete || !reflect.DeepEqual(h.MissingNonces, missing) {
		t.Errorf("executions -all: got nonce %v with missing nonces %v, want 4, incomplete, with %v missing", h.Nonce, h.MissingNonces, missing)
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
	if !reflect.DeepEqual(canonical(t, own.Executions), canonical(t, []executionJSON{first, ownThird, misreported})) {
		t.Errorf("executions -all of the third: got executions %+v, want the third marked", own.Executions)
	}
	ownText := arbot.Run(t, "executions", "-all", "-request-file", pathThird)
	if want := `(?m)^The execution in block \d+, transaction ` + third.TxHash.String() + `, is of the request's own Safe transaction`; !regexp.MustCompile(want).Match(ownText) {
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
		`(?m)^Missing nonces: 1$`,
		`(?m)^\d+ +` + first.TxHash.String() + ` +success +` + first.SafeTxHash.String() + ` +service +0 +` + target.String() + ` +0 +CALL +0x66697273 \(5 bytes\)$`,
		`(?m)^3 +` + misreported.SafeTxHash.String() + ` +` + misreported.TxHash.String() + ` +the transaction's fields don't hash`,
	} {
		if !regexp.MustCompile(want).Match(text) {
			t.Errorf("executions -all: output has no line matching %q:\n%s", want, text)
		}
	}
}
