// Package multisend decodes the batches of transactions that the MultiSend and
// MultiSendCallOnly contracts of Safe 1.3.0, 1.4.1, and 1.5.0 run, as the
// contracts read them.
package multisend

import (
	"math/big"
	"slices"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// MaxDataLength is a data length that a transaction of a batch can't exceed
// without reverting: its call would expand memory past 2^24 bytes, which costs
// more than 2^29 gas, more than a transaction can use on the networks that the
// Charter covers.
const MaxDataLength = 1 << 24

// MaxPadding is how many zero bytes the decoders of nested batches, the package
// functions and the checks, add in all to the data of batch transactions that
// run past the end of their batch, which the contracts read as zeros. A batch
// that Safe's tools build has none, and a call that a batch cuts short only
// misses its last bytes, such as the zero bytes at the end of an amount, while
// each transaction of a crafted batch can claim up to MaxDataLength, 16 MiB, of
// them. 64 KiB is 2,048 words, far more than the 10 words of the longest head
// of the functions that the package functions decodes, execTransaction's, and
// bounds what a decoder allocates for zeros to 64 KiB for any call data, rather
// than 16 MiB for each batch.
const MaxPadding = 64 << 10

// Transaction is a transaction of a batch. Since 1.5.0, the contracts take the
// zero address as To to mean the account that runs the batch, such as the Safe
// that delegate calls MultiSend.
type Transaction struct {
	Operation safenet.Operation `json:"operation"`
	To        ethrpc.Address    `json:"to"`
	Value     *big.Int          `json:"value"`
	// Data is the transaction's data that the batch has, and DataLength the length
	// of the data that the contracts call with. Data is shorter only for a
	// transaction that runs past the end of the batch.
	Data       ethrpc.Bytes `json:"data"`
	DataLength uint64       `json:"dataLength"`
}

// FullData returns the data that the contracts call with: Data, followed by
// zeros up to DataLength.
func (tx Transaction) FullData() ethrpc.Bytes {
	data := make(ethrpc.Bytes, tx.DataLength)
	copy(data, tx.Data)
	return data
}

// PaddedData returns the data that the contracts call with, as FullData does,
// if its zeros fit in padding, the zeros that a decoder can still add, and
// takes them from it. It returns false if they don't fit.
func (tx Transaction) PaddedData(padding *int) (ethrpc.Bytes, bool) {
	zeros := int(tx.DataLength) - len(tx.Data)
	if zeros == 0 {
		return tx.Data, true
	}
	if zeros > *padding {
		return nil, false
	}
	*padding -= zeros
	return tx.FullData(), true
}

// Decode returns the transactions of batch, the argument of multiSend, as the
// contracts read them, or false if the batch reverts whatever the state:
//
//   - Its transactions are packed: an operation byte, a 20-byte address, a
//     32-byte value, a 32-byte data length, and the data. The contracts run them
//     while there are more than 32 bytes left, so they ignore up to 32 trailing
//     bytes.
//   - They don't check that a transaction fits in the bytes that are left, and
//     read past their end, where memory is zero. A transaction is therefore
//     decoded as if its bytes were followed by zeros, which its DataLength
//     counts and its Data leaves out.
//   - A transaction with more than MaxDataLength bytes of data needs more memory
//     than a transaction can pay for, so it reverts.
//   - An operation other than a call or a delegate call reverts.
//
// The rules that differ between the contracts, such as MultiSendCallOnly's
// refusal of transactions that are delegate calls, are the caller's.
func Decode(batch []byte) ([]Transaction, bool) {
	var transactions []Transaction
	for offset := 0; len(batch)-offset > 32; {
		head := [1 + 20 + 32 + 32]byte{}
		copy(head[:], batch[offset:])
		dataLength := new(big.Int).SetBytes(head[53:85])
		if !dataLength.IsUint64() || dataLength.Uint64() > MaxDataLength {
			return nil, false
		}
		length := int(dataLength.Uint64())
		start := min(offset+len(head), len(batch))
		data := slices.Clone(batch[start:min(start+length, len(batch))])
		offset += len(head) + length

		operation := safenet.Operation(head[0])
		if operation > safenet.OperationDelegateCall {
			return nil, false
		}
		transactions = append(transactions, Transaction{
			Operation:  operation,
			To:         ethrpc.Address(head[1:21]),
			Value:      new(big.Int).SetBytes(head[21:53]),
			Data:       data,
			DataLength: uint64(length),
		})
	}
	return transactions, true
}
