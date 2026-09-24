package ethrpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Address is a 20-byte Ethereum account address.
type Address [20]byte

// ParseAddress parses a 0x-prefixed hex address. The EIP-55 checksum of
// mixed-case addresses is not verified.
func ParseAddress(s string) (Address, error) {
	var a Address
	err := a.UnmarshalText([]byte(s))
	return a, err
}

// String returns the address as lowercase 0x-prefixed hex.
func (a Address) String() string {
	return encodeHex(a[:])
}

func (a Address) MarshalText() ([]byte, error) {
	return []byte(a.String()), nil
}

func (a *Address) UnmarshalText(text []byte) error {
	b, err := decodeHex(text)
	if err != nil {
		return fmt.Errorf("invalid address: %w", err)
	}
	if len(b) != len(a) {
		return fmt.Errorf("invalid address: got %d bytes, want %d", len(b), len(a))
	}
	copy(a[:], b)
	return nil
}

// Bytes is arbitrary binary data, encoded as 0x-prefixed hex.
type Bytes []byte

// String returns the data as lowercase 0x-prefixed hex.
func (b Bytes) String() string {
	return encodeHex(b)
}

func (b Bytes) MarshalText() ([]byte, error) {
	return []byte(b.String()), nil
}

func (b *Bytes) UnmarshalText(text []byte) error {
	decoded, err := decodeHex(text)
	if err != nil {
		return fmt.Errorf("invalid bytes: %w", err)
	}
	*b = decoded
	return nil
}

// Quantity is an unsigned integer, encoded as a hex quantity.
type Quantity uint64

func (q Quantity) MarshalText() ([]byte, error) {
	return []byte("0x" + strconv.FormatUint(uint64(q), 16)), nil
}

func (q *Quantity) UnmarshalText(text []byte) error {
	digits, ok := strings.CutPrefix(string(text), "0x")
	if !ok || digits == "" {
		return fmt.Errorf("invalid quantity %q: not 0x-prefixed hex", text)
	}
	value, err := strconv.ParseUint(digits, 16, 64)
	if err != nil {
		return fmt.Errorf("invalid quantity %q: %w", text, err)
	}
	*q = Quantity(value)
	return nil
}

// BlockNumber is a block number, encoded as a hex quantity.
type BlockNumber uint64

func (n BlockNumber) MarshalText() ([]byte, error) {
	return Quantity(n).MarshalText()
}

func (n *BlockNumber) UnmarshalText(text []byte) error {
	return (*Quantity)(n).UnmarshalText(text)
}

// CallRequest is the message call object passed to eth_call.
type CallRequest struct {
	// From is the sender of the call. It is omitted if zero, which nodes
	// treat as the zero address.
	From Address `json:"from,omitzero"`
	To   Address `json:"to"`
	Data Bytes   `json:"data,omitempty"`
}

// Error is an error returned by the JSON-RPC server.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	// Data holds additional error information, such as the revert data of a
	// failed eth_call.
	Data json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

func encodeHex(b []byte) string {
	return "0x" + hex.EncodeToString(b)
}

func decodeHex(text []byte) ([]byte, error) {
	digits, ok := strings.CutPrefix(string(text), "0x")
	if !ok {
		return nil, errors.New("missing 0x prefix")
	}
	return hex.DecodeString(digits)
}
