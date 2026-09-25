package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// arbotPackage is the import path of the arbot command.
const arbotPackage = "github.com/safe-research/safenet-arbitration-bot/cmd/arbot"

// Arbot runs a built arbot binary, configured to read Gnosis Chain from a node.
type Arbot struct {
	path   string
	config string
}

// BuildArbot builds arbot, configured to read Gnosis Chain from the node.
func BuildArbot(tb testing.TB, node *Anvil) *Arbot {
	tb.Helper()
	dir := tb.TempDir()
	path := filepath.Join(dir, "arbot")
	if out, err := exec.Command("go", "build", "-o", path, arbotPackage).CombinedOutput(); err != nil {
		tb.Fatalf("building arbot: %v\n%s", err, out)
	}
	config := filepath.Join(dir, "config.json")
	data := fmt.Sprintf(`{"rpcs": {"%d": %q}}`, ethrpc.Gnosis, node.URL)
	if err := os.WriteFile(config, []byte(data), 0o644); err != nil {
		tb.Fatal(err)
	}
	return &Arbot{path: path, config: config}
}

// Run runs arbot with args, and returns its stdout. It fails the test if arbot
// fails.
func (a *Arbot) Run(tb testing.TB, args ...string) []byte {
	tb.Helper()
	var stderr bytes.Buffer
	cmd := a.command(args)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		tb.Fatalf("arbot %v: %v\n%s", args, err, stderr.Bytes())
	}
	return out
}

// Fail runs arbot with args, and returns its stderr. It fails the test unless
// arbot exits with status code.
func (a *Arbot) Fail(tb testing.TB, code int, args ...string) []byte {
	tb.Helper()
	var stderr bytes.Buffer
	cmd := a.command(args)
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != code {
		tb.Fatalf("arbot %v: got error %v, want exit status %d", args, err, code)
	}
	return stderr.Bytes()
}

func (a *Arbot) command(args []string) *exec.Cmd {
	return exec.Command(a.path, append([]string{"-config", a.config}, args...)...)
}
