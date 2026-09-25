// Package e2e runs the default Safenet deployment's contracts on a local anvil
// node, for end-to-end tests of arbot.
//
// The contracts' code, and the storage they need to work, are the artifacts in
// testdata/anvil.json, which internal/cmd/fetch-artifacts writes. Run `go
// generate ./internal/e2e` to fetch them again.
//
// Tests using the package are skipped if anvil isn't installed, or in short
// mode.
package e2e

import (
	_ "embed"
	"encoding/json"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// Run fetch-artifacts at the root of the repository, like arbot, so that it
// uses the same configuration file.
//go:generate go run -C ../.. ./internal/cmd/fetch-artifacts -o internal/e2e/testdata/anvil.json

//go:embed testdata/anvil.json
var artifactsJSON []byte

// Artifacts are the accounts that run the default Safenet deployment on anvil,
// and the addresses of its contracts and participants.
type Artifacts struct {
	Oracle     ethrpc.Address             `json:"oracle"`
	Consensus  ethrpc.Address             `json:"consensus"`
	FeeToken   ethrpc.Address             `json:"feeToken"`
	Arbitrator ethrpc.Address             `json:"arbitrator"`
	Sentinels  []ethrpc.Address           `json:"sentinels"`
	Epoch      uint64                     `json:"epoch"`
	Accounts   map[ethrpc.Address]Account `json:"accounts"`
}

// Account is the code and storage of an account.
type Account struct {
	Code    ethrpc.Bytes                `json:"code"`
	Storage map[ethrpc.Hash]ethrpc.Hash `json:"storage"`
}

// LoadArtifacts returns the artifacts, and fails the test if they aren't for
// the default deployment.
func LoadArtifacts(tb testing.TB) *Artifacts {
	tb.Helper()
	var a Artifacts
	if err := json.Unmarshal(artifactsJSON, &a); err != nil {
		tb.Fatalf("decoding testdata/anvil.json: %v", err)
	}
	if a.Oracle != safenet.DefaultOracle || a.Consensus != safenet.DefaultConsensus {
		tb.Fatalf("testdata/anvil.json is for SentinelOracle %s and Consensus %s, not the defaults %s and %s; run `go generate ./internal/e2e`",
			a.Oracle, a.Consensus, safenet.DefaultOracle, safenet.DefaultConsensus)
	}
	return &a
}
