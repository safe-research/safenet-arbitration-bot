// Package status reports what became of the Safe transaction of a Safenet
// request: whether its nonce was below the Safe's when it was proposed, and
// whether the Safe executed it, or another Safe transaction with its nonce,
// confirmed onchain, and when, against the request's proposal, voting, and
// arbitration.
//
// Nothing on chain after the request's Safe block is onchain data (Charter §
// 2.8), so these are facts about the proposal, and the package never classifies
// a request by them.
package status

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/executions"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

// NotEvidence is the note that every report starts with.
const NotEvidence = "Nothing on chain after the Safe block is onchain data (§ 2.8), including an execution after it and its effects."

// The statuses of an execution.
const (
	// Executed means that the Safe executed the request's Safe transaction.
	Executed = "executed"
	// NotExecuted means that the Safe's nonce isn't above the transaction's, and
	// the Safe logged no execution of the transaction in the blocks scanned.
	NotExecuted = "not-executed"
	// OtherTransaction means that the Safe executed another Safe transaction with
	// the transaction's nonce.
	OtherTransaction = "other-transaction"
	// Unknown means that none of the sources settles the status, for the reason
	// given.
	Unknown = "unknown"
)

// The sources of an execution's status.
const (
	// SourceService is an execution that the Safe Transaction Service names, and
	// whose receipt has the Safe's log of it.
	SourceService = "service"
	// SourceLogs is an execution found in a scan of the Safe's execution logs.
	SourceLogs = "logs"
	// SourceNonce is the Safe's nonce, with a scan of its execution logs that has
	// no execution of the transaction.
	SourceNonce = "nonce"
)

// The values of a Timing.
const (
	BeforeProposal  = "before-proposal"
	DuringVoting    = "during-voting"
	AfterVoting     = "after-voting"
	AfterFreeze     = "after-freeze"
	AfterResolution = "after-resolution"
)

// Report is the status of a request's Safe transaction.
type Report struct {
	Request ethrpc.Hash `json:"request"`
	// State is the request's state as of the latest Gnosis Chain block, StateBlock.
	State      safenet.State  `json:"state"`
	StateBlock uint64         `json:"stateBlock"`
	Safe       ethrpc.Address `json:"safe"`
	ChainID    uint64         `json:"chainId"`
	SafeBlock  uint64         `json:"safeBlock"`
	SafeTxHash ethrpc.Hash    `json:"safeTxHash"`
	Nonce      Nonce          `json:"nonce"`
	Execution  Execution      `json:"execution"`
	// Timing places the execution, of the transaction or of the other one, in the
	// request's timeline, or is nil if there is none.
	Timing *Timing `json:"timing"`
	// Notes are facts and caveats in plain words, the first being NotEvidence.
	Notes []string `json:"notes"`
}

// Nonce is the Safe transaction's nonce, against the Safe's.
type Nonce struct {
	Transaction *big.Int `json:"transaction"`
	// SafeAtSafeBlock and SafeAtLatest are the Safe's nonce at the end of the Safe
	// block and of the latest block of its chain, LatestBlock.
	SafeAtSafeBlock *big.Int `json:"safeAtSafeBlock"`
	SafeAtLatest    *big.Int `json:"safeAtLatest"`
	LatestBlock     uint64   `json:"latestBlock"`
	// Stale is whether the transaction's nonce is below the Safe's at the Safe
	// block, so that the Safe couldn't execute it on that state.
	Stale bool `json:"stale"`
}

// Execution is whether the Safe executed the Safe transaction, or another one
// with its nonce.
type Execution struct {
	// Status is Executed, NotExecuted, OtherTransaction, or Unknown.
	Status string `json:"status"`
	// TxHash, Block, BlockTime, Success, and SafeTxHash are the onchain
	// transaction, its block and the block's time, whether the Safe transaction's
	// call succeeded, and the Safe transaction hash, of the execution of this
	// transaction or of the other one, or nil if there is none.
	TxHash     *ethrpc.Hash `json:"txHash"`
	Block      *uint64      `json:"block"`
	BlockTime  *time.Time   `json:"blockTime"`
	Success    *bool        `json:"success"`
	SafeTxHash *ethrpc.Hash `json:"safeTxHash"`
	// Source is SourceService, SourceLogs, or SourceNonce, or empty for Unknown.
	Source string `json:"source"`
	// Reason says how the status was found, or why it is unknown.
	Reason string `json:"reason"`
	// Service is the Safe Transaction Service that was asked, an offchain source (§
	// 3.4), or nil if the chain has none.
	Service *Service `json:"service"`
	// Scanned is the range of blocks of the Safe's chain whose execution logs were
	// scanned, or nil if the service settled the status.
	Scanned *executions.BlockRange `json:"scanned"`
}

