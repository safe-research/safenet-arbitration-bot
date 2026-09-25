package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCharterRequestFileErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	config := write("config.json", "{}")
	request := write("request.json", `{"charter": "charter.safenet-gov.eth", "proposal": {"ethereumBlock": 24000000}}`)

	tests := []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"-block", "24000000", "-request-file", request}, 2, "mutually exclusive"},
		{[]string{"-request-file", write("no-block.json", `{"charter": "charter.safenet-gov.eth"}`)}, 1, "no Charter name or Ethereum Mainnet block"},
		{[]string{"-request-file", write("no-charter.json", `{"proposal": {"ethereumBlock": 24000000}}`)}, 1, "no Charter name or Ethereum Mainnet block"},
		{[]string{"-request-file", write("unknown.json", `{"charter": "charter.safenet-gov.eth", "ethereumBlock": 24000000}`)}, 1, `unknown field "ethereumBlock"`},
		{[]string{"-request-file", filepath.Join(dir, "missing.json")}, 1, "no such file"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		args := append([]string{"arbot", "-config", config, "charter"}, test.args...)
		code := Run(t.Context(), args, &stdout, &stderr)
		if code != test.code || stdout.Len() != 0 || !strings.Contains(stderr.String(), test.stderr) {
			t.Errorf("charter %v: got status %d, stdout %q, stderr %q; want %d, no output, and stderr containing %q",
				test.args, code, stdout.String(), stderr.String(), test.code, test.stderr)
		}
	}
}
