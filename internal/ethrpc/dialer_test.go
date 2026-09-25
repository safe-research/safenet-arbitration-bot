package ethrpc

import (
	"sync"
	"testing"
)

func TestNewDialer(t *testing.T) {
	mainnet, mainnetChecks := node(t, "0x1", nil)
	gnosis, gnosisChecks := node(t, "0x64", nil)
	dial := NewDialer(map[uint64]string{Mainnet: mainnet, Gnosis: gnosis})

	// Dialing each chain concurrently, several times over, connects to it once.
	clients := make(map[uint64][]*Client)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 5 {
		for _, chainID := range []uint64{Mainnet, Gnosis} {
			wg.Go(func() {
				client, err := dial(t.Context(), chainID)
				if err != nil {
					t.Errorf("dial(%d): %v", chainID, err)
					return
				}
				mu.Lock()
				clients[chainID] = append(clients[chainID], client)
				mu.Unlock()
			})
		}
	}
	wg.Wait()

	for chainID, clients := range clients {
		for _, client := range clients {
			if client != clients[0] || client.ChainID() != chainID {
				t.Errorf("dial(%d): got a client for chain %d, or a different client each time", chainID, client.ChainID())
			}
		}
	}
	if clients[Mainnet][0].url != mainnet || clients[Gnosis][0].url != gnosis {
		t.Errorf("clients use %s and %s, want the configured %s and %s", clients[Mainnet][0].url, clients[Gnosis][0].url, mainnet, gnosis)
	}
	if mainnetChecks.Load() != 1 || gnosisChecks.Load() != 1 {
		t.Errorf("got %d and %d chain ID checks, want one per chain", mainnetChecks.Load(), gnosisChecks.Load())
	}
}

func TestNewDialerReusesErrors(t *testing.T) {
	// The node serves a different chain than the one it is configured for.
	url, checks := node(t, "0x64", nil)
	dial := NewDialer(map[uint64]string{Mainnet: url})
	for range 2 {
		if _, err := dial(t.Context(), Mainnet); err == nil {
			t.Fatal("dial: expected an error for a node on the wrong chain")
		}
	}
	if checks.Load() != 1 {
		t.Errorf("got %d chain ID checks, want 1", checks.Load())
	}
}
