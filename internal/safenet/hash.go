package safenet

import (
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// EIP-712 type hashes of the domains and messages that Safenet hashes.
var (
	domainTypeHash              = typeHash("EIP712Domain(uint256 chainId,address verifyingContract)")
	safeTxTypeHash              = typeHash("SafeTx(address to,uint256 value,bytes data,uint8 operation,uint256 safeTxGas,uint256 baseGas,uint256 gasPrice,address gasToken,address refundReceiver,uint256 nonce)")
	transactionProposalTypeHash = typeHash("TransactionProposal(uint64 epoch,address oracle,bytes oracleData,bytes32 safeTxHash)")
	// legacyDomainTypeHash is the domain of the Safe versions before 1.3.0, which
	// has no chain ID.
	legacyDomainTypeHash = typeHash("EIP712Domain(address verifyingContract)")
)

func typeHash(typ string) solabi.Word {
	return keccak256.Hash([]byte(typ))
}

// Hash returns the EIP-712 hash of the transaction that the Safe's owners sign,
// the Safe transaction hash, as Safe 1.3.0 and later versions hash it, and
// Safenet with them.
func (tx *SafeTransaction) Hash() ethrpc.Hash {
	domain := hashWords(domainTypeHash, solabi.Uint(tx.ChainID), solabi.Address(tx.Safe))
	return typedDataHash(domain, tx.message())
}

// LegacyHash returns the Safe transaction hash as Safe 1.0.0, 1.1.1, and 1.2.0
// hash it, in a domain without the chain ID. 1.1.1 and 1.2.0 log it when they
// execute the transaction.
func (tx *SafeTransaction) LegacyHash() ethrpc.Hash {
	domain := hashWords(legacyDomainTypeHash, solabi.Address(tx.Safe))
	return typedDataHash(domain, tx.message())
}

// message returns the hash of the transaction's SafeTx message, which Safe
// 1.0.0 and later versions hash the same way.
func (tx *SafeTransaction) message() solabi.Word {
	return hashWords(
		safeTxTypeHash,
		solabi.Address(tx.To),
		solabi.Uint(tx.Value),
		keccak256.Hash(tx.Data),
		solabi.Uint64(uint64(tx.Operation)),
		solabi.Uint(tx.SafeTxGas),
		solabi.Uint(tx.BaseGas),
		solabi.Uint(tx.GasPrice),
		solabi.Address(tx.GasToken),
		solabi.Address(tx.RefundReceiver),
		solabi.Uint(tx.Nonce),
	)
}

// requestID returns the ID of the oracle request for a transaction proposal:
// the EIP-712 hash of the TransactionProposal message that validators attest
// to, in the domain of the Consensus contract at consensus on chain chainID.
func requestID(chainID uint64, consensus ethrpc.Address, epoch uint64, oracle ethrpc.Address, oracleData []byte, safeTxHash ethrpc.Hash) ethrpc.Hash {
	domain := hashWords(domainTypeHash, solabi.Uint64(chainID), solabi.Address(consensus))
	message := hashWords(
		transactionProposalTypeHash,
		solabi.Uint64(epoch),
		solabi.Address(oracle),
		keccak256.Hash(oracleData),
		safeTxHash,
	)
	return typedDataHash(domain, message)
}

func hashWords(words ...solabi.Word) solabi.Word {
	data := make([]byte, 0, len(words)*32)
	for _, word := range words {
		data = append(data, word[:]...)
	}
	return keccak256.Hash(data)
}

func typedDataHash(domain, message solabi.Word) ethrpc.Hash {
	return keccak256.Hash([]byte{0x19, 0x01}, domain[:], message[:])
}
