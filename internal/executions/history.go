package executions

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/checks"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/events"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

// History is every Safe transaction that a Safe executed up to a block, as
// FindAll finds them.
type History struct {
	// Nonce is the Safe's nonce at the last block. Every execution uses the next
	// nonce, so the Safe executed this number of Safe transactions up to it, unless
	// a delegate call changed its nonce.
	Nonce *big.Int
	// Executions are the executions found, in the order of their logs.
	Executions []Execution
	// Missing are the ranges of nonces below Nonce that no execution found used, in
	// order. It is nil if Nonce is too large to list them.
	Missing []NonceRange
	// Scanned are the ranges of blocks whose logs were scanned for the executions
	// that the service doesn't list, in order.
	Scanned []BlockRange
	// Rejected are the executions that the service lists up to the last block but
	// that the chain doesn't confirm, in the order that the service lists them.
	Rejected []Rejected
	// Creation is the creation of the Safe's account, if the service has it and the
	// chain confirms its block, or nil.
	Creation *Creation
	// uncounted is whether the Safe's nonce may not count the executions: their
	// nonces show that a delegate call set it back, or a delegate call or a module
	// left it other than one above the highest nonce of the executions up to its
	// block, or at the last block, or it couldn't be read there, or an execution is
	// unknown, which may be a delegate call.
	uncounted bool
}

// Complete reports whether the executions found account for every nonce below
// the Safe's nonce, and nothing shows that the nonce doesn't count the
// executions: none of them shows that a delegate call set it back, the nonce at
// the end of each block with a delegate call or an execution by a module that
// FindAll reads, and at the last block, is one above the highest nonce of the
// executions up to it, which the node can read, with an official singleton, and
// no execution is unknown. A module can still change the nonce in a block that
// FindAll doesn't read without any of them showing it.
func (h *History) Complete() bool {
	return h.Missing != nil && len(h.Missing) == 0 && !h.uncounted
}

// NonceRange is a range of nonces, From to To, including both.
type NonceRange struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
}

// BlockRange is a range of blocks, including both ends.
type BlockRange struct {
	FromBlock uint64 `json:"fromBlock"`
	ToBlock   uint64 `json:"toBlock"`
}

// Rejected is an execution that the Safe Transaction Service lists but the
// chain doesn't confirm, with the reason.
type Rejected struct {
	SafeTxHash ethrpc.Hash `json:"safeTxHash"`
	// TxHash is the onchain transaction that the service says executed it, which is
	// the zero hash if it gives none.
	TxHash ethrpc.Hash `json:"txHash"`
	// Nonce is the nonce that the service gives the transaction, or nil if it gives
	// none.
	Nonce  *big.Int `json:"nonce"`
	Reason string   `json:"reason"`
}

