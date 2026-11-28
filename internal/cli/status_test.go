package cli

import (
	"bytes"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/executions"
	"github.com/safe-research/safenet-arbitration-bot/internal/status"
)

func TestStatusErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	config := write("config.json", "{}")
	request := write("request.json", `{"proposal": {"safeBlock": 24000000, "transaction": {"chainId": 1}}}`)

	tests := []struct {
		args   []string
		code   int
		stderr string
	}{
		{nil, 2, "-request-file is required"},
		{[]string{"-request-file", request, "-blocks", "0"}, 2, "-blocks must be at least 1"},
		{[]string{"-request-file", request, "extra"}, 2, "Usage: arbot status"},
		{[]string{"-request-file", write("no-block.json", `{"proposal": {"transaction": {"chainId": 1}}}`)}, 1, "no Safe chain ID or block"},
		{[]string{"-request-file", filepath.Join(dir, "missing.json")}, 1, "no such file"},
	}
	for _, test := range tests {
		args := append([]string{"-config", config, "status"}, test.args...)
		code, stdout, stderr := runArbot(t, args...)
		if code != test.code || stdout != "" || !strings.Contains(stderr, test.stderr) {
			t.Errorf("status %v: got status %d, stdout %q, stderr %q; want %d, no output, and stderr containing %q",
				test.args, code, stdout, stderr, test.code, test.stderr)
		}
	}
}

func TestWriteStatus(t *testing.T) {
	block, success := uint64(503), false
	at := fakeBlockTime(503)
	tx, other := ethrpc.Hash{0: 0xbb, 31: 3}, ethrpc.Hash{0: 0x07, 31: 2}
	report := func(x status.Execution, notes ...string) *status.Report {
		return &status.Report{
			Request: ethrpc.Hash{0: 0x4e, 31: 1}, Safe: historySafe, ChainID: 1, SafeBlock: 500, SafeTxHash: ethrpc.Hash{0: 0x0e, 31: 1},
			Nonce: status.Nonce{
				Transaction: big.NewInt(5), SafeAtSafeBlock: big.NewInt(5), SafeAtLatest: big.NewInt(6), LatestBlock: 1000,
			},
			Execution: x, Notes: append([]string{status.NotEvidence}, notes...),
		}
	}
	for _, test := range []struct {
		name   string
		report *status.Report
		want   []string
	}{
		{
			"another transaction",
			report(status.Execution{
				Status: status.OtherTransaction, Source: status.SourceLogs, TxHash: &tx, Block: &block, BlockTime: &at, Success: &success,
				SafeTxHash: &other, Reason: "The Safe executed another Safe transaction.", Scanned: &executions.BlockRange{FromBlock: 500, ToBlock: 599},
				Service: &status.Service{URL: "https://service.example", ObservedAt: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)},
			}),
			[]string{
				`^Status of the Safe transaction of request 0x4e0{61}1$`,
				`^` + regexp.QuoteMeta(status.NotEvidence) + `$`,
				`^  Safe +` + historySafe.String() + ` on chain 1$`,
				`^  Safe at the latest block +6 \(block 1000\)$`,
				`^  Stale +no$`,
				`^  Status +other-transaction$`,
				`^  Source +logs$`,
				`^  Executed +block 503 \(` + at.Format(time.RFC3339) + `\), transaction ` + tx.String() + `, failure$`,
				`^  Other Safe tx hash +` + other.String() + `$`,
				`^  Service +"https://service\.example", observed 2026-11-26T12:00:00Z$`,
				`^  Scanned +blocks 500 to 599$`,
				`^  none$`,
			},
		},
		{
			// The reason and the notes can have a node's error, which is escaped.
			"unknown",
			report(status.Execution{Status: status.Unknown, Reason: "Scanning failed: \x1b[2Jcleared."}, "A note from \u202ethe service."),
			[]string{
				`^  Status +unknown$`,
				`^  Reason +Scanning failed: \\x1b\[2Jcleared\.$`,
				`^  Service +none for chain 1$`,
				`^  - A note from \\u202ethe service\.$`,
			},
		},
	} {
		var stdout bytes.Buffer
		if err := writeStatus(&env{stdout: &stdout}, test.report); err != nil {
			t.Fatal(err)
		}
		matchLines(t, "status "+test.name, stdout.Bytes(), test.want...)
		if test.report.Execution.Source == "" && strings.Contains(stdout.String(), "Source") {
			t.Errorf("status %s: output has a source:\n%s", test.name, stdout.String())
		}
	}
}
