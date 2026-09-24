package ens

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// charter is the Safenet Charter ENS name, with its onchain records as of
// September 2026.
const (
	charter            = "charter.safenet-gov.eth"
	charterNode        = "8eb28b73557db9dbf11eb3284826984b167db85cbc1ae64e80ba91f9167d07ea"
	charterResolver    = "f29100983e058b709f3d539b0c765937b804ac15"
	charterContenthash = "e30101551220ff8c7bc33ec802ca6ef38a29d89c7417726a7a0b15efd6bd4adea7a79c21667e"
	charterURL         = "ipfs://bafkreih7rr54gpwialfg544kfhmjy5axojvhucyv57ll2sw6u6tzyilgpy"
)

func TestNamehash(t *testing.T) {
	for name, want := range map[string]string{
		"":                "0000000000000000000000000000000000000000000000000000000000000000",
		"eth":             "93cdeb708b7545dc668eb9280176169d1c33cfd8ed6f04690a0bcc88a93fc4ae",
		"safenet-gov.eth": "54a2f6838fb9f5f0eee9bd970632238a2adb9d4d213e1d3b04117a9a2de3b142",
		charter:           charterNode,
	} {
		node := Namehash(name)
		if got := hex.EncodeToString(node[:]); got != want {
			t.Errorf("Namehash(%q): got %s, want %s", name, got, want)
		}
	}
}

func TestDecodeContentHash(t *testing.T) {
	contenthash, _ := hex.DecodeString(charterContenthash)
	url, err := decodeContentHash(contenthash)
	if err != nil {
		t.Fatalf("decodeContentHash: %v", err)
	}
	if url != charterURL {
		t.Errorf("decodeContentHash: got %s, want %s", url, charterURL)
	}

	for name, invalid := range map[string]string{
		"swarm":  "e40101fa011b20d1de9994b4d039f6548d191eb26786769f580809256b4685ef316805265ea162",
		"CIDv0":  "e3011220ff8c7bc33ec802ca6ef38a29d89c7417726a7a0b15efd6bd4adea7a79c21667e",
		"no CID": "e301",
	} {
		contenthash, _ := hex.DecodeString(invalid)
		if _, err := decodeContentHash(contenthash); err == nil {
			t.Errorf("decodeContentHash(%s): expected an error", name)
		}
	}
}

// word ABI-encodes a uint64 as a 32-byte word.
func word(n uint64) string {
	return fmt.Sprintf("%064x", n)
}

// abiBytes ABI-encodes a bytes return value.
func abiBytes(data string) string {
	padded := data + strings.Repeat("0", (64-len(data)%64)%64)
	return word(32) + word(uint64(len(data)/2)) + padded
}

// mainnet starts an Ethereum Mainnet node whose eth_call results are looked
// up by the hex call data (without 0x) and the target address, and returns
// an ENS client for it.
func mainnet(t *testing.T, calls map[string]string) *Client {
	t.Helper()
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
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "eth_chainId":
			res["result"] = "0x1"
		case "eth_call":
			var call struct {
				To   string `json:"to"`
				Data string `json:"data"`
			}
			json.Unmarshal(req.Params[0], &call)
			key := strings.TrimPrefix(call.To, "0x") + ":" + strings.TrimPrefix(call.Data, "0x")
			if result, ok := calls[key]; ok {
				res["result"] = "0x" + result
			} else {
				res["error"] = map[string]any{"code": 3, "message": "execution reverted"}
			}
		default:
			res["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(server.Close)

	eth, err := ethrpc.NewClient(t.Context(), Mainnet, server.URL)
	if err != nil {
		t.Fatalf("ethrpc.NewClient: %v", err)
	}
	client, err := NewClient(eth)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

const registry = "00000000000c2e074ec69a0dfb2997ba6c7d2e1e"

func TestResolve(t *testing.T) {
	client := mainnet(t, map[string]string{
		registry + ":0178b8bf" + charterNode:        strings.Repeat("0", 24) + charterResolver,
		charterResolver + ":bc1c58d1" + charterNode: abiBytes(charterContenthash),
	})
	url, err := client.Resolve(t.Context(), charter, 24_000_000)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if url != charterURL {
		t.Errorf("Resolve: got %s, want %s", url, charterURL)
	}
}

func TestResolveWithoutResolver(t *testing.T) {
	client := mainnet(t, map[string]string{
		registry + ":0178b8bf" + charterNode: word(0),
	})
	if _, err := client.Resolve(t.Context(), charter, 24_000_000); err == nil {
		t.Fatal("Resolve: expected an error")
	}
}

func TestResolveWithoutContentHash(t *testing.T) {
	client := mainnet(t, map[string]string{
		registry + ":0178b8bf" + charterNode:        strings.Repeat("0", 24) + charterResolver,
		charterResolver + ":bc1c58d1" + charterNode: abiBytes(""),
	})
	if _, err := client.Resolve(t.Context(), charter, 24_000_000); err == nil {
		t.Fatal("Resolve: expected an error")
	}
}

func TestResolveRejectsInvalidNames(t *testing.T) {
	client := mainnet(t, nil)
	for _, name := range []string{"", ".eth", "charter..eth", "charter.eth."} {
		if _, err := client.Resolve(t.Context(), name, 24_000_000); err == nil {
			t.Errorf("Resolve(%q): expected an error", name)
		}
	}
}

func TestDecodeBytes(t *testing.T) {
	for name, tc := range map[string]struct {
		result string
		want   string
		ok     bool
	}{
		"empty":            {abiBytes(""), "", true},
		"content hash":     {abiBytes(charterContenthash), charterContenthash, true},
		"truncated":        {abiBytes(charterContenthash)[:128+20], "", false},
		"offset too large": {word(1<<20) + word(0), "", false},
		"length too large": {word(32) + word(1<<20), "", false},
		"too short":        {"00", "", false},
	} {
		result, _ := hex.DecodeString(tc.result)
		got, err := decodeBytes(result)
		if (err == nil) != tc.ok {
			t.Errorf("%s: got error %v, want ok=%v", name, err, tc.ok)
			continue
		}
		if tc.ok && hex.EncodeToString(got) != tc.want {
			t.Errorf("%s: got %x, want %s", name, got, tc.want)
		}
	}
}
