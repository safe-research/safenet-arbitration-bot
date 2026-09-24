package ipfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/safe-research/safenet-arbitration-bot/internal/xdg"
)

// MaxContentSize is the largest content that Fetch accepts. Supported CIDs
// address a single raw block, and IPFS limits blocks to 2 MiB.
const MaxContentSize = 2 << 20

// Client fetches content from an IPFS HTTP gateway, caching it on disk.
type Client struct {
	// gateway is the gateway base URL. An empty string means the default
	// gateway.
	gateway string
	http    *http.Client
	// cache is the directory holding cached content, one file per CID. An
	// empty string disables caching.
	cache string
}

// NewClient returns a client for the HTTP gateway at the base URL gateway,
// such as "https://ipfs.filebase.io". If gateway is empty, the client uses the
// default gateway (see DefaultGateway), resolved the first time it needs to
// download content. Content is cached in $XDG_CACHE_HOME/arbot/ipfs.
func NewClient(gateway string) *Client {
	var cache string
	if dir := xdg.CacheHome(); dir != "" {
		cache = filepath.Join(dir, "arbot", "ipfs")
	}
	return &Client{gateway: gateway, http: http.DefaultClient, cache: cache}
}

// Fetch returns the content addressed by cid. Content is immutable, so it is
// served from the cache when present, and otherwise downloaded from
// {gateway}/ipfs/{cid} and cached. Neither gateways nor the cache are trusted:
// it returns an error unless the content matches cid.
func (c *Client) Fetch(ctx context.Context, cid CID) ([]byte, error) {
	if content, ok := c.cached(cid); ok {
		return content, nil
	}
	content, err := c.download(ctx, cid)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", cid, err)
	}
	// The cache only saves future downloads, so failing to write it does not
	// fail the fetch.
	_ = c.store(cid, content)
	return content, nil
}

// cached returns the cached content for cid, if the cache holds a valid copy.
// A missing or corrupted entry is a cache miss and gets overwritten by the
// next store.
func (c *Client) cached(cid CID) ([]byte, bool) {
	if c.cache == "" {
		return nil, false
	}
	content, err := os.ReadFile(filepath.Join(c.cache, cid.String()))
	if err != nil || ComputeCID(content) != cid {
		return nil, false
	}
	return content, true
}

// store writes content to the cache. It writes to a temporary file and
// renames it into place, so concurrent runs never read a partial entry.
func (c *Client) store(cid CID, content []byte) error {
	if c.cache == "" {
		return nil
	}
	if err := os.MkdirAll(c.cache, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(c.cache, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(c.cache, cid.String()))
}

// download fetches the content for cid from the gateway and verifies it.
func (c *Client) download(ctx context.Context, cid CID) ([]byte, error) {
	gateway := c.gateway
	if gateway == "" {
		var err error
		if gateway, err = DefaultGateway(ctx); err != nil {
			return nil, err
		}
	}
	u, err := url.JoinPath(gateway, "ipfs", cid.String())
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, bytes.TrimSpace(snippet))
	}

	// Read one byte past the limit to tell content that is exactly at the
	// limit apart from content that exceeds it.
	content, err := io.ReadAll(io.LimitReader(resp.Body, MaxContentSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > MaxContentSize {
		return nil, fmt.Errorf("content exceeds %d bytes", MaxContentSize)
	}
	if got := ComputeCID(content); got != cid {
		return nil, fmt.Errorf("gateway returned content with CID %s", got)
	}
	return content, nil
}
