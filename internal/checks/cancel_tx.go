package checks

import (
	"context"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// The cancellation checks find a call by the Safe with no data or value to an
// account that does nothing with it secure, such as the transactions that
// wallets propose to cancel another by using up its nonce. The call changes no
// setting or storage, sends no value, grants nothing, and calls no other
// contract, so it has none of the effects that the rules of Article IV
// consider, whatever else the transaction does. A call with value may only send
// it back to the Safe or burn it, but the checks abstain on it, as it isn't a
// cancellation. They also abstain on the refund, whose recipient is the account
// that executes the transaction if the refund receiver is the zero address, and
// whose value is only a bound on the refund.
var (
	// The Safe of every § 2.1 version handles a call with no data in its receive
	// function, which only emits SafeReceived. Safe{Wallet} cancels a transaction
	// with such a call.
	emptySelfCallCheck = check{
		verdict:     Secure,
		description: "calls the Safe with no data or value, as a cancellation does",
		fn: func(_ context.Context, _ *env, safe *safeID, c call) (bool, error) {
			return emptyCall(c, safe.address), nil
		},
	}
	// The zero address has no code, and no contract can be deployed to it, so a
	// call to it does nothing. Some wallets cancel a transaction with such a call.
	emptyZeroAddressCallCheck = check{
		verdict:     Secure,
		description: "calls the zero address with no data or value, as a cancellation does",
		fn: func(_ context.Context, _ *env, _ *safeID, c call) (bool, error) {
			return emptyCall(c, ethrpc.Address{}), nil
		},
	}
)

// emptyCall reports whether c is a call to the account at to with no data or
// value, other than the refund.
func emptyCall(c call, to ethrpc.Address) bool {
	return c.kind != Refund && c.to == to && c.operation == safenet.OperationCall && c.value.Sign() == 0 &&
		len(c.data) == 0
}