// Service is a Safe Transaction Service, and when it was observed.
type Service struct {
	URL        string    `json:"url"`
	ObservedAt time.Time `json:"observedAt"`
}

// Timing places an execution in a request's timeline, by the timestamps of the
// blocks: of the Safe's chain for the execution, and of Gnosis Chain for the
// rest.
type Timing struct {
	// Value is BeforeProposal, DuringVoting, AfterVoting, AfterFreeze, or
	// AfterResolution.
	Value string `json:"value"`
	// Uncertain is whether an event after Now can still come before the execution
	// and change Value: a reveal deadline that Now hasn't reached, or a freeze or a
	// settlement of a request that was pending or frozen then. For a Safe on Gnosis
	// Chain, the execution is placed by block number, also against such a deadline,
	// so only a freeze or a settlement in a block after Now, and after the commit
	// deadline, which a freeze can't come before, and before the execution's can.
	// Value is then the earliest that the timing can be.
	Uncertain bool  `json:"uncertain"`
	Execution Point `json:"execution"`
	Proposal  Point `json:"proposal"`
	// CommitDeadline and RevealDeadline are the last blocks for committing and
	// revealing votes.
	CommitDeadline Deadline `json:"commitDeadline"`
	RevealDeadline Deadline `json:"revealDeadline"`
	// Frozen is the block that the request was frozen in, or nil if it wasn't.
	Frozen *Point `json:"frozen"`
	// Resolved is the block that settled the arbitration, with its outcome, or nil
	// if it isn't settled.
	Resolved *Resolution `json:"resolved"`
	// Now is the latest Gnosis Chain block, which the request was read at.
	Now Point `json:"now"`
}

// Point is a block and its timestamp.
type Point struct {
	Block uint64    `json:"block"`
	Time  time.Time `json:"time"`
}

// Deadline is a deadline block, which may be after the latest block.
type Deadline struct {
	Block uint64 `json:"block"`
	// Reached is whether the block is at or before the latest block, and Time is
	// its timestamp, or nil if it isn't.
	Reached bool       `json:"reached"`
	Time    *time.Time `json:"time"`
}

// Resolution is the block that settled an arbitration, with its outcome.
type Resolution struct {
	Point
	Outcome safenet.Outcome `json:"outcome"`
}

// Checker reads the status of a request's Safe transaction.
type Checker struct {
	// Gnosis is a client for Gnosis Chain, and Safe one for the Safe's chain, which
	// may be the same chain.
	Gnosis *ethrpc.Client
	Safe   *ethrpc.Client
	// Service is a client for the Safe Transaction Service of the Safe's chain, or
	// nil if it has none.
	Service *txservice.Client
	// Blocks bounds the scan of the Safe's execution logs.
	Blocks uint64
	// Warn is called with the errors of sources that failed for a reason other than
	// not having what was asked for, such as a service that refuses the request. It
	// may be nil.
	Warn func(error)
}

