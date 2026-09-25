package checks

import (
	"math/big"
	"testing"
)

func TestOffNetwork(t *testing.T) {
	tests := []struct {
		chainID *big.Int
		want    bool
	}{
		{big.NewInt(1), false},
		{big.NewInt(42161), false},
		{big.NewInt(100), false},
		{big.NewInt(0), true},
		{big.NewInt(10), true},
		{new(big.Int).Lsh(big.NewInt(1), 64), true},
	}
	for _, test := range tests {
		got, err := offNetwork.fn(t.Context(), nil, &safeID{chainID: test.chainID}, call{})
		if got != test.want || err != nil {
			t.Errorf("offNetwork for chain %d = %t, %v; want %t", test.chainID, got, err, test.want)
		}
	}
}