// FindAll returns every Safe transaction that the Safe executed up to the block
// to, in order.
//
// A Safe's whole history is too long to scan the logs of on public nodes, so it
// lists the Safe's executions from the Safe Transaction Service, which must be
// set, and uses them to find the blocks to read. It reads the Safe's logs in
// each block that the service names up to to, and keeps the executions that the
// Safe logged there, whether the service lists them rightly or not, which it
// decodes like Find, taking a transaction that the service lists with its hash
// from the listing. A block whose logs have no execution of the Safe is read
// once more, since a node behind a load balancer now and then answers for a
// block that it hasn't caught up with.
//
// It only confirms an execution that the service lists if its fields hash to
// the Safe transaction hash that the service reports, and the Safe logged the
// execution of that hash in the block and the onchain transaction that the
// service reports. It rejects any other, with the reason. One that the service
// lists after to isn't evidence of the history up to to, so it is left out.
//
// The Safe's nonce at to is the number of executions up to it, and the
// transactions that the service lists have their nonces, once their hashes
// check out. So for the nonces that no execution found has, it scans the blocks
// between the executions with the nonces before and after them, or from the
// block that created the Safe for the first ones, or up to to for the last
// ones. A scan that fails, such as one that a public node times out, is a
// warning: its range isn't in Scanned, and its nonces stay missing. A Safe that
// the service doesn't know is a warning too, and the scans look for all of its
// executions.
//
// Executions by modules don't use nonces, and the service doesn't list them, so
// they aren't found, but a module can change the nonce, so the scans note the
// blocks with one to check the nonce at. Any other request to the node that
// fails, such as for a rate limit, fails FindAll, since it says nothing about
// the history, unless it reads the Safe's nonce at an execution's block and
// another source has the transaction (see Finder.Eth). A nonce at a block with
// a delegate call or a module's execution that the node has no answer for, such
// as at a block whose state it doesn't keep, only leaves the history
// incomplete.
func (f *Finder) FindAll(ctx context.Context, safe ethrpc.Address, to ethrpc.BlockNumber) (*History, error) {
	if f.Service == nil {
		return nil, errors.New("finding every execution needs the Safe Transaction Service")
	}
	nonce, err := f.readNonce(ctx, safe, to)
	if err != nil {
		return nil, err
	}
	listed, err := f.Service.Executions(ctx, safe)
	switch {
	case errors.Is(err, txservice.ErrNotFound):
		f.warn(fmt.Errorf("the Safe Transaction Service doesn't know %s", safe))
	case err != nil:
		return nil, fmt.Errorf("listing the executions of %s from the Safe Transaction Service: %w", safe, err)
	}
	creation, err := f.CreationFromService(ctx, safe, to)
	if err != nil {
		return nil, err
	}

	// The transactions that the service lists, by hash, if their hash checks out,
	// whatever it says about their execution, and the blocks up to to that it
	// names.
	transactions := make(map[ethrpc.Hash]*safenet.SafeTransaction)
	var blocks []uint64
	for _, x := range listed {
		if x.Transaction.Safe == safe && matches(x.Transaction, x.SafeTxHash) {
			transactions[x.SafeTxHash] = x.Transaction
		}
		if x.Block != 0 && x.Block <= uint64(to) {
			blocks = append(blocks, x.Block)
		}
	}
	slices.Sort(blocks)
	c := newCollector(f, safe)
	c.modules = true
	for _, block := range slices.Compact(blocks) {
		read, err := c.scan(ctx, ethrpc.BlockNumber(block), ethrpc.BlockNumber(block))
		if err == nil && read == 0 {
			_, err = c.scan(ctx, ethrpc.BlockNumber(block), ethrpc.BlockNumber(block))
		}
		if err != nil {
			return nil, err
		}
	}

	h := &History{Nonce: nonce, Scanned: []BlockRange{}, Rejected: []Rejected{}, Creation: creation}
	for _, r := range f.gaps(ctx, c, transactions, nonce, to, h) {
		if _, err := c.scan(ctx, ethrpc.BlockNumber(r.FromBlock), ethrpc.BlockNumber(r.ToBlock)); err != nil {
			f.warn(fmt.Errorf("scanning blocks %d to %d for executions that the Safe Transaction Service doesn't list: %w", r.FromBlock, r.ToBlock, err))
			continue
		}
		h.Scanned = append(h.Scanned, r)
	}

	slices.SortFunc(c.executions, func(a, b Execution) int {
		return cmp.Or(cmp.Compare(a.Block, b.Block), cmp.Compare(a.LogIndex, b.LogIndex))
	})
	// Not every block up to to is read, since a scan can fail, so an execution's
	// nonce is worked out from the Safe's at the end of its block.
	d := &decoder{
		Finder: f, safe: safe, to: to, executions: c.executions, events: c.events, listed: transactions,
		byBlock: true, nexts: map[uint64]*big.Int{uint64(to): nonce},
	}
	if err := d.decodeAll(ctx); err != nil {
		return nil, err
	}
	h.Executions = c.executions

	found := make(map[blockHash][]ethrpc.Hash)
	for _, e := range h.Executions {
		k := blockHash{e.Block, e.SafeTxHash}
		found[k] = append(found[k], e.TxHash)
	}
	for _, x := range listed {
		if x.Block > uint64(to) {
			continue
		}
		if reason := confirm(x, safe, found); reason != "" {
			h.Rejected = append(h.Rejected, Rejected{SafeTxHash: x.SafeTxHash, TxHash: x.TxHash, Nonce: x.Transaction.Nonce, Reason: reason})
		}
	}
	if nonce.IsUint64() {
		h.Missing = missing(h.Executions, nonce.Uint64())
	} else {
		f.warn(fmt.Errorf("the Safe's nonce %s is too large to list the nonces that no execution used", nonce))
	}
	if h.uncounted = setBack(h.Executions); h.uncounted {
		f.warn(errors.New("the nonces of the executions found show that a delegate call set the Safe's nonce back, so the nonce doesn't count the executions"))
	} else if h.Missing != nil && len(h.Missing) == 0 {
		counted, err := d.counted(ctx, c.moduleBlocks)
		if err != nil {
			return nil, err
		}
		h.uncounted = !counted
	}
	return h, nil
}

