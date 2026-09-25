package safenet

import "github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"

// The addresses of the SentinelOracle and Consensus deployments on Gnosis Chain
// that commands use by default.
var (
	DefaultOracle    = mustParseAddress("0x544F12bAd6FF72564abBc7eA6494A2a4BdD0DDD0")
	DefaultConsensus = mustParseAddress("0x98810887769db19A0Df9bf2f44E4998856fcb390")
)

func mustParseAddress(s string) ethrpc.Address {
	address, err := ethrpc.ParseAddress(s)
	if err != nil {
		panic(err)
	}
	return address
}
