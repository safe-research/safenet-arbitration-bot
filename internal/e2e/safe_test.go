package e2e_test

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/e2e"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// safeOutput is the JSON output of `arbot safe`.
type safeOutput struct {
	Safe            ethrpc.Address   `json:"safe"`
	ChainID         uint64           `json:"chainId"`
	SafeBlock       uint64           `json:"safeBlock"`
	Block           uint64           `json:"block,omitempty"`
	Singleton       ethrpc.Address   `json:"singleton"`
	Owners          []ethrpc.Address `json:"owners"`
	OwnerCount      *big.Int         `json:"ownerCount"`
	Threshold       *big.Int         `json:"threshold"`
	Nonce           *big.Int         `json:"nonce"`
	Modules         []ethrpc.Address `json:"modules"`
	FallbackHandler ethrpc.Address   `json:"fallbackHandler"`
	Guard           ethrpc.Address   `json:"guard"`
	ModuleGuard     ethrpc.Address   `json:"moduleGuard"`
}

// TestSafe runs `arbot safe` on a Safe 1.3.0 that the singleton's setup set up
// on the Mainnet node, with a guard set before the proposal, and a module guard
// set after it, which the evidence must not include, and on a second Safe
// before and after a settings change.
func TestSafe(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	r := n.Propose(nil)
	if r.Transaction.ChainID.Uint64() != ethrpc.Mainnet {
		t.Fatalf("Safe transaction is for chain %s, want Ethereum Mainnet", r.Transaction.ChainID)
	}
	mainnet, safe := n.Mainnet, r.Transaction.Safe
	owner, other := ethrpc.Address{0: 0x0e, 19: 1}, ethrpc.Address{0: 0x0e, 19: 2}
	guard, moduleGuard := ethrpc.Address{0: 0x6a, 19: 1}, ethrpc.Address{0: 0x36, 19: 1}
	// set sets the setting at the hash of name to address, as the singleton would.
	set := func(name string, address ethrpc.Address) {
		var value ethrpc.Hash
		copy(value[12:], address[:])
		mainnet.RPC(nil, "anvil_setStorageAt", safe, ethrpc.Hash(keccak256.Hash([]byte(name))), value)
	}

	// The Mainnet node's blocks are earlier than the proposal until it mines up to
	// it.
	mainnet.SetBalance(owner, new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil))
	n.SetupSafe(mainnet, safe, singleton130, owner, other)
	set("guard_manager.guard.address", guard)

	// A second Safe, which raises its threshold to two in a Safe transaction before
	// the proposal. The Mainnet node only keeps the states of its last 128 blocks,
	// which -block reads, so the node mines up to just before the proposal first.
	mainnet.MineUntil(n.Header(r.Block).Time().Add(-3 * time.Minute))
	second, third := ethrpc.Address{0: 0x5a, 19: 0xf3}, ethrpc.Address{0: 0x0e, 19: 3}
	n.SetupSafe(mainnet, second, singleton130, owner, third)
	changeThreshold := safenet.SafeTransaction{
		To: second, Value: new(big.Int), Data: solabi.Call(solabi.Selector("changeThreshold(uint256)"), uint64(2)),
		SafeTxGas: new(big.Int), BaseGas: new(big.Int), GasPrice: new(big.Int),
	}
	changed := mainnet.Transact(owner, second, n.ExecCalldata(&changeThreshold, owner))
	beforeChange := uint64(changed.BlockNumber) - 1
	mainnet.MineUntil(n.Header(r.Block).Time())
	set("module_manager.module_guard.address", moduleGuard)
	mainnet.MineUntil(n.Header(r.Block).Time().Add(time.Minute))

	arbot := n.Arbot()
	info := arbot.Run(t, "info", "-json", r.ID.String())
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, info, 0o644); err != nil {
		t.Fatal(err)
	}
	safeBlock := decode[requestInfo](t, info).Proposal.SafeBlock

	got := decode[safeOutput](t, arbot.Run(t, "safe", "-json", "-request-file", path))
	want := safeOutput{
		Safe: safe, ChainID: ethrpc.Mainnet, SafeBlock: safeBlock, Singleton: singleton130,
		Owners: []ethrpc.Address{owner, other}, OwnerCount: big.NewInt(2), Threshold: big.NewInt(1), Nonce: new(big.Int),
		Modules: []ethrpc.Address{}, Guard: guard,
	}
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("safe:\ngot  %s\nwant %s", g, w)
	}

	t.Run("text", func(t *testing.T) {
		text := arbot.Run(t, "safe", "-request-file", path)
		for _, line := range []string{
			`Safe ` + safe.String() + ` on chain 1, as of block \d+`,
			`  Singleton         ` + singleton130.String(),
			`  Threshold         1 of 2 owners`,
			`  Guard             ` + guard.String(),
			`  ` + other.String(),
			`  none`,
		} {
			if !regexp.MustCompile(`(?m)^` + line + `$`).Match(text) {
				t.Errorf("safe: output has no line matching %q:\n%s", line, text)
			}
		}
	})

	t.Run("missing request file", func(t *testing.T) {
		arbot.Fail(t, 2, "safe")
	})

	t.Run("address", func(t *testing.T) {
		got := decode[safeOutput](t, arbot.Run(t, "safe", "-json", "-request-file", path, "-address", second.String()))
		if got.Safe != second || got.SafeBlock != safeBlock || got.Block != 0 || got.Threshold.Int64() != 2 || got.Nonce.Int64() != 1 {
			t.Errorf("safe -address: got %+v, want the second Safe with threshold 2 and nonce 1", got)
		}
		got = decode[safeOutput](t, arbot.Run(t, "safe", "-json", "-request-file", path, "-address", second.String(), "-block", fmt.Sprint(beforeChange)))
		if got.Safe != second || got.Block != beforeChange || got.Threshold.Int64() != 1 || got.Nonce.Int64() != 0 {
			t.Errorf("safe -address -block: got %+v, want the second Safe with threshold 1 and nonce 0", got)
		}
		// An owner has no code, so it isn't a Safe.
		if stderr := arbot.Fail(t, 1, "safe", "-json", "-request-file", path, "-address", owner.String()); !strings.Contains(string(stderr), "it has no code") {
			t.Errorf("safe -address of an owner: got %q, want it refused, since it has no code", stderr)
		}
		if stderr := arbot.Fail(t, 2, "safe", "-request-file", path, "-block", fmt.Sprint(safeBlock+1)); !strings.Contains(string(stderr), "is after the request's Safe block") {
			t.Errorf("safe -block after the Safe block: got %q, want a usage error", stderr)
		}
	})
}
