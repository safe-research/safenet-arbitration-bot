package cli

import (
	"math/big"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

func TestFormatUnits(t *testing.T) {
	tests := []struct {
		amount   string
		decimals uint8
		want     string
	}{
		{"400000000000000000", 18, "0.4"},
		{"800000000000000000000", 18, "800"},
		{"8000000000000000000", 18, "8"},
		{"1", 18, "0.000000000000000001"},
		{"0", 18, "0"},
		{"1234567", 6, "1.234567"},
		{"1230000", 6, "1.23"},
		{"-1500000", 6, "-1.5"},
		{"42", 0, "42"},
	}
	for _, test := range tests {
		amount, _ := new(big.Int).SetString(test.amount, 10)
		if got := formatUnits(amount, test.decimals); got != test.want {
			t.Errorf("formatUnits(%s, %d) = %q, want %q", test.amount, test.decimals, got, test.want)
		}
	}
}

func TestFormatAmount(t *testing.T) {
	amount := big.NewInt(400_000)
	tests := []struct {
		symbol string
		want   string
	}{
		{"MTK", "0.4 MTK"},
		{"", `0.4 ""`},
		{"A B", `0.4 "A B"`},
		{"\x1b[31mRED", `0.4 "\x1b[31mRED"`},
	}
	for _, test := range tests {
		if got := formatAmount(amount, safenet.Token{Symbol: test.symbol, Decimals: 6}); got != test.want {
			t.Errorf("formatAmount with symbol %q = %q, want %q", test.symbol, got, test.want)
		}
	}
}