// counted reports whether the Safe's nonce counts the executions found at the
// end of each block where something else may have changed it: each block with
// an execution found that is a delegate call, or that Safe 1.1.1 or 1.2.0
// executed, each of moduleBlocks, which have an execution by a module, and to.
// There it must be one above the highest nonce of the executions found up to
// the block, as executions leave it, or 0 if there are none, and, other than at
// to, whose nonce is the history's, the Safe's singleton must be an official
// Safe singleton, whose nonce() is the Safe's nonce. A delegate call or a
// module can set the nonce to anything, which the nonces of the executions only
// show if both executions of a reused nonce are found, and at to, after the
// last execution, not even then. From Safe 1.3.0 on, a delegate call to
// MultiSendCallOnly only makes calls, which, like an execution that is a call,
// can only change the nonce through a module, whose executions are only found
// in the blocks that FindAll reads. Before 1.3.0, a call to the Safe itself can
// also run changeMasterCopy, which replaces the singleton, so any execution
// whose hash is that of 1.1.1 and 1.2.0 is checked. An unknown execution may be
// any delegate call. It warns about each block where the nonce or the singleton
// isn't, once about those where it can't be read, such as at a block whose
// state the node doesn't keep, and once about unknown executions. A read that
// fails transiently, such as for a rate limit, returns its error instead, since
// it says nothing about the nonce.
func (d *decoder) counted(ctx context.Context, moduleBlocks []uint64) (bool, error) {
	delegateCalls := make(map[uint64]bool)
	older := make(map[uint64]bool)
	unknown := false
	for _, e := range d.executions {
		tx := e.Transaction
		if tx == nil {
			unknown = true
			continue
		}
		if tx.Operation == safenet.OperationDelegateCall && !checks.MultiSendCallOnly(tx.To) {
			delegateCalls[e.Block] = true
		} else if tx.Hash() != e.SafeTxHash {
			// It matched the older hash of Safe 1.1.1 and 1.2.0.
			older[e.Block] = true
		}
	}
	modules := make(map[uint64]bool)
	for _, block := range moduleBlocks {
		modules[block] = true
	}
	// which names what may have changed the nonce in block.
	which := func(block uint64) string {
		switch {
		case delegateCalls[block] && modules[block]:
			return "a delegate call and an execution by a module"
		case delegateCalls[block]:
			return "a delegate call"
		case older[block] && modules[block]:
			return "an execution by Safe 1.1.1 or 1.2.0 and an execution by a module"
		case older[block]:
			return "an execution by Safe 1.1.1 or 1.2.0"
		}
		return "an execution by a module"
	}
	// nonceAfter returns the nonce that the executions found up to block leave, one
	// above the highest of theirs, for blocks in order, as the executions are.
	var highest *big.Int
	done := 0
	nonceAfter := func(block uint64) *big.Int {
		for ; done < len(d.executions) && d.executions[done].Block <= block; done++ {
			if tx := d.executions[done].Transaction; tx != nil && (highest == nil || tx.Nonce.Cmp(highest) > 0) {
				highest = tx.Nonce
			}
		}
		if highest == nil {
			return new(big.Int)
		}
		return new(big.Int).Add(highest, big.NewInt(1))
	}

	blocks := slices.Concat(slices.Collect(maps.Keys(delegateCalls)), slices.Collect(maps.Keys(older)), moduleBlocks)
	slices.Sort(blocks)
	blocks = slices.Compact(blocks)
	counted, unread := true, 0
	for _, block := range blocks {
		want := nonceAfter(block)
		next, err := d.nonceAt(ctx, block)
		if ethrpc.Transient(err) {
			return false, err
		}
		// A delegate call can also have replaced the singleton, whose nonce() is then
		// no evidence of the Safe's nonce.
		var account checks.SafeAccount
		if next != nil {
			account, err = checks.NewCallReader(d.Eth, d.safe, ethrpc.BlockNumber(block)).Safe(ctx)
			if ethrpc.Transient(err) {
				return false, err
			}
		}
		switch {
		case next == nil || err != nil:
			unread++
		case account.SingletonVersion == nil && !olderSingleton(account.Singleton, d.Eth.ChainID()):
			d.warn(fmt.Errorf("the Safe's singleton at the end of block %d, which has %s, is %s rather than an official Safe singleton, so its nonce() may not be the Safe's nonce", block, which(block), account.Singleton))
			counted = false
		case next.Cmp(want) != 0:
			d.warn(fmt.Errorf("the Safe's nonce at the end of block %d, which has %s, is %s rather than %s, one above the highest nonce of the executions found up to it, so the nonce doesn't count the executions", block, which(block), next, want))
			counted = false
		}
	}
	if unread > 0 {
		d.warn(fmt.Errorf("the Safe's nonce at the end of %d blocks with a delegate call, an execution by Safe 1.1.1 or 1.2.0, or an execution by a module is unknown, so it may not count the executions", unread))
		counted = false
	}
	// The nonce at to is the history's, so it needs no read. A set-back after the
	// last execution found leaves no other trace.
	if to := uint64(d.to); !slices.Contains(blocks, to) {
		if next, want := d.nexts[to], nonceAfter(to); next != nil && next.Cmp(want) != 0 {
			d.warn(fmt.Errorf("the Safe's nonce at block %d is %s rather than %s, one above the highest nonce of the executions found, so the nonce doesn't count the executions", to, next, want))
			counted = false
		}
	}
	if unknown {
		d.warn(errors.New("an execution found is unknown, so it may be a delegate call that changed the Safe's nonce"))
		counted = false
	}
	return counted, nil
}

