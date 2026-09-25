package ethrpc

import (
	"context"
	"fmt"
	"math/bits"
	"time"
)

// SearchBlock returns the last block with a timestamp strictly before t. It
// fails if no block is before t, and if the latest block is before t, as a
// block that is yet to come could be before t too.
//
// The search fetches a handful of block headers on a chain with a steady block
// time, and at most about twice as many as a binary search on any chain.
func (c *Client) SearchBlock(ctx context.Context, t time.Time) (Block, error) {
	latest, err := c.BlockNumber(ctx)
	if err != nil {
		return Block{}, err
	}
	// The search keeps lo before t, and hi at or after t.
	hi, err := c.GetBlockByNumber(ctx, latest)
	if err != nil {
		return Block{}, err
	}
	if hi.Time().Before(t) {
		return Block{}, fmt.Errorf("searching for the last block before %s: the latest block %d is at %s", t.Format(time.RFC3339), hi.Number, hi.Time().Format(time.RFC3339))
	}
	lo, err := c.GetBlockByNumber(ctx, 0)
	if err != nil {
		return Block{}, err
	}
	if !lo.Time().Before(t) {
		return Block{}, fmt.Errorf("searching for the last block before %s: the genesis block is at %s", t.Format(time.RFC3339), lo.Time().Format(time.RFC3339))
	}

	// Each probe estimates the block at t from the last two blocks fetched, as if
	// the chain's block time were steady between them: first from the genesis and
	// latest blocks, and then from blocks ever closer to t, which soon brings the
	// estimates within a block or so of t. An estimate outside the range between lo
	// and hi falls back to interpolating between them, and once a binary search
	// would have finished, the probes bisect the range instead.
	prev, last := lo, hi
	for probes, budget := 0, bits.Len64(uint64(hi.Number-lo.Number)); hi.Number-lo.Number > 1; probes++ {
		n, ok := estimate(prev, last, t)
		if !ok || n < lo.Number || n > hi.Number {
			n, _ = estimate(lo, hi, t)
		}
		if probes >= budget {
			n = lo.Number + (hi.Number-lo.Number)/2
		}
		n = min(max(n, lo.Number+1), hi.Number-1)

		b, err := c.GetBlockByNumber(ctx, n)
		if err != nil {
			return Block{}, err
		}
		if b.Time().Before(t) {
			lo = b
		} else {
			hi = b
		}
		prev, last = last, b
	}
	return lo, nil
}

// estimate returns the block that would be at t if the chain's block time were
// steady from a to b, and whether a and b have different timestamps, which it
// requires. The estimate can be outside the range from a to b, and is 0 if it
// would be before the genesis block.
func estimate(a, b Block, t time.Time) (BlockNumber, bool) {
	if a.Timestamp == b.Timestamp {
		return 0, false
	}
	blocks := float64(b.Number) - float64(a.Number)
	seconds := b.Time().Sub(a.Time()).Seconds()
	n := float64(b.Number) + t.Sub(b.Time()).Seconds()*blocks/seconds
	return BlockNumber(max(n, 0)), true
}