// Check returns the status of the Safe transaction of r, with its state, terms,
// and arbitration as of the latest Gnosis Chain block, which progress reads. It
// reads them after the Safe's chain, so that their block is as late as the
// executions that it finds, as far as the chains' clocks and nodes agree, and
// the timing says when it isn't.
//
// A request to a node or the service that fails without an answer (see
// ethrpc.Transient and txservice.Unavailable) fails it, since it says nothing
// about the execution, and so does a scan of the Safe's logs whose result isn't
// logs. A scan, or a request to decode another transaction's execution, that
// the node refuses with a JSON-RPC error leaves the status unknown, with the
// reason.
func (c *Checker) Check(
	ctx context.Context, r *safenet.Request, progress func(context.Context, ethrpc.Hash) (*safenet.Progress, error),
) (*Report, error) {
	tx := r.Proposal.Transaction
	if tx.ChainID == nil || !tx.ChainID.IsUint64() || tx.ChainID.Uint64() != c.Safe.ChainID() {
		return nil, fmt.Errorf("the request's Safe transaction is for chain %v, not %d", tx.ChainID, c.Safe.ChainID())
	}
	if tx.Nonce == nil {
		return nil, errors.New("the request's Safe transaction has no nonce")
	}
	report := &Report{
		Request: r.ID, Safe: tx.Safe, ChainID: tx.ChainID.Uint64(),
		SafeBlock: r.Proposal.SafeBlock, SafeTxHash: r.Proposal.SafeTxHash, Notes: []string{NotEvidence},
	}

	latest, err := c.Safe.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting the latest block of chain %d: %w", report.ChainID, err)
	}
	if uint64(latest) < report.SafeBlock {
		return nil, fmt.Errorf("the latest block %d of chain %d is before the Safe block %d", latest, report.ChainID, report.SafeBlock)
	}
	finder := &executions.Finder{Eth: c.Safe, Service: c.Service, Warn: c.Warn}
	n := &report.Nonce
	n.Transaction, n.LatestBlock = tx.Nonce, uint64(latest)
	if n.SafeAtSafeBlock, err = finder.ReadNonce(ctx, tx.Safe, ethrpc.BlockNumber(report.SafeBlock)); err != nil {
		return nil, err
	}
	if n.SafeAtLatest, err = finder.ReadNonce(ctx, tx.Safe, latest); err != nil {
		return nil, err
	}
	n.Stale = tx.Nonce.Cmp(n.SafeAtSafeBlock) < 0
	switch {
	case n.Stale:
		report.note("The transaction's nonce %s is below the Safe's nonce %s at the Safe block %d, so the Safe couldn't execute it on that state.",
			tx.Nonce, n.SafeAtSafeBlock, report.SafeBlock)
	case tx.Nonce.Cmp(n.SafeAtSafeBlock) > 0:
		report.note("The transaction's nonce %s is above the Safe's nonce %s at the Safe block %d, so the Safe could only execute it once its nonce reached it.",
			tx.Nonce, n.SafeAtSafeBlock, report.SafeBlock)
	}

	done := false
	if c.Service != nil {
		report.Execution.Service = &Service{URL: c.Service.URL(), ObservedAt: time.Now().UTC().Truncate(time.Second)}
		if done, err = c.fromService(ctx, report, finder); err != nil {
			return nil, err
		}
	}
	if !done {
		if err := c.scan(ctx, report, finder); err != nil {
			return nil, err
		}
	}

	p, err := progress(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	// The oracle sets the terms when the request is posted, so other deadlines mean
	// that r is of another oracle, or was changed.
	if p.Terms.CommitDeadline != r.Terms.CommitDeadline || p.Terms.RevealDeadline != r.Terms.RevealDeadline {
		return nil, fmt.Errorf("the oracle has the deadlines %d and %d for request %s, not %d and %d as the request has",
			p.Terms.CommitDeadline, p.Terms.RevealDeadline, r.ID, r.Terms.CommitDeadline, r.Terms.RevealDeadline)
	}
	report.State, report.StateBlock = p.State, p.Block
	if report.Execution.Block != nil {
		if report.Timing, err = c.timing(ctx, report, r, p); err != nil {
			return nil, err
		}
	}
	return report, nil
}