// olderSingletons are the official singletons of the Safe versions before
// 1.3.0, which § 2.1 doesn't list, but which Safes with the proxies of 1.0.0
// and 1.1.1 that it lists ran on before they upgraded. Their nonce() is the
// Safe's nonce too. The safe-deployments repository lists them, at
// https://github.com/safe-global/safe-deployments/tree/v1.37.67/src/assets in
// v1.0.0, v1.1.1, and v1.2.0/gnosis_safe.json, and of the networks that the
// Charter covers, only on Ethereum Mainnet and Gnosis Chain.
var olderSingletons = map[ethrpc.Address]string{
	ethrpc.MustParseAddress("0xb6029EA3B2c51D09a50B53CA8012FeEB05bDa35A"): "1.0.0",
	ethrpc.MustParseAddress("0x34CfAC646f301356fAa8B21e94227e3583Fe3F5F"): "1.1.1",
	ethrpc.MustParseAddress("0x6851D6fDFAfD08c0295C392436245E5bc78B0185"): "1.2.0",
}

// olderSingleton reports whether address is one of olderSingletons on the
// network with chainID.
func olderSingleton(address ethrpc.Address, chainID uint64) bool {
	return olderSingletons[address] != "" && (chainID == ethrpc.Mainnet || chainID == ethrpc.Gnosis)
}

