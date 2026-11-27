package e2e_test

import (
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/e2e"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/status"
)

// TestStatus runs `arbot status` against real Safe 1.3.0 contracts on anvil,
// for requests whose Safe transactions were executed on Ethereum Mainnet after
// their proposals on Gnosis Chain, one of them during a voting that was then
// frozen and ruled on, and one by a delegate call that sets the Safe's nonce
// back, or before the proposal, in the Safe block, or not at all.
func TestStatus(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	mainnet, a := n.Mainnet, n.Artifacts
	// The Mainnet node is mined up to the Gnosis Chain node's time once, here, and
	// Gnosis Chain is then a few minutes ahead, so that the Mainnet blocks of the
	// test can be placed at any time around the proposals.
	arbot := n.Arbot()
	n.Mine(40)
	owner := ethrpc.Address{0: 0x0e, 19: 1}
	target := ethrpc.Address{0: 0xa1, 19: 1}
	// A contract that sets the storage slot of the nonce of a Safe that delegate
	// calls it to 1, with the code SSTORE(5, 1).
	resetter := ethrpc.Address{0: 0x5e, 19: 1}
	mainnet.RPC(nil, "anvil_setCode", resetter, "0x600160055500")
	mainnet.SetBalance(owner, new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil))

	// safeTx sets the fields of a Safe transaction of the Safe on Mainnet that its
	// owner can execute, a call to the target with data.
	safeTx := func(nonce int64, data string) func(tx *safenet.SafeTransaction) {
		return func(tx *safenet.SafeTransaction) {
			tx.ChainID, tx.To, tx.Value, tx.Data, tx.Operation = big.NewInt(ethrpc.Mainnet), target, new(big.Int), []byte(data), safenet.OperationCall
			tx.SafeTxGas, tx.BaseGas, tx.GasPrice = new(big.Int), new(big.Int), new(big.Int)
			tx.GasToken, tx.RefundReceiver, tx.Nonce = ethrpc.Address{}, ethrpc.Address{}, big.NewInt(nonce)
		}
	}
	// execAt has the owner execute tx in a Mainnet block at time t.
	execAt := func(tx *safenet.SafeTransaction, t time.Time) *e2e.Receipt {
		mainnet.RPC(nil, "evm_setNextBlockTimestamp", ethrpc.Quantity(t.Unix()))
		return mainnet.Transact(owner, tx.Safe, n.ExecCalldata(tx, owner))
	}
	dir := t.TempDir()
	requestFile := func(r *e2e.Request) string {
		path := filepath.Join(dir, r.ID.String()+".json")
		if err := os.WriteFile(path, arbot.Run(t, "info", "-json", r.ID.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	statusOf := func(arbot *e2e.Arbot, path string) status.Report {
		t.Helper()
		return decode[status.Report](t, arbot.Run(t, "status", "-json", "-request-file", path))
	}
	// checkExecuted checks that the status is the execution of the request's
	// transaction in receipt, at time at.
	checkExecuted := func(t *testing.T, got status.Report, source string, receipt *e2e.Receipt, at time.Time) {
		t.Helper()
		if x := got.Execution; x.Status != status.Executed || x.Source != source || x.Block == nil || *x.Block != uint64(receipt.BlockNumber) ||
			*x.TxHash != receipt.TransactionHash || !x.BlockTime.Equal(at) || !*x.Success || *x.SafeTxHash != got.SafeTxHash {
			t.Errorf("status: got execution %+v, want the one in block %d, from the %s", x, receipt.BlockNumber, source)
		}
		if got.Notes[0] != status.NotEvidence {
			t.Errorf("status: got notes %q, want the first to say that nothing on chain after the Safe block is onchain data", got.Notes)
		}
	}

	// The first request's transaction is executed a second after its proposal, and
	// the sentinels then split on it, and the arbitrator rules it secure.
	first := n.ProposeTransaction(safeTx(0, "first"))
	proposed := n.Header(first.Block).Time()
	n.SetupSafe(mainnet, first.Transaction.Safe, a.SafeSingleton, owner)
	executed := execAt(first.Transaction, proposed.Add(time.Second))
	n.Vote(first.Approve(a.Sentinels[0]), first.Deny(a.Sentinels[1], "R-4.1"))
	first.Resolve(true, "Secure.")
	firstPath := requestFile(first)

	t.Run("executed after the proposal", func(t *testing.T) {
		got := statusOf(arbot, firstPath)
		if got.Request != first.ID || got.SafeTxHash != first.SafeTxHash || got.ChainID != ethrpc.Mainnet || got.Safe != first.Transaction.Safe {
			t.Errorf("status: got request %s, Safe transaction %s of %s on chain %d, want the first", got.Request, got.SafeTxHash, got.Safe, got.ChainID)
		}
		if n := got.Nonce; n.Transaction.Int64() != 0 || n.SafeAtSafeBlock.Int64() != 0 || n.SafeAtLatest.Int64() != 1 || n.Stale {
			t.Errorf("status: got nonce %+v, want 0, unused at the Safe block and used at the latest", n)
		}
		checkExecuted(t, got, status.SourceLogs, executed, proposed.Add(time.Second))
		// The scan for the transaction itself starts 100,000 blocks before the Safe
		// block, so at block 0.
		if x := got.Execution; x.Service != nil || x.Scanned == nil || x.Scanned.FromBlock != 0 || x.Scanned.ToBlock != got.Nonce.LatestBlock {
			t.Errorf("status: got service %+v and blocks %+v scanned, want none and the blocks from 0 to the latest", x.Service, x.Scanned)
		}
	})

	t.Run("timing", func(t *testing.T) {
		got := statusOf(arbot, firstPath)
		timing := got.Timing
		if got.State != safenet.StateResolvedApproved || timing == nil || timing.Value != status.DuringVoting {
			t.Fatalf("status: got state %s and timing %+v, want RESOLVED_APPROVED, executed during the voting", got.State, timing)
		}
		// at checks that a point is the Gnosis Chain block, at its time.
		at := func(name string, got status.Point, block uint64) {
			t.Helper()
			if got.Block != block || !got.Time.Equal(n.Header(block).Time()) {
				t.Errorf("status: got %s %+v, want Gnosis Chain block %d at %s", name, got, block, n.Header(block).Time())
			}
		}
		at("proposal", timing.Proposal, first.Block)
		at("now", timing.Now, timing.Now.Block)
		if timing.Frozen == nil || timing.Resolved == nil || timing.Resolved.Outcome != safenet.OutcomeSecure {
			t.Fatalf("status: got freeze %+v and settlement %+v, want both, secure", timing.Frozen, timing.Resolved)
		}
		at("freeze", *timing.Frozen, first.FrozenBlock)
		at("settlement", timing.Resolved.Point, uint64(first.Record.BlockNumber))
		for _, d := range []struct {
			name  string
			got   status.Deadline
			block uint64
		}{{"commit deadline", timing.CommitDeadline, first.CommitDeadline}, {"reveal deadline", timing.RevealDeadline, first.RevealDeadline}} {
			if reached := d.block <= timing.Now.Block; d.got.Block != d.block || d.got.Reached != reached || reached != (d.got.Time != nil) ||
				reached && !d.got.Time.Equal(n.Header(d.block).Time()) {
				t.Errorf("status: got %s %+v, want block %d, reached by block %d: %t", d.name, d.got, d.block, timing.Now.Block, reached)
			}
		}
		if x := timing.Execution; x.Block != uint64(executed.BlockNumber) || !x.Time.Equal(proposed.Add(time.Second)) {
			t.Errorf("status: got execution %+v, want block %d a second after the proposal", x, executed.BlockNumber)
		}
	})

	t.Run("executed after the proposal, from the service", func(t *testing.T) {
		service := e2e.NewTxService(t)
		service.ListExecutions(e2e.TxServiceExecution{
			Transaction: first.Transaction, SafeTxHash: first.SafeTxHash, TxHash: executed.TransactionHash, Block: uint64(executed.BlockNumber),
		})
		got := statusOf(arbot.WithTxService(t, ethrpc.Mainnet, service.URL), firstPath)
		checkExecuted(t, got, status.SourceService, executed, proposed.Add(time.Second))
		if x := got.Execution; x.Service == nil || x.Service.URL != service.URL || x.Scanned != nil {
			t.Errorf("status: got service %+v and blocks %+v scanned, want the service, without a scan", x.Service, x.Scanned)
		}
	})

	t.Run("text", func(t *testing.T) {
		text := arbot.Run(t, "status", "-request-file", firstPath)
		for _, want := range []string{
			`(?m)^Status of the Safe transaction of request ` + first.ID.String() + `, RESOLVED_APPROVED as of Gnosis Chain block \d+$`,
			`(?m)^` + regexp.QuoteMeta(status.NotEvidence) + `$`,
			`(?m)^  Status +executed$`,
			`(?m)^  Executed +block \d+ \(` + proposed.Add(time.Second).Format(time.RFC3339) + `\), transaction ` + executed.TransactionHash.String() + `, success$`,
			`(?m)^  Service +none for chain 1$`,
			`(?m)^Timing: during-voting$`,
			`(?m)^  Frozen +Gnosis Chain block ` + strconv.FormatUint(first.FrozenBlock, 10) + ` +\S+Z$`,
			`(?m)^  Resolved +Gnosis Chain block ` + strconv.FormatUint(uint64(first.Record.BlockNumber), 10) + ` +\S+Z \(secure\)$`,
		} {
			if !regexp.MustCompile(want).Match(text) {
				t.Errorf("status: output has no line matching %q:\n%s", want, text)
			}
		}
	})

	// The second request's transaction is a delegate call that sets the Safe's
	// nonce back to its own, so the nonce doesn't show its execution.
	second := n.ProposeTransaction(func(tx *safenet.SafeTransaction) {
		safeTx(1, "")(tx)
		tx.To, tx.Operation = resetter, safenet.OperationDelegateCall
	})
	setBack := execAt(second.Transaction, n.Header(second.Block).Time().Add(time.Second))
	secondPath := requestFile(second)

	t.Run("executed with the nonce set back", func(t *testing.T) {
		got := statusOf(arbot, secondPath)
		if n := got.Nonce; n.Transaction.Int64() != 1 || n.SafeAtSafeBlock.Int64() != 1 || n.SafeAtLatest.Int64() != 1 || n.Stale {
			t.Errorf("status: got nonce %+v, want 1, as the Safe's at the Safe block and at the latest", n)
		}
		checkExecuted(t, got, status.SourceLogs, setBack, n.Header(second.Block).Time().Add(time.Second))
	})

	// The third request's transaction is executed two seconds before its proposal,
	// in the last Mainnet block before it, which is its Safe block.
	early := &safenet.SafeTransaction{Safe: first.Transaction.Safe}
	safeTx(1, "before")(early)
	latest, err := n.BlockNumber(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	next := n.Header(uint64(latest)).Time().Add(5 * time.Second)
	before := execAt(early, next.Add(-2*time.Second))
	n.RPC(nil, "evm_setNextBlockTimestamp", ethrpc.Quantity(next.Unix()))
	third := n.ProposeTransaction(safeTx(1, "before"))
	if !n.Header(third.Block).Time().Equal(next) || third.SafeTxHash != early.Hash() {
		t.Fatalf("the third proposal is at %s for %s, want %s for %s", n.Header(third.Block).Time(), third.SafeTxHash, next, early.Hash())
	}
	mainnet.Mine(1)

	t.Run("executed before the proposal", func(t *testing.T) {
		got := statusOf(arbot, requestFile(third))
		if got.SafeBlock != uint64(before.BlockNumber) {
			t.Errorf("status: got Safe block %d, want the block of the execution, %d", got.SafeBlock, before.BlockNumber)
		}
		if n := got.Nonce; n.Transaction.Int64() != 1 || n.SafeAtSafeBlock.Int64() != 2 || !n.Stale {
			t.Errorf("status: got nonce %+v, want 1, below the Safe's 2 at the Safe block, stale", n)
		}
		checkExecuted(t, got, status.SourceLogs, before, next.Add(-2*time.Second))
		// The stale nonce is scanned for up to the Safe block, and the transaction
		// itself after it too, up to the latest block.
		if x := got.Execution.Scanned; x == nil || x.ToBlock != got.Nonce.LatestBlock {
			t.Errorf("status: got blocks %+v scanned, want those up to the latest block %d", x, got.Nonce.LatestBlock)
		}
		if timing := got.Timing; got.State != safenet.StatePending || timing == nil || timing.Value != status.BeforeProposal ||
			timing.Execution.Block != got.SafeBlock || timing.Frozen != nil || timing.Resolved != nil {
			t.Errorf("status: got state %s and timing %+v, want PENDING, executed before the proposal, in the Safe block", got.State, timing)
		}
	})

	// The fourth request's transaction has a nonce that the Safe hasn't used.
	unused := n.ProposeTransaction(safeTx(5, "unused"))
	mainnet.MineUntil(n.Header(unused.Block).Time())

	t.Run("not executed", func(t *testing.T) {
		got := statusOf(arbot, requestFile(unused))
		if x := got.Execution; x.Status != status.NotExecuted || x.Source != status.SourceNonce || x.Block != nil ||
			x.Scanned == nil || x.Scanned.ToBlock != got.Nonce.LatestBlock {
			t.Errorf("status: got execution %+v, want not executed, from the nonce, after a scan up to the latest block", x)
		}
		if n := got.Nonce; n.Stale || n.SafeAtLatest.Int64() != 2 {
			t.Errorf("status: got nonce %+v, want 5, unused, with the Safe's at 2", n)
		}
	})
}
