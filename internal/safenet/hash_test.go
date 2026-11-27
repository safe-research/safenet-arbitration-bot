package safenet

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

func mustParseHash(s string) ethrpc.Hash {
	hash, err := ethrpc.ParseHash(s)
	if err != nil {
		panic(err)
	}
	return hash
}

func TestSafeTransactionHash(t *testing.T) {
	// The vector from the Safenet explorer's hashing tests.
	tx := SafeTransaction{
		ChainID:   big.NewInt(100),
		Safe:      ethrpc.MustParseAddress("0x779720809250AF7931935a192FCD007479C41299"),
		To:        ethrpc.MustParseAddress("0x2dC63c83040669F0aDBa5F832F713152bA862c97"),
		Value:     big.NewInt(100_000_000_000_000_000),
		Operation: OperationCall,
		SafeTxGas: new(big.Int),
		BaseGas:   new(big.Int),
		GasPrice:  new(big.Int),
		Nonce:     big.NewInt(1),
	}
	want := mustParseHash("0xd6a2395bd7bd650df56610d38760d1b4b8073d37db35090ce3c855ef659c1b81")
	if got := tx.Hash(); got != want {
		t.Errorf("Hash: got %s, want %s", got, want)
	}
}

func TestSafeTransactionLegacyHash(t *testing.T) {
	// The vector of TestSafeTransactionHash, and the transaction at nonce 7 of a
	// Safe on the 1.1.1 singleton on Ethereum Mainnet, which logged its execution
	// in block 10,659,059 with the hash in the older domain. The hashes are from
	// cast.
	explorer := SafeTransaction{
		ChainID:   big.NewInt(100),
		Safe:      ethrpc.MustParseAddress("0x779720809250AF7931935a192FCD007479C41299"),
		To:        ethrpc.MustParseAddress("0x2dC63c83040669F0aDBa5F832F713152bA862c97"),
		Value:     big.NewInt(100_000_000_000_000_000),
		Operation: OperationCall,
		SafeTxGas: new(big.Int),
		BaseGas:   new(big.Int),
		GasPrice:  new(big.Int),
		Nonce:     big.NewInt(1),
	}
	data, err := hex.DecodeString("8d80ff0a" +
		"0000000000000000000000000000000000000000000000000000000000000020" +
		"0000000000000000000000000000000000000000000000000000000000000099" +
		"003a22df48d84957f907e67f4313e3d43179040d6e0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000044eb9253c0000000000000000000000000ba100000" +
		"625a3754423978a60c9317c58a424e3d00000000000000000000000000000000000000000000003051a9f917d9b977e800000000000000")
	if err != nil {
		t.Fatal(err)
	}
	legacy := SafeTransaction{
		ChainID:   big.NewInt(1),
		Safe:      ethrpc.MustParseAddress("0xFEB4acf3df3cDEA7399794D0869ef76A6EfAff52"),
		To:        ethrpc.MustParseAddress("0x8D29bE29923b68abfDD21e541b9374737B49cdAD"),
		Value:     new(big.Int),
		Data:      data,
		Operation: OperationDelegateCall,
		SafeTxGas: big.NewInt(66590),
		BaseGas:   new(big.Int),
		GasPrice:  new(big.Int),
		Nonce:     big.NewInt(7),
	}
	for _, test := range []struct {
		name         string
		tx           SafeTransaction
		hash, legacy ethrpc.Hash
	}{
		{
			"explorer vector", explorer,
			mustParseHash("0xd6a2395bd7bd650df56610d38760d1b4b8073d37db35090ce3c855ef659c1b81"),
			mustParseHash("0x0aea230e0019b59a55a23eea5be571a8f7f5ad9b2b50333b6afa382df210e8d5"),
		},
		{
			"Safe 1.1.1", legacy,
			mustParseHash("0x088b186add47ab5ba378ce0d2fbb37a0e0fba7c9fb700afe38b923cc35f1bd06"),
			mustParseHash("0xd206604afc8f8998c7e92634c8902686513d6a3547f15de7d658990f66dfab31"),
		},
	} {
		if got := test.tx.Hash(); got != test.hash {
			t.Errorf("%s: Hash: got %s, want %s", test.name, got, test.hash)
		}
		if got := test.tx.LegacyHash(); got != test.legacy {
			t.Errorf("%s: LegacyHash: got %s, want %s", test.name, got, test.legacy)
		}
		// The older domain has no chain ID.
		other := test.tx
		other.ChainID = big.NewInt(42161)
		if got := other.LegacyHash(); got != test.legacy || other.Hash() == test.hash {
			t.Errorf("%s on another chain: LegacyHash %s, Hash %s; want %s and another hash than %s", test.name, got, other.Hash(), test.legacy, test.hash)
		}
	}
}

func TestRequestID(t *testing.T) {
	// A request on the deployment in testdata/gnosis.json.
	got := requestID(
		ethrpc.Gnosis,
		fixtureConsensus,
		161285,
		fixtureOracle,
		nil,
		mustParseHash("0xea6f04a866f107949f795b60dbcbe9563ab67ee232bbb14c7b85644dc9fce221"),
	)
	want := mustParseHash("0x062be5de4a4ca7123b0894a48807d09ca0e445c282e48dc3601e141bd32b48cb")
	if got != want {
		t.Errorf("requestID: got %s, want %s", got, want)
	}
}