// setBack reports whether the nonces of the known transactions of executions,
// in the order of their logs, show that a delegate call set the Safe's nonce
// back: an onchain transaction uses a nonce that isn't above those of the
// earlier ones, or uses one twice. The executions of one onchain transaction
// can log in another order, since an execution nested in another logs first.
func setBack(executions []Execution) bool {
	// earlier is the highest nonce of the onchain transactions before tx, highest
	// that of all of them so far, and used the nonces of tx so far.
	var earlier, highest *big.Int
	var tx ethrpc.Hash
	var used map[string]bool
	for _, e := range executions {
		if e.Transaction == nil {
			continue
		}
		n := e.Transaction.Nonce
		if e.TxHash != tx {
			tx, earlier, used = e.TxHash, highest, make(map[string]bool)
		}
		key := n.String()
		if earlier != nil && n.Cmp(earlier) <= 0 || used[key] {
			return true
		}
		used[key] = true
		if highest == nil || n.Cmp(highest) > 0 {
			highest = n
		}
	}
	return false
}

// blockHash is a block and the hash of a Safe transaction executed in it.
type blockHash struct {
	block      uint64
	safeTxHash ethrpc.Hash
}

// confirm returns why the chain doesn't confirm the execution x that the
// service lists, given the onchain transactions of the executions of the Safe
// found, in the order of their logs, by block and Safe transaction hash, or ""
// if it does.
func confirm(x txservice.Execution, safe ethrpc.Address, found map[blockHash][]ethrpc.Hash) string {
	switch {
	case x.Block == 0 || x.TxHash == (ethrpc.Hash{}):
		return "the service gives no onchain transaction or block"
	case x.Transaction.Safe != safe:
		return fmt.Sprintf("the transaction is for the Safe %s", x.Transaction.Safe)
	case !matches(x.Transaction, x.SafeTxHash):
		return "the transaction's fields don't hash to its Safe transaction hash"
	}
	txs := found[blockHash{x.Block, x.SafeTxHash}]
	if slices.Contains(txs, x.TxHash) {
		return ""
	}
	if len(txs) > 0 {
		return fmt.Sprintf("the Safe logged its execution in block %d in the transaction %s, not the one that the service reports", x.Block, txs[0])
	}
	return fmt.Sprintf("the Safe logged no execution of it in block %d", x.Block)
}

// missing returns the ranges of the nonces below next that none of executions
// used.
func missing(executions []Execution, next uint64) []NonceRange {
	var used []uint64
	for _, e := range executions {
		if tx := e.Transaction; tx != nil && tx.Nonce.IsUint64() && tx.Nonce.Uint64() < next {
			used = append(used, tx.Nonce.Uint64())
		}
	}
	slices.Sort(used)
	ranges := []NonceRange{}
	from := uint64(0)
	for _, n := range slices.Compact(used) {
		if n > from {
			ranges = append(ranges, NonceRange{From: from, To: n - 1})
		}
		from = n + 1
	}
	if from < next {
		ranges = append(ranges, NonceRange{From: from, To: next - 1})
	}
	return ranges
}

