// Package ens resolves Ethereum Name Service records onchain.
package ens

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
)

// Mainnet is the chain ID of Ethereum Mainnet, where the ENS registry lives.
const Mainnet = 1

// Registry is the address of the ENS registry.
var Registry = mustParseAddress("0x00000000000C2E074eC69A0dFb2997BA6C7d2e1e")

// Function selectors of the registry and resolver methods that Resolve calls.
var (
	resolverSelector    = []byte{0x01, 0x78, 0xb8, 0xbf} // resolver(bytes32)
	contenthashSelector = []byte{0xbc, 0x1c, 0x58, 0xd1} // contenthash(bytes32)
)

// Client resolves ENS names using an Ethereum Mainnet node.
type Client struct {
	eth *ethrpc.Client
}

// NewClient returns a client that resolves names using eth, which must be a
// client for Ethereum Mainnet.
func NewClient(eth *ethrpc.Client) (*Client, error) {
	if eth.ChainID() != Mainnet {
		return nil, fmt.Errorf("ENS requires an Ethereum Mainnet node, got chain %d", eth.ChainID())
	}
	return &Client{eth: eth}, nil
}

// Resolve returns the content hash record (EIP-1577) of name as of block, as
// a URL such as "ipfs://bafkreih7rr54gpwialfg544kfhmjy5axojvhucyv57ll2sw6u6tzyilgpy".
//
// The name must already be normalized (ENSIP-15), for example lowercase. Only
// names with an onchain resolver set in the registry are supported, not
// wildcard (ENSIP-10) or offchain (CCIP-Read) resolution.
func (c *Client) Resolve(ctx context.Context, name string, block ethrpc.BlockNumber) (string, error) {
	if name == "" || slices.Contains(strings.Split(name, "."), "") {
		return "", fmt.Errorf("invalid ENS name %q", name)
	}
	node := Namehash(name)

	result, err := c.eth.Call(ctx, ethrpc.CallRequest{To: Registry, Data: calldata(resolverSelector, node)}, block)
	if err != nil {
		return "", fmt.Errorf("resolving %s: getting resolver: %w", name, err)
	}
	resolver, err := decodeAddress(result)
	if err != nil {
		return "", fmt.Errorf("resolving %s: getting resolver: %w", name, err)
	}
	if resolver == (ethrpc.Address{}) {
		return "", fmt.Errorf("resolving %s: name has no resolver", name)
	}

	result, err = c.eth.Call(ctx, ethrpc.CallRequest{To: resolver, Data: calldata(contenthashSelector, node)}, block)
	if err != nil {
		return "", fmt.Errorf("resolving %s: getting content hash: %w", name, err)
	}
	contenthash, err := decodeBytes(result)
	if err != nil {
		return "", fmt.Errorf("resolving %s: getting content hash: %w", name, err)
	}
	if len(contenthash) == 0 {
		return "", fmt.Errorf("resolving %s: name has no content hash record", name)
	}
	url, err := decodeContentHash(contenthash)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", name, err)
	}
	return url, nil
}

// Namehash returns the ENS node of name (ENSIP-1). The name must already be
// normalized.
func Namehash(name string) [32]byte {
	var node [32]byte
	if name == "" {
		return node
	}
	labels := strings.Split(name, ".")
	for _, label := range slices.Backward(labels) {
		labelHash := keccak256.Hash([]byte(label))
		node = keccak256.Hash(node[:], labelHash[:])
	}
	return node
}

// calldata ABI-encodes a call to a function taking a single bytes32 argument.
func calldata(selector []byte, node [32]byte) ethrpc.Bytes {
	return append(bytes.Clone(selector), node[:]...)
}

// decodeAddress decodes an ABI-encoded address return value.
func decodeAddress(result []byte) (ethrpc.Address, error) {
	if len(result) != 32 || !isZero(result[:12]) {
		return ethrpc.Address{}, fmt.Errorf("invalid address return value 0x%x", result)
	}
	return ethrpc.Address(result[12:]), nil
}

// decodeBytes decodes an ABI-encoded bytes return value.
func decodeBytes(result []byte) ([]byte, error) {
	offset, ok := decodeLength(result, 0)
	if !ok || offset > uint64(len(result)) {
		return nil, errors.New("invalid bytes return value: bad offset")
	}
	length, ok := decodeLength(result, offset)
	if !ok || length > uint64(len(result))-offset-32 {
		return nil, errors.New("invalid bytes return value: bad length")
	}
	start := offset + 32
	return result[start : start+length], nil
}

// decodeLength decodes the ABI-encoded integer at offset, which must fit in a
// uint64.
func decodeLength(data []byte, offset uint64) (uint64, bool) {
	if offset > uint64(len(data)) || uint64(len(data))-offset < 32 {
		return 0, false
	}
	word := data[offset : offset+32]
	if !isZero(word[:24]) {
		return 0, false
	}
	return binary.BigEndian.Uint64(word[24:]), true
}

func isZero(b []byte) bool {
	return !slices.ContainsFunc(b, func(x byte) bool { return x != 0 })
}

// ipfsNamespace is the multicodec of the IPFS namespace (0xe3), as the varint
// that prefixes IPFS content hashes.
var ipfsNamespace = []byte{0xe3, 0x01}

// base32Lower is lowercase, unpadded RFC 4648 base32, the default string
// encoding of CIDv1 (multibase prefix "b").
var base32Lower = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// decodeContentHash decodes an EIP-1577 content hash into a URL. Only IPFS
// content hashes of CIDv1 CIDs are supported.
func decodeContentHash(contenthash []byte) (string, error) {
	cid, ok := bytes.CutPrefix(contenthash, ipfsNamespace)
	if !ok {
		return "", fmt.Errorf("unsupported content hash 0x%x: not an IPFS content hash", contenthash)
	}
	if len(cid) == 0 || cid[0] != 0x01 {
		return "", fmt.Errorf("unsupported content hash 0x%x: not a CIDv1", contenthash)
	}
	return "ipfs://b" + base32Lower.EncodeToString(cid), nil
}

func mustParseAddress(s string) ethrpc.Address {
	address, err := ethrpc.ParseAddress(s)
	if err != nil {
		panic(err)
	}
	return address
}
