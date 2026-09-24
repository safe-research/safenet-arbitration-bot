// Package ipfs implements the parts of IPFS that Arbot needs to verify content
// fetched from untrusted gateways.
package ipfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"
)

// CID is an IPFS content identifier.
//
// Only CIDv1 with the raw codec and a SHA2-256 multihash is supported. This is
// the format `ipfs add --cid-version=1` uses for content that fits in a single
// block, and the one the Safenet Charter ENS name references (it renders as
// "bafkrei..."). The CID of such content is just the SHA2-256 digest of its
// bytes.
type CID struct {
	digest [sha256.Size]byte
}

// prefix is the binary CID prefix for the supported format: the CID version
// (1), the multicodec (raw, 0x55), the multihash function (sha2-256, 0x12), and
// the digest length (32 bytes).
var prefix = []byte{0x01, 0x55, 0x12, sha256.Size}

// multibase is the multibase prefix of lowercase, unpadded RFC 4648 base32, the
// default string encoding of CIDv1.
const multibase = "b"

var encoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// Compute returns the CID of content stored as a single raw block.
func ComputeCID(content []byte) CID {
	return CID{digest: sha256.Sum256(content)}
}

// ParseCID parses the string form of a CID, such as
// "bafkreih7rr54gpwialfg544kfhmjy5axojvhucyv57ll2sw6u6tzyilgpy".
func ParseCID(s string) (CID, error) {
	encoded, ok := strings.CutPrefix(s, multibase)
	if !ok {
		return CID{}, fmt.Errorf("invalid CID %q: unsupported multibase encoding", s)
	}
	raw, err := encoding.DecodeString(encoded)
	if err != nil {
		return CID{}, fmt.Errorf("invalid CID %q: %w", s, err)
	}
	digest, ok := bytes.CutPrefix(raw, prefix)
	if !ok {
		return CID{}, fmt.Errorf("invalid CID %q: only CIDv1 raw SHA2-256 CIDs are supported", s)
	}
	if len(digest) != sha256.Size {
		return CID{}, fmt.Errorf("invalid CID %q: got a %d-byte digest, want %d", s, len(digest), sha256.Size)
	}

	var cid CID
	copy(cid.digest[:], digest)
	return cid, nil
}

// ParseCIDFromURL parses an IPFS URL of the form "ipfs://<cid>", as used by ENS
// content hash records, such as
// "ipfs://bafkreih7rr54gpwialfg544kfhmjy5axojvhucyv57ll2sw6u6tzyilgpy".
func ParseCIDFromURL(url string) (CID, error) {
	s, ok := strings.CutPrefix(url, "ipfs://")
	if !ok {
		return CID{}, fmt.Errorf("invalid IPFS URL %q: missing ipfs:// scheme", url)
	}
	return ParseCID(s)
}

// String returns the CID in its canonical string form.
func (c CID) String() string {
	return multibase + encoding.EncodeToString(append(bytes.Clone(prefix), c.digest[:]...))
}
