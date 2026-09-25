package cli

import (
	"cmp"
	"encoding/json"
	"io"

	"github.com/safe-research/safenet-arbitration-bot/internal/config"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// openSafenet returns a Safenet that reads the configured SentinelOracle and
// Consensus contracts on Gnosis Chain, using the configured RPCs.
func openSafenet(cfg config.Config) *safenet.Safenet {
	oracle := cmp.Or(cfg.Oracle, safenet.DefaultOracle)
	consensus := cmp.Or(cfg.Consensus, safenet.DefaultConsensus)
	return safenet.New(ethrpc.NewDialer(cfg.RPCs), oracle, consensus)
}

// writeJSON writes v to w as indented JSON.
func writeJSON(w io.Writer, v any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}
