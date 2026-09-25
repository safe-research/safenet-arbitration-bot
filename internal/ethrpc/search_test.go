package ethrpc

import (
	"encoding/json"
	"math/bits"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// blockNode starts a JSON-RPC server for a chain of n blocks, where block i has
// the timestamp timestamp(i), in seconds. It serves eth_blockNumber and
// eth_getBlockByNumber, and counts the blocks it is asked for.
func blockNode(t *testing.T, n uint64, timestamp func(i uint64) uint64) (*Client, *atomic.Int64) {
	t.Helper()
	var fetched atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64            `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var result any
		switch req.Method {
		case "eth_chainId":
			result = "0x1"
		case "eth_blockNumber":
			result = BlockNumber(n - 1)
		case "eth_getBlockByNumber":
			fetched.Add(1)
			var number BlockNumber
			if err := json.Unmarshal(req.Params[0], &number); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if uint64(number) < n {
				result = Block{Number: number, Timestamp: Quantity(timestamp(uint64(number)))}
			}
		default:
			http.Error(w, "unexpected method "+req.Method, http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return connect(t, server.URL), &fetched
}

func TestSearchBlock(t *testing.T) {
	// Some chains give consecutive blocks the same timestamp.
	timestamps := []uint64{100, 105, 105, 105, 112, 120, 120, 130}
	client, _ := blockNode(t, uint64(len(timestamps)), func(i uint64) uint64 { return timestamps[i] })

	tests := []struct {
		t    time.Time
		want BlockNumber
	}{
		{time.Unix(101, 0), 0},
		{time.Unix(105, 0), 0},
		{time.Unix(106, 0), 3},
		{time.Unix(112, 0), 3},
		{time.Unix(112, 500_000_000), 4},
		{time.Unix(120, 0), 4},
		{time.Unix(121, 0), 6},
		{time.Unix(130, 0), 6},
	}
	for _, test := range tests {
		block, err := client.SearchBlock(t.Context(), test.t)
		if err != nil {
			t.Errorf("SearchBlock(%s): %v", test.t, err)
			continue
		}
		if block.Number != test.want || uint64(block.Timestamp) != timestamps[test.want] {
			t.Errorf("SearchBlock(%s): got block %d at %d, want block %d", test.t, block.Number, block.Timestamp, test.want)
		}
	}
}

// irregularTimestamps returns the timestamps of a chain of n blocks with
// irregular block times: mostly 12 seconds, but also blocks that share their
// parent's timestamp, 2-second blocks, and gaps of up to a day.
func irregularTimestamps(n int) []uint64 {
	r := rand.New(rand.NewPCG(1, 2))
	timestamps := make([]uint64, n)
	timestamps[0] = 1_000_000
	for i := 1; i < n; i++ {
		var step uint64
		switch x := r.IntN(100); {
		case x < 5:
			step = 0
		case x < 30:
			step = 2
		case x < 31:
			step = r.Uint64N(86_400)
		default:
			step = 12
		}
		timestamps[i] = timestamps[i-1] + step
	}
	return timestamps
}

func TestSearchBlockMatchesLinearSearch(t *testing.T) {
	const n = 2_000
	timestamps := irregularTimestamps(n)
	client, fetched := blockNode(t, n, func(i uint64) uint64 { return timestamps[i] })
	// The latest and genesis blocks, and at most two probes per halving.
	limit := int64(2 + 2*bits.Len64(n))
	r := rand.New(rand.NewPCG(3, 4))
	for range 500 {
		ts := timestamps[0] + 1 + r.Uint64N(timestamps[n-1]-timestamps[0])
		want := BlockNumber(sort.Search(n, func(i int) bool { return timestamps[i] >= ts }) - 1)

		fetched.Store(0)
		block, err := client.SearchBlock(t.Context(), time.Unix(int64(ts), 0))
		if err != nil {
			t.Fatalf("SearchBlock(%d): %v", ts, err)
		}
		if block.Number != want {
			t.Errorf("SearchBlock(%d): got block %d, want %d", ts, block.Number, want)
		}
		if fetched.Load() > limit {
			t.Errorf("SearchBlock(%d): fetched %d blocks, want at most %d", ts, fetched.Load(), limit)
		}
	}
}

func TestSearchBlockConvergesOnSteadyBlockTime(t *testing.T) {
	const n = 25_000_000
	// Mostly 12-second blocks, with every 97th slot missed, and a slower start.
	timestamp := func(i uint64) uint64 {
		if i < 1_000_000 {
			return 1_500_000_000 + 14*i
		}
		i -= 1_000_000
		return 1_500_000_000 + 14*1_000_000 + 12*(i+i/97)
	}
	client, fetched := blockNode(t, n, timestamp)
	r := rand.New(rand.NewPCG(5, 6))
	for range 100 {
		i := r.Uint64N(n - 1)
		want := BlockNumber(i)
		// Any time after the block, up to and including the next block's timestamp.
		gap := time.Duration(timestamp(i+1)-timestamp(i)) * time.Second
		ts := time.Unix(int64(timestamp(i)), 0).Add(1 + time.Duration(r.Int64N(int64(gap))))

		fetched.Store(0)
		block, err := client.SearchBlock(t.Context(), ts)
		if err != nil {
			t.Fatalf("SearchBlock(%s): %v", ts, err)
		}
		if block.Number != want {
			t.Errorf("SearchBlock(%s): got block %d, want %d", ts, block.Number, want)
		}
		if got := fetched.Load(); got > 7 {
			t.Errorf("SearchBlock(%s): fetched %d blocks, want at most 7", ts, got)
		}
	}
}

func TestSearchBlockOutOfRange(t *testing.T) {
	client, _ := blockNode(t, 10, func(i uint64) uint64 { return 100 + 10*i })
	tests := []struct {
		t    time.Time
		want string
	}{
		{time.Unix(100, 0), "the genesis block is at"},
		{time.Unix(50, 0), "the genesis block is at"},
		{time.Unix(191, 0), "the latest block 9 is at"},
	}
	for _, test := range tests {
		_, err := client.SearchBlock(t.Context(), test.t)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("SearchBlock(%s): got error %v, want one saying %q", test.t, err, test.want)
		}
	}
}
