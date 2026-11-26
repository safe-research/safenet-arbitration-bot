package ethrpc

import (
	"context"
	"errors"
	"iter"
)

// Nodes commonly limit the block range of eth_getLogs, often to 10,000 blocks,
// and only report the limit in an error message, whose format varies. ScanLogs
// starts with chunks of maxLogRange blocks, and halves them down to minLogRange
// blocks while the node answers with JSON-RPC errors, other than with HTTP
// status 429 or a server error, or a rate limit. It doubles them again after
// chunks succeed, since the error may have been brief.
var (
	maxLogRange uint64 = 10_000
	minLogRange uint64 = 50
)

// ScanLogs returns the logs matching filter, in order, querying the block range
// in chunks that the node accepts. Iteration stops at the first error, which is
// yielded with a zero Log. Stopping early saves querying the remaining chunks.
func (c *Client) ScanLogs(ctx context.Context, filter LogFilter) iter.Seq2[Log, error] {
	return func(yield func(Log, error) bool) {
		// A node that does limit the range fails a chunk that was doubled again, so
		// each time that happens, twice as many chunks must succeed before the next
		// doubling. wait is how many must, and succeeded is how many have since the
		// range last changed.
		step, wait, succeeded, doubled := maxLogRange, 1, 0, false
		for from := filter.FromBlock; from <= filter.ToBlock; {
			chunk := filter
			chunk.FromBlock = from
			chunk.ToBlock = min(from+BlockNumber(step-1), filter.ToBlock)
			// Some nodes report too many results with the code of a rate limit, which is
			// only retried as one if its message says so, or once the range can't be
			// halved, so that a range that is too large costs one request.
			retryable := throttled
			if step == minLogRange {
				retryable = rateLimited
			}
			logs, err := c.getLogs(ctx, retryable, chunk)
			// A JSON-RPC error may be about the range. An HTTP rate limit or server error
			// isn't, nor is a rate limit, and a smaller range only means more requests.
			if _, ok := errors.AsType[*Error](err); ok && !refused(err) && !throttled(err) && step > minLogRange {
				if doubled {
					wait *= 2
				}
				step, succeeded, doubled = max(step/2, minLogRange), 0, false
				continue
			}
			if err != nil {
				yield(Log{}, err)
				return
			}
			for _, log := range logs {
				if !yield(log, nil) {
					return
				}
			}
			if chunk.ToBlock == filter.ToBlock {
				return
			}
			from = chunk.ToBlock + 1
			if succeeded++; succeeded >= wait && step < maxLogRange {
				step, succeeded, doubled = min(2*step, maxLogRange), 0, true
			} else {
				doubled = false
			}
		}
	}
}
