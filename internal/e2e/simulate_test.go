package e2e_test

import (
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/e2e"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// simulateOutput is the JSON output of `arbot simulate`.
type simulateOutput struct {
	ChainID   uint64         `json:"chainId"`
	SafeBlock uint64         `json:"safeBlock"`
	Safe      ethrpc.Address `json:"safe"`
	Result    struct {
		Method       string       `json:"method"`
		Status       string       `json:"status"`
		ReturnData   ethrpc.Bytes `json:"returnData"`
		RevertReason string       `json:"revertReason"`
		Error        *struct {
			Signature string `json:"signature"`
			Args      []struct {
				Value any `json:"value"`
			} `json:"args"`
		} `json:"error"`
	} `json:"result"`
	Trace *struct {
		Available bool `json:"available"`
		Calls     []struct {
			Depth int             `json:"depth"`
			Type  string          `json:"type"`
			To    *ethrpc.Address `json:"to"`
			Error string          `json:"error"`
		} `json:"calls"`
	} `json:"trace"`
}

// TestSimulate runs `arbot simulate` against a real Safe 1.3.0 on anvil, on two
// transactions that add an owner to the Safe: a call with value, which reverts,
// since the function isn't payable, and a delegate call to the Safe itself,
// which reverts with GS031, since its sender isn't the Safe.
func TestSimulate(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	// The first transaction is a delegate call and the second a call, both to the
	// Safe on Ethereum Mainnet.
	delegate := n.Propose(nil)
	r := n.Propose(nil)
	tx := r.Transaction
	if tx.ChainID.Uint64() != ethrpc.Mainnet || tx.Operation != 0 || tx.To != tx.Safe || tx.Value.Sign() == 0 ||
		delegate.Transaction.Operation != safenet.OperationDelegateCall || delegate.Transaction.To != tx.Safe {
		t.Fatalf("Safe transactions %+v and %+v aren't a delegate call and a call with value to the Safe on Ethereum Mainnet", delegate.Transaction, tx)
	}
	mainnet, a := n.Mainnet, n.Artifacts
	owner := ethrpc.Address{0: 0x0e, 19: 1}
	ether := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	mainnet.SetBalance(owner, ether)
	mainnet.SetBalance(tx.Safe, ether)
	n.SetupSafe(mainnet, tx.Safe, a.SafeSingleton, owner)
	mainnet.MineUntil(n.Header(r.Block).Time())

	arbot := n.Arbot()
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	if err := os.WriteFile(path, arbot.Run(t, "info", "-json", r.ID.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	pathDelegate := filepath.Join(dir, "delegate.json")
	if err := os.WriteFile(pathDelegate, arbot.Run(t, "info", "-json", delegate.ID.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	got := decode[simulateOutput](t, arbot.Run(t, "simulate", "-json", "-request-file", path))
	if got.ChainID != ethrpc.Mainnet || got.Safe != tx.Safe || got.SafeBlock == 0 || got.Result.Method != "eth_call" ||
		got.Result.Status != "reverted" || got.Result.RevertReason == "" || len(got.Result.ReturnData) != 0 || got.Result.Error != nil ||
		got.Trace != nil {
		t.Errorf("simulate: got %+v, want a call that reverted without data, and no trace", got)
	}
	text := arbot.Run(t, "simulate", "-request-file", path)
	for _, want := range []string{`(?m)^Result +reverted$`, `(?m)^  Method +eth_call from the Safe$`} {
		if !regexp.MustCompile(want).Match(text) {
			t.Errorf("simulate: output has no line matching %q:\n%s", want, text)
		}
	}

	got = decode[simulateOutput](t, arbot.Run(t, "simulate", "-json", "-request-file", pathDelegate))
	if e := got.Result.Error; got.Result.Method != "simulateAndRevert" || got.Result.Status != "reverted" ||
		e == nil || e.Signature != "Error(string)" || len(e.Args) != 1 || e.Args[0].Value != "GS031" {
		t.Errorf("simulate of the delegate call: got %+v, want a revert with GS031", got.Result)
	}
}

// TestSimulateBatch runs `arbot simulate -trace` against a real Safe 1.3.0 on
// anvil, on two MultiSendCallOnly batches of calls that the Safe makes to
// itself: one that adds two owners, which succeeds, and one that adds the same
// owner twice, whose second call reverts only because the first one ran.
func TestSimulateBatch(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	mainnet, a := n.Mainnet, n.Artifacts
	var code ethrpc.Bytes
	if err := code.UnmarshalText([]byte(multiSendCallOnly130Code)); err != nil {
		t.Fatal(err)
	}
	mainnet.RPC(nil, "anvil_setCode", multiSendCallOnly130, code)

	addOwner := func(owner ethrpc.Address) []byte {
		return solabi.Call(solabi.Selector("addOwnerWithThreshold(address,uint256)"), owner, uint64(1))
	}
	batch := func(safe ethrpc.Address, owners ...ethrpc.Address) ethrpc.Bytes {
		var transactions []byte
		for _, owner := range owners {
			data := addOwner(owner)
			transactions = append(append(transactions, 0), safe[:]...)
			transactions = append(append(transactions, solabi.Encode(uint64(0), uint64(len(data)))...), data...)
		}
		return solabi.Call(solabi.Selector("multiSend(bytes)"), transactions)
	}
	first, second := ethrpc.Address{0: 0xbe, 19: 0xa1}, ethrpc.Address{0: 0xbe, 19: 0xa2}
	propose := func(owners ...ethrpc.Address) *e2e.Request {
		return n.ProposeTransaction(func(tx *safenet.SafeTransaction) {
			tx.ChainID, tx.To, tx.Value, tx.Operation = big.NewInt(ethrpc.Mainnet), multiSendCallOnly130, new(big.Int), safenet.OperationDelegateCall
			tx.Data, tx.GasPrice = batch(tx.Safe, owners...), new(big.Int)
		})
	}
	succeeds, reverts := propose(first, second), propose(first, first)

	owner := ethrpc.Address{0: 0x0e, 19: 1}
	ether := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	mainnet.SetBalance(owner, ether)
	n.SetupSafe(mainnet, succeeds.Transaction.Safe, a.SafeSingleton, owner)
	mainnet.MineUntil(n.Header(reverts.Block).Time())

	arbot := n.Arbot()
	dir := t.TempDir()
	run := func(r *e2e.Request) simulateOutput {
		t.Helper()
		path := filepath.Join(dir, r.ID.String()+".json")
		if err := os.WriteFile(path, arbot.Run(t, "info", "-json", r.ID.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		if text := arbot.Run(t, "simulate", "-trace", "-request-file", path); !regexp.MustCompile(`(?m)^Trace \(debug_traceCall, callTracer\)$`).Match(text) {
			t.Errorf("simulate -trace: output has no trace:\n%s", text)
		}
		return decode[simulateOutput](t, arbot.Run(t, "simulate", "-trace", "-json", "-request-file", path))
	}

	got := run(succeeds)
	if got.Result.Method != "simulateAndRevert" || got.Result.Status != "success" || len(got.Result.ReturnData) != 0 {
		t.Errorf("simulate of the batch that succeeds: got %+v", got.Result)
	}
	// The trace's top frame is the call of simulateAndRevert, which reverts by
	// design, then the Safe's delegate calls to its singleton and the MultiSend,
	// which make the two calls to the Safe, which delegate calls to its singleton
	// in turn.
	if tr := got.Trace; tr == nil || !tr.Available || len(tr.Calls) < 5 || tr.Calls[0].Error == "" || tr.Calls[2].Type != "DELEGATECALL" ||
		tr.Calls[2].To == nil || *tr.Calls[2].To != multiSendCallOnly130 {
		t.Errorf("simulate -trace of the batch that succeeds: got trace %+v", tr)
	}

	// MultiSendCallOnly 1.3.0 reverts with no data when a call of its batch does,
	// and the trace shows which: of the batch's calls to the Safe, the frames at
	// depth 3, the second one reverts, since the owner is already one.
	got = run(reverts)
	if got.Result.Status != "reverted" || len(got.Result.ReturnData) != 0 || got.Result.Error != nil {
		t.Errorf("simulate of the batch that reverts: got %+v, want a revert without data", got.Result)
	}
	var batched []string
	if got.Trace != nil {
		for _, c := range got.Trace.Calls {
			if c.Depth == 3 {
				batched = append(batched, c.Error)
			}
		}
	}
	if len(batched) != 2 || batched[0] != "" || batched[1] == "" {
		t.Errorf("simulate -trace of the batch that reverts: got the errors %q of the batched calls, want only the second to fail", batched)
	}
}