// gaps returns the ranges of blocks to scan for the executions with the nonces,
// below nonce, that no execution that c collected has, given the transactions
// by hash whose hashes check out. Each range is between the blocks of the
// executions with the nonces before and after the missing ones, which are where
// they must be, since nonces only increase. The range before the first
// execution found starts at the creation of h, or, if it is unknown, at the
// first block with the Safe's code, which becomes its creation.
func (f *Finder) gaps(ctx context.Context, c *collector, transactions map[ethrpc.Hash]*safenet.SafeTransaction, nonce *big.Int, to ethrpc.BlockNumber, h *History) []BlockRange {
	if !nonce.IsInt64() {
		f.warn(fmt.Errorf("the Safe's nonce %s is too large to find the executions that it doesn't have", nonce))
		return nil
	}
	// The blocks of the executions by their nonce, which only an execution whose
	// transaction checks out has. A nonce that a delegate call reused has the block
	// of its first execution.
	blocks := make(map[int64]uint64)
	for _, e := range c.executions {
		tx := transactions[e.SafeTxHash]
		if tx == nil {
			for _, event := range c.events[e.TxHash] {
				if matches(event, e.SafeTxHash) {
					tx = event
					break
				}
			}
		}
		if tx == nil || tx.Nonce.Cmp(nonce) >= 0 {
			continue
		}
		if b, ok := blocks[tx.Nonce.Int64()]; !ok || e.Block < b {
			blocks[tx.Nonce.Int64()] = e.Block
		}
	}
	// The Safe's nonce is that of the next execution, after the last block.
	blocks[nonce.Int64()] = uint64(to)

	var ranges []BlockRange
	// The execution before the first is the Safe's creation, at a block that is
	// only searched for if an execution before the first one found is missing.
	prev, prevBlock := int64(-1), uint64(0)
	for _, n := range slices.Sorted(maps.Keys(blocks)) {
		// Nonces that a delegate call changed may be out of the order of their blocks,
		// which leaves no range between them.
		if n > prev+1 && prevBlock <= blocks[n] {
			from, ok := prevBlock, true
			if prev == -1 {
				from, ok = f.firstBlock(ctx, c.safe, ethrpc.BlockNumber(blocks[n]), h)
			}
			if ok && from <= blocks[n] {
				ranges = append(ranges, BlockRange{FromBlock: from, ToBlock: blocks[n]})
			}
		}
		prev, prevBlock = n, blocks[n]
	}
	return ranges
}

// firstBlock returns the block that the Safe's history starts at: the block of
// the creation of h, if known, or else the first block, up to to, at which it
// has code, which a Safe proxy has from its deployment on, and which it sets as
// the creation's. It warns and returns false if it can't find it.
func (f *Finder) firstBlock(ctx context.Context, safe ethrpc.Address, to ethrpc.BlockNumber, h *History) (uint64, bool) {
	if h.Creation != nil {
		return h.Creation.Block, true
	}
	block, err := f.deployment(ctx, safe, to)
	if err != nil {
		f.warn(fmt.Errorf("finding the block that %s was deployed in: %w", safe, err))
		return 0, false
	}
	// The code confirms the block, but no transaction.
	h.Creation = &Creation{Block: block}
	return block, true
}

