package cli

import (
	"encoding/json"
	"math/big"
	"regexp"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
)

var (
	stateOther = ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000002")
	// stateSingleton is the Safe 1.3.0 singleton.
	stateSingleton = ethrpc.MustParseAddress("0xd9Db270c1B5E3Bd161E8c8503c55cEABeE709552")
)

// storageWord returns an address or number as a storage word.
func storageWord(v any) ethrpc.Hash {
	var h ethrpc.Hash
	switch v := v.(type) {
	case ethrpc.Address:
		copy(h[12:], v[:])
	case int:
		big.NewInt(int64(v)).FillBytes(h[:])
	}
	return h
}

// listSlot returns the storage slot of the entry for key in the mapping of a
// Safe's linked list at slot index.
func listSlot(key ethrpc.Address, index byte) ethrpc.Hash {
	var preimage [64]byte
	copy(preimage[12:32], key[:])
	preimage[63] = index
	return keccak256.Hash(preimage[:])
}

// safeAccount returns a Safe 1.3.0 proxy with the owners, threshold, and nonce,
// and no modules.
func safeAccount(threshold, nonce int, owners ...ethrpc.Address) stateAccount {
	sentinel := ethrpc.Address{19: 1}
	storage := map[ethrpc.Hash]ethrpc.Hash{
		{}:      storageWord(stateSingleton),
		{31: 3}: storageWord(len(owners)),
		{31: 4}: storageWord(threshold),
		{31: 5}: storageWord(nonce),
		// The modules, an empty list.
		listSlot(sentinel, 1): storageWord(sentinel),
	}
	previous := sentinel
	for _, owner := range append(owners, sentinel) {
		storage[listSlot(previous, 2)] = storageWord(owner)
		previous = owner
	}
	return stateAccount{code: stateSafeProxy, storage: storage}
}

// safeJSON is the part of the JSON output of `arbot safe` that the tests check.
type safeJSON struct {
	Safe      ethrpc.Address   `json:"safe"`
	SafeBlock uint64           `json:"safeBlock"`
	Block     *uint64          `json:"block"`
	Owners    []ethrpc.Address `json:"owners"`
	Threshold *big.Int         `json:"threshold"`
	Nonce     *big.Int         `json:"nonce"`
}

