package keccak256

import (
	"encoding/hex"
	"testing"
)

func TestHash(t *testing.T) {
	for _, tc := range []struct {
		data []string
		want string
	}{
		{nil, "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"},
		{[]string{""}, "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"},
		{[]string{"abc"}, "4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45"},
		// Several arguments hash as their concatenation.
		{[]string{"a", "", "bc"}, "4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45"},
		// The first 4 bytes are the ABI function selector.
		{[]string{"resolver(bytes32)"}, "0178b8bf"},
	} {
		var data [][]byte
		for _, d := range tc.data {
			data = append(data, []byte(d))
		}
		hash := Hash(data...)
		if got := hex.EncodeToString(hash[:len(tc.want)/2]); got != tc.want {
			t.Errorf("Hash(%q): got %s, want %s", tc.data, got, tc.want)
		}
	}
}

func TestNew(t *testing.T) {
	h := New()
	h.Write([]byte("abc"))
	if got, want := hex.EncodeToString(h.Sum(nil)), "4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45"; got != want {
		t.Errorf("New: got %s, want %s", got, want)
	}
	if h.Size() != Size {
		t.Errorf("Size: got %d, want %d", h.Size(), Size)
	}
}
