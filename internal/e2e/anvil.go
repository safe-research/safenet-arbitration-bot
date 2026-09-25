package e2e

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// StateHistory is the number of recent blocks whose state anvil keeps, for
// calls at past blocks. Calls at older blocks fail.
const StateHistory = 256

// Anvil is a local anvil node with the chain ID of Gnosis Chain, which
// impersonates any account that sends it a transaction. It embeds a client for
// the node, and adds anvil's own methods.
type Anvil struct {
	*ethrpc.Client
	tb  testing.TB
	URL string
}

// StartAnvil starts an anvil node, which runs until the test ends. It skips the
// test if anvil isn't installed, or in short mode.
func StartAnvil(tb testing.TB) *Anvil {
	tb.Helper()
	if testing.Short() {
		tb.Skip("skipping end-to-end test in short mode")
	}
	path, err := exec.LookPath("anvil")
	if err != nil {
		tb.Skip("skipping end-to-end test: anvil is not installed")
	}

	// Keeping the state of every block makes mining an arbitration timeout's worth
	// of blocks take minutes, so anvil only keeps the state of the latest blocks.
	// The test's context is canceled before its cleanup functions run, which kills
	// anvil.
	cmd := exec.CommandContext(tb.Context(), path,
		"--chain-id", fmt.Sprint(ethrpc.Gnosis), "--port", "0", "--auto-impersonate",
		"--prune-history", fmt.Sprint(StateHistory))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		tb.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		tb.Fatalf("starting anvil: %v", err)
	}
	tb.Cleanup(func() { cmd.Wait() })

	lines := bufio.NewScanner(stdout)
	for lines.Scan() {
		if address, ok := strings.CutPrefix(lines.Text(), "Listening on "); ok {
			go io.Copy(io.Discard, stdout)
			url := "http://" + address
			eth, err := ethrpc.NewClient(tb.Context(), ethrpc.Gnosis, url)
			if err != nil {
				tb.Fatal(err)
			}
			return &Anvil{Client: eth, tb: tb, URL: url}
		}
	}
	tb.Fatalf("anvil exited without listening: %v", lines.Err())
	return nil
}

// Install sets the code and storage of the artifacts' accounts.
func (a *Anvil) Install(artifacts *Artifacts) {
	a.tb.Helper()
	for address, account := range artifacts.Accounts {
		a.rpc(nil, "anvil_setCode", address, account.Code)
		for slot, value := range account.Storage {
			a.rpc(nil, "anvil_setStorageAt", address, slot, value)
		}
	}
}

// rpc calls one of anvil's methods with params, and decodes its result into
// result unless it is nil. It fails the test on error.
func (a *Anvil) rpc(result any, method string, params ...any) {
	a.tb.Helper()
	if err := a.RawRequest(a.tb.Context(), result, method, params...); err != nil {
		a.tb.Fatal(err)
	}
}

// blockNumber returns the number of the latest block.
func (a *Anvil) blockNumber() ethrpc.BlockNumber {
	a.tb.Helper()
	n, err := a.BlockNumber(a.tb.Context())
	if err != nil {
		a.tb.Fatal(err)
	}
	return n
}

// Mine mines n empty blocks.
func (a *Anvil) Mine(n uint64) {
	a.tb.Helper()
	a.rpc(nil, "anvil_mine", ethrpc.Quantity(n))
}

// MineTo mines empty blocks until block is the latest block.
func (a *Anvil) MineTo(block uint64) {
	a.tb.Helper()
	if latest := uint64(a.blockNumber()); latest < block {
		a.Mine(block - latest)
	}
}

// SetAutomine sets whether anvil mines each transaction in its own block as it
// is sent. Otherwise, it mines them when MinePending is called.
func (a *Anvil) SetAutomine(automine bool) {
	a.tb.Helper()
	a.rpc(nil, "evm_setAutomine", automine)
}

// MinePending mines a block with the pending transactions.
func (a *Anvil) MinePending() {
	a.tb.Helper()
	a.rpc(nil, "evm_mine")
}

// SetBalance sets the ether balance of account.
func (a *Anvil) SetBalance(account ethrpc.Address, wei *big.Int) {
	a.tb.Helper()
	a.rpc(nil, "anvil_setBalance", account, "0x"+wei.Text(16))
}

// Receipt is a transaction receipt.
type Receipt struct {
	TransactionHash ethrpc.Hash        `json:"transactionHash"`
	BlockNumber     ethrpc.BlockNumber `json:"blockNumber"`
	Status          ethrpc.Quantity    `json:"status"`
	Logs            []ethrpc.Log       `json:"logs"`
}

// Log returns the receipt's log of event emitted by address, if any.
func (r *Receipt) Log(address ethrpc.Address, event ethrpc.Hash) (ethrpc.Log, bool) {
	i := slices.IndexFunc(r.Logs, func(log ethrpc.Log) bool {
		return log.Address == address && len(log.Topics) > 0 && log.Topics[0] == event
	})
	if i < 0 {
		return ethrpc.Log{}, false
	}
	return r.Logs[i], true
}

// Send sends a transaction from from, without waiting for it to be mined, and
// returns its hash.
func (a *Anvil) Send(from, to ethrpc.Address, data ethrpc.Bytes, value *big.Int) ethrpc.Hash {
	a.tb.Helper()
	tx := map[string]any{"from": from, "to": to, "data": data}
	if value != nil {
		tx["value"] = "0x" + value.Text(16)
	}
	var hash ethrpc.Hash
	a.rpc(&hash, "eth_sendTransaction", tx)
	return hash
}

// Receipt returns the receipt of the transaction hash, waiting for anvil to
// mine it, and fails the test if it reverted.
func (a *Anvil) Receipt(hash ethrpc.Hash) *Receipt {
	a.tb.Helper()
	var r *Receipt
	for range 100 {
		if a.rpc(&r, "eth_getTransactionReceipt", hash); r != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r == nil {
		a.tb.Fatalf("transaction %s is not mined", hash)
	}
	if r.Status != 1 {
		a.tb.Fatalf("transaction %s reverted: %s", hash, a.revert(hash, r.BlockNumber))
	}
	return r
}

// revert returns why the transaction hash, which reverted in block, reverted,
// by replaying it on the state before block.
func (a *Anvil) revert(hash ethrpc.Hash, block ethrpc.BlockNumber) string {
	a.tb.Helper()
	var tx map[string]any
	a.rpc(&tx, "eth_getTransactionByHash", hash)
	call := map[string]any{"from": tx["from"], "to": tx["to"], "data": tx["input"], "value": tx["value"]}
	if err := a.RawRequest(a.tb.Context(), nil, "eth_call", call, block-1); err != nil {
		if rpcErr, ok := errors.AsType[*ethrpc.Error](err); ok {
			return fmt.Sprintf("%v (data %s)", err, rpcErr.Data)
		}
		return err.Error()
	}
	return "replaying it succeeds"
}

// Transact sends a transaction, waits for it to be mined, and returns its
// receipt.
func (a *Anvil) Transact(from, to ethrpc.Address, data ethrpc.Bytes) *Receipt {
	a.tb.Helper()
	return a.Receipt(a.Send(from, to, data, nil))
}
