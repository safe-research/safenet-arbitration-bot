// Package keccak256 implements the Keccak-256 hash function used by Ethereum.
//
// Keccak-256 is the original Keccak submission to the SHA-3 competition, and
// differs from the final SHA3-256 standard (crypto/sha3) in its padding, so
// the two produce different hashes.
package keccak256

import (
	"hash"

	"golang.org/x/crypto/sha3"
)

// Size is the size of a Keccak-256 hash in bytes.
const Size = 32

// New returns a new hash.Hash computing Keccak-256.
func New() hash.Hash {
	return sha3.NewLegacyKeccak256()
}

// Hash returns the Keccak-256 hash of the concatenation of data.
func Hash(data ...[]byte) [Size]byte {
	h := New()
	for _, d := range data {
		h.Write(d)
	}
	return [Size]byte(h.Sum(nil))
}
