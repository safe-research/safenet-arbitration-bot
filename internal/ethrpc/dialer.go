package ethrpc

import (
	"context"
	"sync"
)

// Dialer returns a client for the chain with the given ID.
type Dialer func(ctx context.Context, chainID uint64) (*Client, error)

// NewDialer returns a Dialer that connects to each chain with NewClient, using
// the URL that rpcs has for the chain's ID, or the chain's default RPC URLs if
// it has none. It connects to each chain once, and returns the same client, or
// the same error, every time after. It is safe for concurrent use.
func NewDialer(rpcs map[uint64]string) Dialer {
	var mu sync.Mutex
	connections := make(map[uint64]func() (*Client, error))
	return func(ctx context.Context, chainID uint64) (*Client, error) {
		mu.Lock()
		connect, ok := connections[chainID]
		if !ok {
			connect = sync.OnceValues(func() (*Client, error) {
				return NewClient(ctx, chainID, rpcs[chainID])
			})
			connections[chainID] = connect
		}
		mu.Unlock()
		return connect()
	}
}