// deployment returns the first block, up to to, at which safe has code. It
// searches the blocks by bisection, which needs a node that serves the code of
// old blocks.
func (f *Finder) deployment(ctx context.Context, safe ethrpc.Address, to ethrpc.BlockNumber) (uint64, error) {
	code, err := f.Eth.GetCode(ctx, safe, to)
	if err != nil {
		return 0, err
	}
	if len(code) == 0 {
		return 0, fmt.Errorf("%s has no code at block %d", safe, to)
	}
	lo, hi := uint64(0), uint64(to)
	for lo < hi {
		mid := lo + (hi-lo)/2
		code, err := f.Eth.GetCode(ctx, safe, ethrpc.BlockNumber(mid))
		if err != nil {
			return 0, err
		}
		if len(code) > 0 {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo, nil
}

// Creation is the creation of a Safe's account.
type Creation struct {
	// Block is the block whose transaction gave the account its code: it has no
	// code at the block before, and has code at Block.
	Block uint64 `json:"block"`
	// BlockTime is the timestamp of Block. FindAll leaves it zero, for the caller
	// to set.
	BlockTime time.Time `json:"blockTime,omitzero"`
	// TxHash is the transaction that the service says created the account, if the
	// chain binds it to the account: its receipt names the account as the contract
	// that it created, or has the account's ProxyCreation event from an official
	// proxy factory. It is nil otherwise, since the code only confirms the block.
	TxHash *ethrpc.Hash `json:"txHash"`
	// Factory is the official proxy factory that logged the account's ProxyCreation
	// event in the transaction, or nil if none did.
	Factory *ethrpc.Address `json:"factory"`
}

// CreationFromService returns the creation of the Safe's account by block to,
// if the Finder's Service has the transaction that created it and the chain
// confirms its block: the transaction's receipt is in a block up to to, the
// account has no code at the block before it, and has code at its block. The
// creation has the transaction only if the chain binds it to the account, and
// warns if it doesn't. It returns nil if the service doesn't have the creation,
// and nil with a warning if the service refuses the request or the chain
// doesn't confirm its block, such as for a node that doesn't serve the code at
// old blocks. It fails if a request to the service or a node fails transiently,
// such as for a rate limit, since that says nothing about the creation.
func (f *Finder) CreationFromService(ctx context.Context, safe ethrpc.Address, to ethrpc.BlockNumber) (*Creation, error) {
	if f.Service == nil {
		return nil, nil
	}
	hash, err := f.Service.Creation(ctx, safe)
	if errors.Is(err, txservice.ErrNotFound) {
		return nil, nil
	}
	if txservice.Unavailable(err) {
		return nil, fmt.Errorf("getting the creation of %s from the Safe Transaction Service: %w", safe, err)
	}
	if err != nil {
		f.warn(fmt.Errorf("getting the creation of %s from the Safe Transaction Service: %w", safe, err))
		return nil, nil
	}
	receipt, err := f.Eth.GetTransactionReceipt(ctx, hash)
	if ethrpc.Transient(err) {
		return nil, fmt.Errorf("getting the receipt of the creation %s: %w", hash, err)
	}
	if err != nil {
		f.warn(fmt.Errorf("getting the receipt of the creation %s: %w", hash, err))
		return nil, nil
	}
	block := receipt.BlockNumber
	if block > to || block == 0 {
		return nil, nil
	}
	before, err := f.Eth.GetCode(ctx, safe, block-1)
	var after ethrpc.Bytes
	if err == nil {
		after, err = f.Eth.GetCode(ctx, safe, block)
	}
	if ethrpc.Transient(err) {
		return nil, fmt.Errorf("confirming the creation %s of %s: %w", hash, safe, err)
	}
	switch {
	case err != nil:
	case len(before) != 0:
		err = fmt.Errorf("the account has code at block %d, before it", block-1)
	case len(after) == 0:
		err = fmt.Errorf("the account has no code at block %d", block)
	}
	if err != nil {
		f.warn(fmt.Errorf("confirming the creation %s of %s: %w", hash, safe, err))
		return nil, nil
	}

	// The code confirms the block, but any transaction in it would pass, so the
	// transaction is only kept if it is bound to the account. Any contract can log
	// a ProxyCreation event, so it must be from an official factory.
	c := &Creation{Block: uint64(block)}
	if receipt.ContractAddress != nil && *receipt.ContractAddress == safe {
		c.TxHash = &hash
	}
	for _, log := range receipt.Logs {
		if log.Removed || checks.ProxyFactoryVersion(log.Address, f.Eth.ChainID()) == "" {
			continue
		}
		if e := events.Decode(log); e != nil && e.Name == "ProxyCreation" && e.Args[0].Value == safe {
			factory := log.Address
			c.TxHash, c.Factory = &hash, &factory
			break
		}
	}
	if c.TxHash == nil {
		f.warn(fmt.Errorf("the Safe Transaction Service names %s as the creation of %s, but it neither creates the account nor logs a ProxyCreation event of it from an official proxy factory, so only its block %d is reported", hash, safe, block))
	}
	return c, nil
}
