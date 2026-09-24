package ethrpc

import (
	"bytes"
	"testing"
)

func TestAddress(t *testing.T) {
	const s = "0x223624cbf099e5a8f8cd5af22afa424a1d1acee9"
	a, err := ParseAddress("0x223624cBF099e5a8f8cD5aF22aFa424a1d1acEE9")
	if err != nil {
		t.Fatalf("ParseAddress: %v", err)
	}
	if a.String() != s {
		t.Errorf("String: got %s, want %s", a, s)
	}

	for _, invalid := range []string{
		"223624cbf099e5a8f8cd5af22afa424a1d1acee9",     // no prefix
		"0x223624cbf099e5a8f8cd5af22afa424a1d1ace",     // too short
		"0x223624cbf099e5a8f8cd5af22afa424a1d1acee900", // too long
		"0x223624cbf099e5a8f8cd5af22afa424a1d1aceeg",   // not hex
	} {
		if _, err := ParseAddress(invalid); err == nil {
			t.Errorf("ParseAddress(%q): expected an error", invalid)
		}
	}
}

func TestHash(t *testing.T) {
	const s = "0x86e8b85731e4787f033d85108356db1e068dea243be32d422e6dc5681ff49cc1"
	h, err := ParseHash("0x86E8B85731E4787F033D85108356DB1E068DEA243BE32D422E6DC5681FF49CC1")
	if err != nil {
		t.Fatalf("ParseHash: %v", err)
	}
	if h.String() != s {
		t.Errorf("String: got %s, want %s", h, s)
	}

	for _, invalid := range []string{
		s[2:],        // no prefix
		s[:64],       // too short
		s + "00",     // too long
		s[:65] + "g", // not hex
	} {
		if _, err := ParseHash(invalid); err == nil {
			t.Errorf("ParseHash(%q): expected an error", invalid)
		}
	}
}

func TestBytes(t *testing.T) {
	for _, tc := range []struct {
		text string
		data []byte
	}{
		{"0x", []byte{}},
		{"0x00ff", []byte{0x00, 0xff}},
	} {
		var b Bytes
		if err := b.UnmarshalText([]byte(tc.text)); err != nil {
			t.Fatalf("UnmarshalText(%q): %v", tc.text, err)
		}
		if !bytes.Equal(b, tc.data) {
			t.Errorf("UnmarshalText(%q): got %x, want %x", tc.text, b, tc.data)
		}
		if b.String() != tc.text {
			t.Errorf("String: got %s, want %s", b, tc.text)
		}
	}

	for _, invalid := range []string{"", "00", "0x0", "0xzz"} {
		var b Bytes
		if err := b.UnmarshalText([]byte(invalid)); err == nil {
			t.Errorf("UnmarshalText(%q): expected an error", invalid)
		}
	}
}

func TestBlockNumberText(t *testing.T) {
	for _, tc := range []struct {
		text   string
		number BlockNumber
	}{
		{"0x0", 0},
		{"0x10", 16},
		{"0xffffffffffffffff", 1<<64 - 1},
	} {
		text, _ := tc.number.MarshalText()
		if string(text) != tc.text {
			t.Errorf("MarshalText(%d): got %s, want %s", tc.number, text, tc.text)
		}
		var n BlockNumber
		if err := n.UnmarshalText([]byte(tc.text)); err != nil {
			t.Fatalf("UnmarshalText(%q): %v", tc.text, err)
		}
		if n != tc.number {
			t.Errorf("UnmarshalText(%q): got %d, want %d", tc.text, n, tc.number)
		}
	}

	for _, invalid := range []string{"", "0x", "10", "0xg", "0x10000000000000000"} {
		var n BlockNumber
		if err := n.UnmarshalText([]byte(invalid)); err == nil {
			t.Errorf("UnmarshalText(%q): expected an error", invalid)
		}
	}
}
