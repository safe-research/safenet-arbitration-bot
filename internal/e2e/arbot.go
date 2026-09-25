package e2e

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/cli"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// Arbot runs arbot in the test's process, configured to read Gnosis Chain from
// a node. As the test imports arbot's implementation, changing it invalidates
// the test's cached results.
type Arbot struct {
	config string
}

// NewArbot returns an Arbot configured to read Gnosis Chain from the node.
func NewArbot(tb testing.TB, node *Anvil) *Arbot {
	tb.Helper()
	config := filepath.Join(tb.TempDir(), "config.json")
	data := fmt.Sprintf(`{"rpcs": {"%d": %q}}`, ethrpc.Gnosis, node.URL)
	if err := os.WriteFile(config, []byte(data), 0o644); err != nil {
		tb.Fatal(err)
	}
	return &Arbot{config: config}
}

// Run runs arbot with args, and returns its stdout. It fails the test if arbot
// fails.
func (a *Arbot) Run(tb testing.TB, args ...string) []byte {
	tb.Helper()
	code, stdout, stderr := a.run(tb, args)
	if code != 0 {
		tb.Fatalf("arbot %v: exit status %d\n%s", args, code, stderr)
	}
	return stdout
}

// Fail runs arbot with args, and returns its stderr. It fails the test unless
// arbot exits with status code.
func (a *Arbot) Fail(tb testing.TB, code int, args ...string) []byte {
	tb.Helper()
	got, _, stderr := a.run(tb, args)
	if got != code {
		tb.Fatalf("arbot %v: got exit status %d, want %d\n%s", args, got, code, stderr)
	}
	return stderr
}

func (a *Arbot) run(tb testing.TB, args []string) (code int, stdout, stderr []byte) {
	var out, errs bytes.Buffer
	code = cli.Run(tb.Context(), append([]string{"arbot", "-config", a.config}, args...), &out, &errs)
	return code, out.Bytes(), errs.Bytes()
}