// note adds a note to the report.
func (r *Report) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// fromService sets the report's execution from the Safe Transaction Service,
// and reports whether it did: only if the service names the onchain transaction
// that executed it, and its receipt has the Safe's log of the execution, in a
// block up to the latest one that the report reads. Anything else that the
// service says is a note. An error of the service other than a failure to
// answer is a warning and a note, and finder then reads the chain without it.
func (c *Checker) fromService(ctx context.Context, report *Report, finder *executions.Finder) (bool, error) {
	listed, err := c.Service.Transaction(ctx, report.SafeTxHash)
	switch {
	case errors.Is(err, txservice.ErrNotFound):
		report.note("The Safe Transaction Service doesn't have the Safe transaction.")
		return false, nil
	case txservice.Unavailable(err):
		return false, fmt.Errorf("getting Safe transaction %s from the Safe Transaction Service: %w", report.SafeTxHash, err)
	case err != nil:
		c.warn(fmt.Errorf("getting Safe transaction %s from the Safe Transaction Service: %w", report.SafeTxHash, err))
		report.note("Getting the Safe transaction from the Safe Transaction Service failed, so the chain was read without it: %v.", err)
		finder.Service = nil
		return false, nil
	case !listed.Executed:
		report.note("The Safe Transaction Service doesn't list the Safe transaction as executed.")
		return false, nil
	case listed.TxHash == (ethrpc.Hash{}):
		report.note("The Safe Transaction Service lists the Safe transaction as executed, but names no onchain transaction.")
		return false, nil
	}
	receipt, err := c.Safe.GetTransactionReceipt(ctx, listed.TxHash)
	if ethrpc.Transient(err) {
		return false, fmt.Errorf("getting the receipt of transaction %s: %w", listed.TxHash, err)
	}
	e, ok := executions.InReceipt(receipt, report.Safe, report.SafeTxHash)
	if !ok {
		reason := "its receipt has no log of the Safe's execution of it"
		if err != nil {
			reason = fmt.Sprintf("getting its receipt failed: %v", err)
		}
		report.note("The Safe Transaction Service says that the Safe transaction was executed in transaction %s, which the chain doesn't confirm: %s.", listed.TxHash, reason)
		return false, nil
	}
	// The report is as of the latest block that it read the nonce at.
	if e.Block > report.Nonce.LatestBlock {
		report.note("The Safe Transaction Service says that the Safe transaction was executed in transaction %s, in block %d, after the latest block %d that the report reads, so it isn't used.",
			listed.TxHash, e.Block, report.Nonce.LatestBlock)
		return false, nil
	}
	if err := c.setExecution(ctx, report, Executed, SourceService, e); err != nil {
		return false, err
	}
	report.Execution.Reason = fmt.Sprintf("The Safe Transaction Service names transaction %s, whose receipt has the Safe's %s log of the Safe transaction hash, in block %d.",
		e.TxHash, executionEvent(e.Success), e.Block)
	return true, nil
}

