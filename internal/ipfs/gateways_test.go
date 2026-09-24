package ipfs

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
)

// useGatewayList replaces the process-wide default gateway list with one
// served from body for the duration of the test, and returns a count of the
// requests made for it.
func useGatewayList(t *testing.T, body string) *atomic.Int64 {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	previous := defaultGateways
	defaultGateways = &gatewayList{url: server.URL, http: http.DefaultClient}
	t.Cleanup(func() { defaultGateways = previous })
	return &requests
}

func TestFetchUsesDefaultGateway(t *testing.T) {
	first, second := []byte("first"), []byte("second")
	upstream, gatewayRequests := gateway(t, first)

	list, err := json.Marshal([]string{upstream.gateway, "https://unused.example"})
	if err != nil {
		t.Fatal(err)
	}
	listRequests := useGatewayList(t, string(list))

	client, _ := newClient(t, "")
	got, err := client.Fetch(t.Context(), ComputeCID(first))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, first) {
		t.Errorf("Fetch: got %q, want %q", got, first)
	}

	// Serving from the cache doesn't need a gateway, and later downloads,
	// including by other clients, reuse the downloaded list.
	if _, err := client.Fetch(t.Context(), ComputeCID(first)); err != nil {
		t.Fatalf("Fetch from cache: %v", err)
	}
	other, _ := newClient(t, "")
	if _, err := other.Fetch(t.Context(), ComputeCID(first)); err != nil {
		t.Fatalf("Fetch with another client: %v", err)
	}
	if _, err := other.Fetch(t.Context(), ComputeCID(second)); err == nil {
		t.Fatal("Fetch: expected an error for content the gateway doesn't have")
	}
	if n := listRequests.Load(); n != 1 {
		t.Errorf("gateway list requests: got %d, want 1", n)
	}
	if n := gatewayRequests.Load(); n != 3 {
		t.Errorf("gateway requests: got %d, want 3", n)
	}
}

func TestFetchDoesNotDownloadGatewayListOnCacheHit(t *testing.T) {
	content := []byte("content")
	cached, _ := gateway(t, content)
	if _, err := cached.Fetch(t.Context(), ComputeCID(content)); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	listRequests := useGatewayList(t, `[]`)
	client := NewClient("")
	client.cache = cached.cache
	if _, err := client.Fetch(t.Context(), ComputeCID(content)); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n := listRequests.Load(); n != 0 {
		t.Errorf("gateway list requests: got %d, want 0", n)
	}
}

func TestDefaultGatewaysSkipsInvalidEntries(t *testing.T) {
	useGatewayList(t, `["ipfs.filebase.io", "https://ipfs.filebase.io", "ftp://example.com", "http://localhost:8080"]`)
	gateways, err := DefaultGateways(t.Context())
	if err != nil {
		t.Fatalf("DefaultGateways: %v", err)
	}
	if want := []string{"https://ipfs.filebase.io", "http://localhost:8080"}; !slices.Equal(gateways, want) {
		t.Errorf("DefaultGateways: got %q, want %q", gateways, want)
	}
}

func TestDefaultGatewaysRetriesAfterFailure(t *testing.T) {
	requests := useGatewayList(t, `[]`)
	for range 2 {
		if _, err := DefaultGateway(t.Context()); err == nil {
			t.Fatal("DefaultGateway: expected an error")
		}
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("gateway list requests: got %d, want 2", n)
	}
}

func TestDefaultGatewaysRejectsInvalidList(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":         `<html>`,
		"not an array":     `{"gateways": []}`,
		"empty":            `[]`,
		"no valid entries": `["ipfs.filebase.io", "ftp://ipfs.filebase.io"]`,
	} {
		t.Run(name, func(t *testing.T) {
			useGatewayList(t, body)
			if _, err := DefaultGateways(t.Context()); err == nil {
				t.Fatal("DefaultGateways: expected an error")
			}
		})
	}
}

func TestDefaultGatewaysHTTPError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	list := &gatewayList{url: server.URL, http: http.DefaultClient}
	if _, err := list.get(t.Context()); err == nil {
		t.Fatal("get: expected an error")
	}
}
