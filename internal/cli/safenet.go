package cli

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/safe-research/safenet-arbitration-bot/internal/config"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// openSafenet returns a Safenet that reads the configured SentinelOracle and
// Consensus contracts on Gnosis Chain, connecting to chains with dial.
func openSafenet(cfg config.Config, dial ethrpc.Dialer) *safenet.Safenet {
	oracle := cmp.Or(cfg.Oracle, safenet.DefaultOracle)
	consensus := cmp.Or(cfg.Consensus, safenet.DefaultConsensus)
	return safenet.New(dial, oracle, consensus)
}

// writeJSON writes v to w as indented JSON.
func writeJSON(w io.Writer, v any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

// readRequest reads a request from the JSON file at path, as `arbot info -json`
// writes it.
func readRequest(path string) (*safenet.Request, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Reject unknown fields, so that a misspelled field in a hand-written request
	// fails rather than being left empty.
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var request *safenet.Request
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("reading request from %s: %w", path, err)
	}
	if request == nil {
		return nil, fmt.Errorf("reading request from %s: no request", path)
	}
	return request, nil
}
