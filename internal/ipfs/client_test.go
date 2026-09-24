package ipfs

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// newClient returns a client for the gateway at url, with $XDG_CACHE_HOME
// pointing at an empty directory so that tests don't share a cache. It
// returns the client's cache directory.
func newClient(t *testing.T, url string) (*Client, string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	client := NewClient(url)
	return client, client.cache
}

// gateway starts an HTTP gateway that serves content under its CID, and
// returns a client for it along with a count of the requests it has served.
func gateway(t *testing.T, content []byte) (*Client, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/ipfs/"+ComputeCID(content).String() {
			http.NotFound(w, r)
			return
		}
		w.Write(content)
	}))
	t.Cleanup(server.Close)
	client, _ := newClient(t, server.URL)
	return client, &requests
}

func TestFetch(t *testing.T) {
	content := []byte("# Safenet Arbitration Charter\n")
	client, _ := gateway(t, content)
	got, err := client.Fetch(t.Context(), ComputeCID(content))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("Fetch: got %q, want %q", got, content)
	}
}

func TestFetchGatewayWithTrailingSlash(t *testing.T) {
	content := []byte("content")
	client, _ := gateway(t, content)
	client.gateway += "/"
	if _, err := client.Fetch(t.Context(), ComputeCID(content)); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
}

func TestFetchRejectsMismatchedContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered"))
	}))
	t.Cleanup(server.Close)

	client, cache := newClient(t, server.URL)
	if _, err := client.Fetch(t.Context(), ComputeCID([]byte("original"))); err == nil {
		t.Fatal("Fetch: expected an error")
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Errorf("cache: got %d entries, want none", len(entries))
	}
}

func TestFetchRejectsOversizedContent(t *testing.T) {
	// The content matches its CID, so only the size limit rejects it.
	content := make([]byte, MaxContentSize+1)
	client, _ := gateway(t, content)
	if _, err := client.Fetch(t.Context(), ComputeCID(content)); err == nil {
		t.Fatal("Fetch: expected an error")
	}
}

func TestFetchHTTPError(t *testing.T) {
	client, _ := gateway(t, nil)
	if _, err := client.Fetch(t.Context(), ComputeCID([]byte("missing"))); err == nil {
		t.Fatal("Fetch: expected an error")
	}
}

func TestFetchCaches(t *testing.T) {
	content := []byte("content")
	cid := ComputeCID(content)
	client, requests := gateway(t, content)

	for range 2 {
		got, err := client.Fetch(t.Context(), cid)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("Fetch: got %q, want %q", got, content)
		}
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("gateway requests: got %d, want 1", n)
	}

	cached, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "arbot", "ipfs", cid.String()))
	if err != nil {
		t.Fatalf("reading cache entry: %v", err)
	}
	if !bytes.Equal(cached, content) {
		t.Errorf("cache entry: got %q, want %q", cached, content)
	}
}

func TestFetchReplacesCorruptedCacheEntry(t *testing.T) {
	content := []byte("content")
	cid := ComputeCID(content)
	client, requests := gateway(t, content)

	path := filepath.Join(client.cache, cid.String())
	if err := os.MkdirAll(client.cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := client.Fetch(t.Context(), cid)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("Fetch: got %q, want %q", got, content)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("gateway requests: got %d, want 1", n)
	}
	if cached, _ := os.ReadFile(path); !bytes.Equal(cached, content) {
		t.Errorf("cache entry: got %q, want %q", cached, content)
	}
}

func TestFetchWithoutCache(t *testing.T) {
	content := []byte("content")
	client, requests := gateway(t, content)
	client.cache = ""

	for range 2 {
		if _, err := client.Fetch(t.Context(), ComputeCID(content)); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("gateway requests: got %d, want 2", n)
	}
}
