package multisend

import (
	"bytes"
	"math/big"
	"slices"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// equal reports whether a and b are the same transactions.
func equal(a, b []Transaction) bool {
	return slices.EqualFunc(a, b, func(x, y Transaction) bool {
		return x.Operation == y.Operation && x.To == y.To && x.Value.Cmp(y.Value) == 0 && bytes.Equal(x.Data, y.Data) &&
			x.DataLength == y.DataLength
	})
}

// pack packs transactions as a batch.
func pack(transactions ...Transaction) []byte {
	var packed []byte
	for _, tx := range transactions {
		value := solabi.Uint(tx.Value)
		length := solabi.Uint64(uint64(len(tx.Data)))
		packed = append(packed, byte(tx.Operation))
		packed = append(packed, tx.To[:]...)
		packed = append(packed, value[:]...)
		packed = append(packed, length[:]...)
		packed = append(packed, tx.Data...)
	}
	return packed
}

func TestDecode(t *testing.T) {
	transfer := Transaction{safenet.OperationCall, ethrpc.Address{19: 0x70}, big.NewInt(1), ethrpc.Bytes{}, 0}
	library := Transaction{safenet.OperationDelegateCall, ethrpc.Address{19: 0x71}, new(big.Int), ethrpc.Bytes{0x12, 0x34}, 2}
	// withDataLength sets the data length of the first of packed transactions.
	withDataLength := func(packed []byte, length *big.Int) []byte {
		word := solabi.Uint(length)
		copy(packed[53:85], word[:])
		return packed
	}
	unknown := transfer
	unknown.Operation = 2
	// truncated ends partway through its value, which is read as if it were
	// followed by zeros.
	truncated := append(append([]byte{0}, bytes.Repeat([]byte{0x11}, 20)...), bytes.Repeat([]byte{0x22}, 19)...)
	truncatedValue := new(big.Int).SetBytes(append(bytes.Repeat([]byte{0x22}, 19), make([]byte, 13)...))

	tests := []struct {
		name  string
		batch []byte
		want  []Transaction
		ok    bool
	}{
		{"empty", nil, nil, true},
		{"transactions", pack(transfer, library), []Transaction{transfer, library}, true},
		{"only ignored trailing bytes", make([]byte, 32), nil, true},
		{"ignored trailing bytes", append(pack(transfer), make([]byte, 32)...), []Transaction{transfer}, true},
		{
			"truncated transaction",
			append(pack(transfer), truncated...),
			[]Transaction{transfer, {safenet.OperationCall, ethrpc.Address(truncated[1:21]), truncatedValue, ethrpc.Bytes{}, 0}},
			true,
		},
		{
			"truncated data",
			pack(library)[:86],
			[]Transaction{{library.Operation, library.To, library.Value, ethrpc.Bytes{0x12}, 2}},
			true,
		},
		{
			"largest data length",
			withDataLength(pack(transfer), big.NewInt(MaxDataLength)),
			[]Transaction{{transfer.Operation, transfer.To, transfer.Value, ethrpc.Bytes{}, MaxDataLength}},
			true,
		},
		{"data length too large", withDataLength(pack(transfer), big.NewInt(MaxDataLength+1)), nil, false},
		{"data length over 64 bits", withDataLength(pack(transfer), new(big.Int).Lsh(big.NewInt(1), 64)), nil, false},
		{"data length of 256 bits", withDataLength(pack(transfer), new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))), nil, false},
		{"unknown operation", pack(transfer, unknown), nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := Decode(test.batch)
			if ok != test.ok || !equal(got, test.want) {
				t.Errorf("Decode() = %+v, %t; want %+v, %t", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestFullData(t *testing.T) {
	tx := Transaction{Data: ethrpc.Bytes{0x12}, DataLength: 3}
	if got := tx.FullData(); !bytes.Equal(got, []byte{0x12, 0, 0}) {
		t.Errorf("FullData() = %s, want 0x120000", got)
	}
}
