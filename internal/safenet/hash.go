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
)

func typeHash(typ string) solabi.Word {
	return keccak256.Hash([]byte(typ))
}

// Hash returns the EIP-712 hash of the transaction that the Safe's owners sign,
// the Safe transaction hash.
func (tx *SafeTransaction) Hash() ethrpc.Hash {
	domain := hashWords(domainTypeHash, solabi.Uint(tx.ChainID), solabi.Address(tx.Safe))
	message := hashWords(
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
	return typedDataHash(domain, message)
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