func TestSafeAddressAndBlock(t *testing.T) {
	shared, onlySafe, onlyOther := ethrpc.Address{0: 0x0e, 19: 1}, ethrpc.Address{0: 0x0e, 19: 2}, ethrpc.Address{0: 0x0e, 19: 3}
	unknownSingleton := ethrpc.Address{0: 0x5a, 19: 3}
	// The Safe added an owner and raised its threshold between blocks 150 and 200.
	// The other Safe shares an owner with it, and is only deployed after block 150.
	node := &stateNode{blocks: map[uint64]map[ethrpc.Address]stateAccount{
		150: {stateSafe: safeAccount(1, 4, shared)},
		200: {
			stateSafe:  safeAccount(2, 5, onlySafe, shared),
			stateOther: safeAccount(1, 9, shared, onlyOther),
			stateEOA:   {balance: 1, nonce: 3},
			// Not a Safe proxy, though its slot 0 holds a singleton.
			stateToken: {code: ethrpc.Bytes{0x60, 0x00}, storage: map[ethrpc.Hash]ethrpc.Hash{{}: storageWord(stateSingleton)}},
			// A Safe proxy whose singleton isn't one of § 2.1.
			unknownSingleton: {code: stateSafeProxy, storage: map[ethrpc.Hash]ethrpc.Hash{{}: storageWord(ethrpc.Address{19: 0x99})}},
		},
	}}
	config, request := stateFiles(t, node.serve(t))
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		code, stdout, stderr := runArbot(t, append([]string{"-config", config, "safe", "-request-file", request}, args...)...)
		if code != 0 {
			t.Fatalf("safe %v: got status %d, stderr %q", args, code, stderr)
		}
		return stdout
	}
	decodeSafe := func(t *testing.T, out string) safeJSON {
		t.Helper()
		var got safeJSON
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("decoding %q: %v", out, err)
		}
		return got
	}

	t.Run("plain", func(t *testing.T) {
		// The output without flags keeps its shape, with the settings at its top level.
		var got map[string]json.RawMessage
		json.Unmarshal([]byte(run(t, "-json")), &got)
		for _, key := range []string{"safe", "chainId", "safeBlock", "singleton", "owners", "ownerCount", "threshold", "nonce", "modules", "fallbackHandler", "guard", "moduleGuard"} {
			if _, ok := got[key]; !ok {
				t.Errorf("safe -json: output has no %q", key)
			}
		}
		if len(got) != 12 || string(got["threshold"]) != "2" {
			t.Errorf("safe -json: got %v, want only the settings, with threshold 2", got)
		}
	})

	t.Run("address", func(t *testing.T) {
		got := decodeSafe(t, run(t, "-json", "-address", stateOther.String()))
		if got.Safe != stateOther || got.SafeBlock != stateSafeBlock || got.Block != nil || got.Threshold.Int64() != 1 ||
			len(got.Owners) != 2 || got.Owners[0] != shared || got.Owners[1] != onlyOther {
			t.Errorf("safe -address: got %+v, want the other Safe's settings at the Safe block", got)
		}
		text := run(t, "-address", stateOther.String())
		for _, want := range []string{
			`(?m)^Safe ` + stateOther.String() + ` on chain 1, as of block 200$`,
			`(?m)^  Threshold         1 of 2 owners$`,
			`(?m)^  ` + onlyOther.String() + `$`,
		} {
			if !regexp.MustCompile(want).MatchString(text) {
				t.Errorf("safe -address: output has no line matching %q:\n%s", want, text)
			}
		}
	})

	t.Run("block", func(t *testing.T) {
		got := decodeSafe(t, run(t, "-json", "-block", "150"))
		if got.Safe != stateSafe || got.SafeBlock != stateSafeBlock || got.Block == nil || *got.Block != 150 ||
			got.Threshold.Int64() != 1 || got.Nonce.Int64() != 4 || len(got.Owners) != 1 {
			t.Errorf("safe -block 150: got %+v, want the Safe's settings at block 150", got)
		}
		if text := run(t, "-block", "150"); !strings.HasPrefix(text, "Safe "+stateSafe.String()+" on chain 1, as of block 150\n") {
			t.Errorf("safe -block 150: got %q, want the settings as of block 150", text)
		}
		// The Safe block itself can be read.
		if got := decodeSafe(t, run(t, "-json", "-block", "200")); got.Block == nil || *got.Block != 200 || got.Threshold.Int64() != 2 {
			t.Errorf("safe -block 200: got %+v, want the Safe's settings at block 200", got)
		}
	})

	t.Run("not a Safe", func(t *testing.T) {
		for _, test := range []struct {
			args   []string
			reason string
		}{
			{[]string{"-address", stateEOA.String()}, "isn't a Safe of a § 2.1 version at block 200: it has no code"},
			{[]string{"-address", stateToken.String()}, "isn't a Safe of a § 2.1 version at block 200: its code isn't that of a SafeProxy"},
			{[]string{"-address", unknownSingleton.String()}, "isn't a Safe of a § 2.1 version at block 200: its singleton isn't"},
			// The other Safe isn't deployed yet at block 150.
			{[]string{"-address", stateOther.String(), "-block", "150"}, "isn't a Safe of a § 2.1 version at block 150: it has no code"},
		} {
			code, stdout, stderr := runArbot(t, append([]string{"-config", config, "safe", "-json", "-request-file", request}, test.args...)...)
			if code != 1 || stdout != "" || !strings.Contains(stderr, test.reason) || !strings.Contains(stderr, "arbot account shows what it is") {
				t.Errorf("safe %v: got status %d, stdout %q, stderr %q; want 1, no output, and %q", test.args, code, stdout, stderr, test.reason)
			}
		}
	})

	t.Run("errors", func(t *testing.T) {
		for _, test := range []struct {
			args   []string
			stderr string
		}{
			{[]string{"-block", "201"}, "-block 201 is after the request's Safe block 200"},
			{[]string{"-address", stateOther.String(), "-block", "201"}, "-block 201 is after the request's Safe block 200"},
			{[]string{"-address", "0x12"}, `-address "0x12"`},
		} {
			code, stdout, stderr := runArbot(t, append([]string{"-config", config, "safe", "-request-file", request}, test.args...)...)
			if code != 2 || stdout != "" || !strings.Contains(stderr, test.stderr) {
				t.Errorf("safe %v: got status %d, stdout %q, stderr %q; want 2, no output, and stderr containing %q",
					test.args, code, stdout, stderr, test.stderr)
			}
		}
	})
}

func TestSafeNotASafe(t *testing.T) {
	// Without flags, the storage of an account that isn't a Safe is read as it is,
	// as zeros for an account without code.
	node := &stateNode{blocks: map[uint64]map[ethrpc.Address]stateAccount{stateSafeBlock: {}}}
	config, request := stateFiles(t, node.serve(t))
	code, stdout, stderr := runArbot(t, "-config", config, "safe", "-json", "-request-file", request)
	zero := `"0x0000000000000000000000000000000000000000"`
	want := `{
  "safe": "0x5afe000000000000000000000000000000000001",
  "chainId": 1,
  "safeBlock": 200,
  "singleton": ` + zero + `,
  "owners": [],
  "ownerCount": 0,
  "threshold": 0,
  "nonce": 0,
  "modules": [],
  "fallbackHandler": ` + zero + `,
  "guard": ` + zero + `,
  "moduleGuard": ` + zero + `
}
`
	if code != 0 || stdout != want || stderr != "" {
		t.Errorf("safe of an account without code: got status %d, stdout %q, stderr %q; want 0 and\n%s", code, stdout, stderr, want)
	}
}

func TestSafeTransientError(t *testing.T) {
	// A node that fails to answer says nothing about the account, so the command
	// fails rather than reporting an account without settings.
	node := &stateNode{overloaded: true}
	config, request := stateFiles(t, node.serve(t))
	for _, args := range [][]string{
		{"safe", "-request-file", request},
		{"safe", "-request-file", request, "-address", stateOther.String()},
		{"safe", "-request-file", request, "-block", "150"},
	} {
		code, stdout, stderr := runArbot(t, append([]string{"-config", config}, args...)...)
		if code != 1 || stdout != "" || !strings.Contains(stderr, "run the command again later") {
			t.Errorf("%v: got status %d, stdout %q, stderr %q; want 1 and a hint to run it again", args, code, stdout, stderr)
		}
	}
}
