package checks

import (
	"context"
	"slices"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// networks are the chain IDs of the networks that the Charter's
// transaction-security rules apply to (Article I, Networks).
var networks = []uint64{ethrpc.Mainnet, ethrpc.ArbitrumOne, ethrpc.Gnosis}

// offNetwork finds a call by a Safe on a network that isn't in networks out of
// scope, as a request outside the networks of Article I is (§ 3.9).
var offNetwork = check{
	verdict:     OutOfScope,
	description: "Safe on a network that the Charter doesn't cover",
	fn: func(_ context.Context, safe *safeID, _ call) (bool, error) {
		return !slices.ContainsFunc(networks, func(n uint64) bool {
			return safe.chainID.IsUint64() && safe.chainID.Uint64() == n
		}), nil
	},
}