// scan sets the report's execution from a scan of the Safe's execution logs in
// the Checker's Blocks blocks from the Safe block on, up to the latest, or for
// a stale nonce, which the Safe passed by the Safe block, up to it. Another
// transaction is only taken to have used the nonce if a source decoded it with
// that nonce, which its hash proves. A delegate call or a module can set the
// nonce back, so the nonce only shows that the transaction wasn't executed if
// the scan has no execution of it either. For a nonce that isn't stale, that
// can also be an execution before the Safe block, so the scan starts Blocks
// blocks before it, where it only looks for the transaction itself: another
// transaction's use of the nonce there was undone by the Safe block. For a
// stale nonce, a set-back after the Safe block can let the transaction run
// after it, so the scan goes on Blocks blocks after it, where it only looks for
// the transaction itself too.
//
// The logs have the hash of the transaction, so its execution needs no
// decoding. The other executions are only decoded where they count, from the
// Safe's nonce at the end of those blocks, which a set-back after them doesn't
// change.
//
// Only a scan, or a request to decode another execution, that the node refuses
// with a JSON-RPC error, such as over its range or result limit, leaves the
// status unknown. Anything else that isn't an answer, such as a null result,
// fails the check.
func (c *Checker) scan(ctx context.Context, report *Report, finder *executions.Finder) error {
	x, n := &report.Execution, &report.Nonce
	nonce, blocks := n.Transaction, max(c.Blocks, 1)
	from, to := report.SafeBlock, report.SafeBlock+min(blocks-1, n.LatestBlock-report.SafeBlock)
	// unscanned is whether the Safe can have used the nonce outside the blocks
	// scanned.
	unscanned := to < n.LatestBlock
	// before and after are the first and last blocks scanned for an execution of
	// the transaction.
	before, after := from-min(blocks, from), to
	if n.Stale {
		from, to = report.SafeBlock-min(blocks-1, report.SafeBlock), report.SafeBlock
		before, after = from, to+min(blocks, n.LatestBlock-to)
		unscanned = from > 0 || after < n.LatestBlock
	}
	x.Scanned = &executions.BlockRange{FromBlock: before, ToBlock: after}
	scanned, err := finder.Scan(ctx, report.Safe, ethrpc.BlockNumber(before), ethrpc.BlockNumber(after))
	if err != nil && !refusal(err) {
		return err
	}
	if err != nil {
		x.Status = Unknown
		x.Reason = fmt.Sprintf("Scanning the Safe's execution logs in blocks %d to %d failed: %v.", before, after, err)
		return nil
	}
	for _, e := range scanned.Executions {
		if e.SafeTxHash == report.SafeTxHash {
			if err := c.setExecution(ctx, report, Executed, SourceLogs, e); err != nil {
				return err
			}
			x.Reason = fmt.Sprintf("The Safe logged its %s of the Safe transaction hash in block %d, transaction %s, in a scan of blocks %d to %d.",
				executionEvent(e.Success), e.Block, e.TxHash, before, after)
			return nil
		}
	}
	found, err := scanned.Decode(ctx, ethrpc.BlockNumber(from), ethrpc.BlockNumber(to))
	if err != nil && !refusal(err) {
		return err
	}
	if err != nil {
		x.Status = Unknown
		x.Reason = fmt.Sprintf("The Safe logged no execution of the transaction in blocks %d to %d, but decoding the other executions in blocks %d to %d failed: %v.",
			before, after, from, to, err)
		return nil
	}
	unknown := 0
	for _, e := range found {
		if e.Transaction == nil {
			unknown++
			continue
		}
		if e.Transaction.Nonce.Cmp(nonce) == 0 {
			if err := c.setExecution(ctx, report, OtherTransaction, SourceLogs, e); err != nil {
				return err
			}
			x.Reason = fmt.Sprintf("The Safe executed another Safe transaction, %s, with the nonce %s in block %d, transaction %s (source %s), in a scan of blocks %d to %d.",
				e.SafeTxHash, nonce, e.Block, e.TxHash, e.Source, from, to)
			return nil
		}
	}

	if !n.Stale && n.SafeAtLatest.Cmp(nonce) <= 0 {
		x.Status, x.Source = NotExecuted, SourceNonce
		x.Reason = fmt.Sprintf("The Safe's nonce is %s at the latest block %d, not above the transaction's nonce %s, and the Safe logged no execution of the transaction in blocks %d to %d.",
			n.SafeAtLatest, n.LatestBlock, nonce, before, to)
		var outside []string
		if before > 0 {
			outside = append(outside, fmt.Sprintf("before block %d", before))
		}
		if unscanned {
			outside = append(outside, fmt.Sprintf("after block %d", to))
		}
		if len(outside) > 0 {
			x.Reason += fmt.Sprintf(" An execution %s that set the nonce back isn't ruled out.", strings.Join(outside, " or "))
		}
		return nil
	}
	passed, at := n.SafeAtLatest, n.LatestBlock
	if n.Stale {
		passed, at = n.SafeAtSafeBlock, report.SafeBlock
	}
	x.Status = Unknown
	x.Reason = fmt.Sprintf("The Safe's nonce is %s at block %d, above the transaction's nonce %s, but in blocks %d to %d the Safe logged no execution of the transaction, nor of another one that a source decoded with that nonce (executions there: %d, unknown transactions among them: %d).",
		passed, at, nonce, from, to, len(found), unknown)
	if before < from {
		x.Reason += fmt.Sprintf(" The Safe logged no execution of the transaction in blocks %d to %d either.", before, from-1)
	}
	if after > to {
		x.Reason += fmt.Sprintf(" The Safe logged no execution of the transaction in blocks %d to %d either.", to+1, after)
	}
	if unscanned {
		x.Reason += " The Safe may have used the nonce outside those blocks, which a larger -blocks scans."
	}
	return nil
}

