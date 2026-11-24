package executions

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"

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
	// setBack is whether the nonces of the executions found show that a delegate
	// call set the Safe's nonce back.
	setBack bool
}

// Complete reports whether the executions found account for every nonce below
// the Safe's nonce, and the nonce counts the executions: none of them shows
// that a delegate call set it back.
func (h *History) Complete() bool {
	return h.Missing != nil && len(h.Missing) == 0 && !h.setBack
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
// they aren't found. Any other request to the node that fails, such as for a
// rate limit, fails FindAll, since it says nothing about the history, unless it
// reads the Safe's nonce at an execution's block and another source has the
// transaction (see Finder.Eth).
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
	if h.setBack = setBack(h.Executions); h.setBack {
		f.warn(errors.New("the nonces of the executions found show that a delegate call set the Safe's nonce back, so the nonce doesn't count the executions"))
	}
	return h, nil
}

// setBack reports whether the nonces of the known transactions of executions,
// in the order of their logs, show that a delegate call set the Safe's nonce
// back: an onchain transaction uses a nonce that isn't above those of the
// earlier ones. The executions of one onchain transaction can log in another
// order, since an execution nested in another logs first.
func setBack(executions []Execution) bool {
	// earlier is the highest nonce of the onchain transactions before tx, and
	// highest that of all of them so far.
	var earlier, highest *big.Int
	var tx ethrpc.Hash
	for _, e := range executions {
		if e.Transaction == nil {
			continue
		}
		n := e.Transaction.Nonce
		if e.TxHash != tx {
			tx, earlier = e.TxHash, highest
		}
		if earlier != nil && n.Cmp(earlier) <= 0 {
			return true
		}
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
		if log.Removed || checks.ProxyFactoryVersion(log.Address) == "" {
			continue
		}
		if e := events.Decode(log); e != nil && e.Name == "ProxyCreation" && e.Args[0].Value == safe {
			factory := log.Address
			c.TxHash, c.Factory = &hash, &factory
			break
		}
	}
	if c.TxHash == nil {
		f.warn(fmt.Errorf("the Safe Transaction Service names %s as the creation of %s, but it neither creates the account nor logs a ProxyCreation event of it from an official 1.3.0, 1.4.1, or 1.5.0 proxy factory, so only its block %d is reported", hash, safe, block))
	}
	return c, nil
}
