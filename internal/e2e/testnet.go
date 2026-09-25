package e2e

import (
	"math/big"
	"slices"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

var (
	transactionProposedEvent = solabi.Event("TransactionProposed(bytes32,bytes32,address,uint64,bytes,(uint256,address,address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,uint256))")
	// The deployed oracle's NewRequest has no DAO fee share, unlike the one in the
	// Safenet repository.
	newRequestEvent       = solabi.Event("NewRequest(bytes32,address,uint96,uint96,uint96,uint64,uint64)")
	disputeTriggeredEvent = solabi.Event("DisputeTriggered(bytes32,uint64)")
)

// Testnet runs Safenet requests on anvil.
type Testnet struct {
	*Anvil
	Artifacts *Artifacts
	// Sponsor proposes the transactions and pays their fees.
	Sponsor ethrpc.Address
	// ArbitrationTimeout is the oracle's ARBITRATION_TIMEOUT.
	ArbitrationTimeout uint64

	// nonce is the nonce of the last Safe transaction, which keeps proposals
	// distinct.
	nonce uint64
}

// NewTestnet starts anvil with the artifacts installed, and funds the sponsor,
// the sentinels, and the arbitrator. See StartAnvil for stateHistory. It skips
// the test if anvil isn't installed, or in short mode.
func NewTestnet(tb testing.TB, stateHistory int) *Testnet {
	tb.Helper()
	node := StartAnvil(tb, stateHistory)
	a := LoadArtifacts(tb)
	node.Install(a)
	n := &Testnet{Anvil: node, Artifacts: a, Sponsor: ethrpc.Address{0: 0x5f, 19: 1}}
	n.ArbitrationTimeout = n.callUint64("ARBITRATION_TIMEOUT()")

	// Fund the arbitrator with ether for gas, and the sponsor and sentinels with
	// fee tokens for fees and bonds, by depositing ether in WETH9.
	ether := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	funds := new(big.Int).Mul(big.NewInt(1_000_000), ether)
	n.SetBalance(a.Arbitrator, funds)
	for _, account := range append([]ethrpc.Address{n.Sponsor}, a.Sentinels...) {
		n.SetBalance(account, new(big.Int).Mul(funds, big.NewInt(2)))
		n.Receipt(n.Send(account, a.FeeToken, calldata("deposit()"), funds))
		n.Transact(account, a.FeeToken, calldata("approve(address,uint256)", a.Oracle, funds))
	}
	return n
}

// calldata returns the calldata for calling the function with signature, such
// as "transfer(address,uint256)", with args.
func calldata(signature string, args ...any) ethrpc.Bytes {
	return solabi.Call(solabi.Selector(signature), args...)
}

// callUint64 calls a function of the oracle without arguments, and decodes the
// uint64 it returns.
func (n *Testnet) callUint64(signature string) uint64 {
	n.tb.Helper()
	call := ethrpc.CallRequest{To: n.Artifacts.Oracle, Data: calldata(signature)}
	result, err := n.Call(n.tb.Context(), call, n.blockNumber())
	if err != nil {
		n.tb.Fatalf("%s: %v", signature, err)
	}
	d := solabi.NewDecoder(result)
	value := d.Uint64(0)
	if err := d.Err(); err != nil {
		n.tb.Fatalf("%s: %v", signature, err)
	}
	return value
}

// Request is a request that the test made, with what it expects the oracle and
// arbot to report about it.
type Request struct {
	n *Testnet

	ID ethrpc.Hash
	// Block and TxHash are the block and transaction of the proposal.
	Block      uint64
	TxHash     ethrpc.Hash
	OracleData []byte
	SafeTxHash ethrpc.Hash
	// Transaction is the proposed Safe transaction.
	Transaction                    *safenet.SafeTransaction
	CommitDeadline, RevealDeadline uint64
	// Ballots are the committed ballots, in commit order, and Revealed the
	// sentinels that revealed theirs.
	Ballots  []Ballot
	Revealed map[ethrpc.Address]bool
	// FrozenBlock is the block that finalizing the request froze it in.
	FrozenBlock uint64
	// Record is the receipt of the transaction that settled the arbitration, and
	// Context the arbitrator's context in it.
	Record  *Receipt
	Context string
}

// Ballot is a sentinel's vote on a request.
type Ballot struct {
	Request  *Request
	Sentinel ethrpc.Address
	Approve  bool
	Reason   string
}

// Approve returns a ballot by sentinel to approve the request.
func (r *Request) Approve(sentinel ethrpc.Address) Ballot {
	return Ballot{Request: r, Sentinel: sentinel, Approve: true}
}

// Deny returns a ballot by sentinel to deny the request, citing reason.
func (r *Request) Deny(sentinel ethrpc.Address, reason string) Ballot {
	return Ballot{Request: r, Sentinel: sentinel, Reason: reason}
}

// salt returns the ballot's commitment salt.
func (b Ballot) salt() ethrpc.Hash {
	return keccak256.Hash(b.Sentinel[:], b.Request.ID[:])
}

// Propose proposes a new Safe transaction with oracleData, in its own block.
func (n *Testnet) Propose(oracleData []byte) *Request {
	n.tb.Helper()
	tx := n.transaction()
	return n.proposed(n.Transact(n.Sponsor, n.Artifacts.Consensus, n.proposal(tx, oracleData)), tx, oracleData)
}

// ProposeInOneBlock proposes a new Safe transaction for each of the oracle
// data, all in the same block.
func (n *Testnet) ProposeInOneBlock(oracleData ...[]byte) []*Request {
	n.tb.Helper()
	txs := make([]*safenet.SafeTransaction, len(oracleData))
	hashes := make([]ethrpc.Hash, len(oracleData))
	n.SetAutomine(false)
	for i, data := range oracleData {
		txs[i] = n.transaction()
		hashes[i] = n.Send(n.Sponsor, n.Artifacts.Consensus, n.proposal(txs[i], data), nil)
	}
	n.MinePending()
	n.SetAutomine(true)

	requests := make([]*Request, len(oracleData))
	for i, data := range oracleData {
		requests[i] = n.proposed(n.Receipt(hashes[i]), txs[i], data)
		if requests[i].Block != requests[0].Block {
			n.tb.Fatalf("proposals are in blocks %d and %d, want the same block", requests[0].Block, requests[i].Block)
		}
	}
	return requests
}

// transaction returns a new Safe transaction on Ethereum Mainnet, which adds an
// owner to the Safe. Its fields vary with its nonce, so that every one of them
// is checked, and every other one is a delegate call.
func (n *Testnet) transaction() *safenet.SafeTransaction {
	n.nonce++
	i := byte(n.nonce)
	safe := ethrpc.Address{0: 0x5a, 19: 0xfe}
	return &safenet.SafeTransaction{
		ChainID:        big.NewInt(ethrpc.Mainnet),
		Safe:           safe,
		To:             safe,
		Value:          big.NewInt(int64(i)),
		Data:           calldata("addOwnerWithThreshold(address,uint256)", ethrpc.Address{0: 0xbe, 19: i}, uint64(2)),
		Operation:      safenet.Operation(i % 2),
		SafeTxGas:      big.NewInt(1000 * int64(i)),
		BaseGas:        big.NewInt(100 * int64(i)),
		GasPrice:       big.NewInt(10 * int64(i)),
		GasToken:       ethrpc.Address{0: 0x70, 19: i},
		RefundReceiver: ethrpc.Address{0: 0x7e, 19: i},
		Nonce:          big.NewInt(int64(i)),
	}
}

// proposal returns the calldata for proposing tx to the oracle.
func (n *Testnet) proposal(tx *safenet.SafeTransaction, oracleData []byte) ethrpc.Bytes {
	return calldata(
		"proposeTransaction(address,bytes,(uint256,address,address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,uint256))",
		n.Artifacts.Oracle,
		oracleData,
		solabi.Tuple{
			tx.ChainID, tx.Safe, tx.To, tx.Value, tx.Data, uint64(tx.Operation), tx.SafeTxGas,
			tx.BaseGas, tx.GasPrice, tx.GasToken, tx.RefundReceiver, tx.Nonce,
		},
	)
}

// proposed returns the request that proposing tx with oracleData made, given
// the proposal's receipt. Its ID and deadlines are those that the oracle
// logged.
func (n *Testnet) proposed(receipt *Receipt, tx *safenet.SafeTransaction, oracleData []byte) *Request {
	n.tb.Helper()
	proposal, ok := receipt.Log(n.Artifacts.Consensus, transactionProposedEvent)
	if !ok {
		n.tb.Fatalf("proposal %s has no TransactionProposed log", receipt.TransactionHash)
	}
	posted, ok := receipt.Log(n.Artifacts.Oracle, newRequestEvent)
	if !ok {
		n.tb.Fatalf("proposal %s has no NewRequest log", receipt.TransactionHash)
	}
	d := solabi.NewDecoder(posted.Data)
	r := &Request{
		n:              n,
		ID:             posted.Topics[1],
		Block:          uint64(receipt.BlockNumber),
		TxHash:         receipt.TransactionHash,
		OracleData:     oracleData,
		SafeTxHash:     proposal.Topics[1],
		Transaction:    tx,
		CommitDeadline: d.Uint64(3),
		RevealDeadline: d.Uint64(4),
		Revealed:       make(map[ethrpc.Address]bool),
	}
	if err := d.Err(); err != nil {
		n.tb.Fatalf("decoding NewRequest: %v", err)
	}
	return r
}

// Commit commits the ballots.
func (n *Testnet) Commit(ballots ...Ballot) {
	n.tb.Helper()
	for _, b := range ballots {
		approve := []byte{0}
		if b.Approve {
			approve[0] = 1
		}
		salt := b.salt()
		hash := keccak256.Hash(approve, salt[:], b.Sentinel[:], b.Request.ID[:], []byte(b.Reason))
		n.Transact(b.Sentinel, n.Artifacts.Oracle, calldata("commit(bytes32,bytes32)", b.Request.ID, ethrpc.Hash(hash)))
		b.Request.Ballots = append(b.Request.Ballots, b)
	}
}

// Vote commits the ballots, reveals them once the commit window has closed, and
// finalizes their requests once the reveal window has closed. Unlike in the
// Safenet repository, the deployed oracle doesn't finalize a request when the
// last committed vote on it is revealed.
func (n *Testnet) Vote(ballots ...Ballot) {
	n.tb.Helper()
	n.Commit(ballots...)
	var requests []*Request
	var commitDeadline, revealDeadline uint64
	for _, b := range ballots {
		if !slices.Contains(requests, b.Request) {
			requests = append(requests, b.Request)
		}
		commitDeadline = max(commitDeadline, b.Request.CommitDeadline)
		revealDeadline = max(revealDeadline, b.Request.RevealDeadline)
	}
	n.MineTo(commitDeadline + 1)

	for _, b := range ballots {
		r := b.Request
		n.Transact(b.Sentinel, n.Artifacts.Oracle,
			calldata("reveal(bytes32,bool,bytes32,string)", r.ID, b.Approve, b.salt(), b.Reason))
		r.Revealed[b.Sentinel] = true
	}
	n.MineTo(revealDeadline + 1)

	for _, r := range requests {
		r.Finalize()
	}
}

// Finalize finalizes the request, recording the block if it freezes it.
func (r *Request) Finalize() {
	r.n.tb.Helper()
	receipt := r.n.Transact(r.n.Sponsor, r.n.Artifacts.Oracle, calldata("finalize(bytes32)", r.ID))
	if _, ok := receipt.Log(r.n.Artifacts.Oracle, disputeTriggeredEvent); ok {
		r.FrozenBlock = uint64(receipt.BlockNumber)
	}
}

// Resolve rules on the frozen request as the arbitrator: secure if approveWins,
// and insecure otherwise.
func (r *Request) Resolve(approveWins bool, context string) {
	r.n.tb.Helper()
	r.settle(r.n.Artifacts.Arbitrator, calldata("resolveDispute(bytes32,bool,string)", r.ID, approveWins, context), context)
}

// MarkOutOfScope declines to rule on the frozen request as the arbitrator.
func (r *Request) MarkOutOfScope(context string) {
	r.n.tb.Helper()
	r.settle(r.n.Artifacts.Arbitrator, calldata("markOutOfScope(bytes32,string)", r.ID, context), context)
}

// TimeoutArbitration times out the arbitration of the frozen request, which
// must be past its deadline.
func (r *Request) TimeoutArbitration() {
	r.n.tb.Helper()
	r.settle(r.n.Sponsor, calldata("timeoutArbitration(bytes32)", r.ID), "")
}

// settle sends the transaction that settles the arbitration, and records its
// receipt and context.
func (r *Request) settle(from ethrpc.Address, data ethrpc.Bytes, context string) {
	r.n.tb.Helper()
	r.Record, r.Context = r.n.Transact(from, r.n.Artifacts.Oracle, data), context
}