// refusal reports whether err is the node's refusal of a request with a
// JSON-RPC error, such as for a range over its limit, which is an answer,
// unlike a failure to get one, or a result that isn't one.
func refusal(err error) bool {
	_, ok := errors.AsType[*ethrpc.Error](err)
	return ok && !ethrpc.Transient(err)
}

// setExecution sets the report's execution to the status, the source, and the
// execution e, with the time of its block.
func (c *Checker) setExecution(ctx context.Context, report *Report, status, source string, e executions.Execution) error {
	t, err := c.Safe.BlockTime(ctx, ethrpc.BlockNumber(e.Block))
	if err != nil {
		return fmt.Errorf("reading the time of block %d: %w", e.Block, err)
	}
	x := &report.Execution
	x.Status, x.Source = status, source
	x.TxHash, x.Block, x.BlockTime, x.Success, x.SafeTxHash = &e.TxHash, &e.Block, &t, &e.Success, &e.SafeTxHash
	return nil
}

// timing places the report's execution in the timeline of the request r, whose
// deadlines, freeze, and settlement as of the latest Gnosis Chain block p has.
//
// The execution is before the proposal only if its block's timestamp is
// earlier. The Safe block is the last block before the proposal's time, so that
// holds exactly for an execution in or before it, which timing checks. The
// execution is after a later event only if its block's timestamp is later, so a
// tie leaves it in the earlier phase, and if p doesn't have an event that can
// come before the execution yet, and change the timing, it is uncertain. On
// Gnosis Chain, each block has a later timestamp than its parent, so for a Safe
// there the order of the blocks decides, which also places the execution
// against a deadline that p hasn't reached.
func (c *Checker) timing(ctx context.Context, report *Report, r *safenet.Request, p *safenet.Progress) (*Timing, error) {
	point := func(block uint64) (Point, error) {
		t, err := c.Gnosis.BlockTime(ctx, ethrpc.BlockNumber(block))
		if err != nil {
			return Point{}, fmt.Errorf("reading the time of Gnosis Chain block %d: %w", block, err)
		}
		return Point{Block: block, Time: t}, nil
	}
	deadline := func(block uint64) (Deadline, error) {
		if block > p.Block {
			return Deadline{Block: block}, nil
		}
		reached, err := point(block)
		return Deadline{Block: block, Reached: true, Time: &reached.Time}, err
	}
	x := report.Execution
	t := &Timing{Execution: Point{Block: *x.Block, Time: *x.BlockTime}}
	var err error
	if t.Proposal, err = point(r.Proposal.Block); err != nil {
		return nil, err
	}
	if t.CommitDeadline, err = deadline(p.Terms.CommitDeadline); err != nil {
		return nil, err
	}
	if t.RevealDeadline, err = deadline(p.Terms.RevealDeadline); err != nil {
		return nil, err
	}
	if a := p.Arbitration; a != nil {
		frozen, err := point(a.FrozenBlock)
		if err != nil {
			return nil, err
		}
		t.Frozen = &frozen
		if a.Record != nil {
			settled, err := point(a.Record.Block)
			if err != nil {
				return nil, err
			}
			t.Resolved = &Resolution{Point: settled, Outcome: a.Outcome}
		}
	}
	if t.Now, err = point(p.Block); err != nil {
		return nil, err
	}

	type event struct {
		name  string
		point *Point
	}
	gnosis := report.ChainID == ethrpc.Gnosis
	var reveal, settled *Point
	if d := t.RevealDeadline; d.Reached {
		reveal = &Point{Block: d.Block, Time: *d.Time}
	} else if gnosis {
		// Its time is unknown, but its block places it.
		reveal = &Point{Block: d.Block}
	}
	if t.Resolved != nil {
		settled = &t.Resolved.Point
	}
	at := t.Execution.Time
	after := func(e *Point) bool {
		if gnosis {
			return e != nil && t.Execution.Block > e.Block
		}
		return e != nil && at.After(e.Time)
	}
	// later are the events that the timing places the execution before.
	var later []event
	switch {
	case at.Before(t.Proposal.Time):
		t.Value = BeforeProposal
	case after(settled):
		t.Value = AfterResolution
	case after(t.Frozen):
		t.Value, later = AfterFreeze, []event{{"settlement", settled}}
	case after(reveal):
		t.Value, later = AfterVoting, []event{{"freeze", t.Frozen}, {"settlement", settled}}
	default:
		t.Value, later = DuringVoting, []event{{"reveal deadline", reveal}, {"freeze", t.Frozen}, {"settlement", settled}}
	}
	if (t.Value == BeforeProposal) != (t.Execution.Block <= report.SafeBlock) {
		return nil, fmt.Errorf("the request's Safe block %d isn't the last block of chain %d before the proposal's time %s: the execution in block %d is at %s",
			report.SafeBlock, report.ChainID, t.Proposal.Time.Format(time.RFC3339), t.Execution.Block, at.Format(time.RFC3339))
	}

	what := "The Safe transaction was executed"
	if x.Status == OtherTransaction {
		what = "The other Safe transaction with the nonce was executed"
	}
	note := fmt.Sprintf("%s at %s, %s: the proposal was at %s, in Gnosis Chain block %d",
		what, at.Format(time.RFC3339), t.Value, t.Proposal.Time.Format(time.RFC3339), t.Proposal.Block)
	if t.Value == BeforeProposal {
		note += fmt.Sprintf(", and the execution is in or before the Safe block %d", report.SafeBlock)
	}
	report.note("%s.", note)
	// The request was read after the Safe's chain, but at a block that can still be
	// earlier than the execution, such as from a node that is behind, so an event
	// after that block may have come first. On Gnosis Chain, such an event is in a
	// later block, and one in the execution's own block is placed after the
	// execution. A freeze can't come before the votes are revealed, which starts in
	// the block after the commit deadline.
	unsettled := p.State == safenet.StatePending || p.State == safenet.StateFrozen
	switch {
	case gnosis:
		first := max(t.Now.Block, t.CommitDeadline.Block) + 1
		if t.Uncertain = unsettled && t.Execution.Block > first; t.Uncertain {
			report.note("The execution is in Gnosis Chain block %d, and the request was read at block %d, so a freeze or settlement in a block between them, from block %d on, may have come before it, and the timing may be later than %s.",
				t.Execution.Block, t.Now.Block, first, t.Value)
		}
	case (unsettled || !t.RevealDeadline.Reached) && at.After(t.Now.Time):
		t.Uncertain = true
		report.note("The execution is later than Gnosis Chain block %d, at %s, which the request was read at, so a deadline, freeze, or settlement after that block may have come before it, and the timing may be later than %s.",
			t.Now.Block, t.Now.Time.Format(time.RFC3339), t.Value)
	}
	// On Gnosis Chain, a tie is the event's own block, in which the order of the
	// transactions is known.
	sameBlock := func(other Point) bool { return gnosis && t.Execution.Block == other.Block }
	switch {
	case !at.Equal(t.Proposal.Time):
	case sameBlock(t.Proposal):
		report.note("The execution is in Gnosis Chain block %d, the block of the proposal, after the Safe block %d, so it is placed after the proposal, whichever of their transactions came first.",
			t.Execution.Block, report.SafeBlock)
	default:
		report.note("The execution's block has the same timestamp as the block of the proposal, so which came first is unknown; it is after the Safe block %d, the last block before the proposal's time, so it is placed after the proposal.",
			report.SafeBlock)
	}
	for _, e := range later {
		switch {
		case e.point == nil:
		case sameBlock(*e.point):
			report.note("The execution is in Gnosis Chain block %d, the block of the %s, and is placed before the %s.", t.Execution.Block, e.name, e.name)
		case !at.Equal(e.point.Time):
		default:
			report.note("The execution's block has the same timestamp as the block of the %s, so which came first is unknown; it is placed before the %s.", e.name, e.name)
		}
	}
	return t, nil
}

// executionEvent returns the name of the event that a Safe logs for an
// execution that succeeded or failed.
func executionEvent(success bool) string {
	if success {
		return "ExecutionSuccess"
	}
	return "ExecutionFailure"
}

func (c *Checker) warn(err error) {
	if c.Warn != nil {
		c.Warn(err)
	}
}
