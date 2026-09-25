package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyRequestFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	config := write("config.json", "{}")
	request := write("request.json", `{"id": "0x062be5de4a4ca7123b0894a48807d09ca0e445c282e48dc3601e141bd32b48cb", "state": "FROZEN"}`)

	tests := []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"-request-file", request}, 0, "unclassified\n", ""},
		{[]string{"-json", "-request-file", request}, 0, "{\n  \"verdict\": null\n}\n", ""},
		{[]string{"-request-file", write("unknown.json", `{"stat": "FROZEN"}`)}, 1, "", `unknown field "stat"`},
		{[]string{"-request-file", write("state.json", `{"state": "frozen"}`)}, 1, "", `unknown value "frozen"`},
		{[]string{"-request-file", write("null.json", `null`)}, 1, "", "no request"},
		{[]string{"-request-file", filepath.Join(dir, "missing.json")}, 1, "", "no such file"},
		{[]string{"-request-file", request, "0x062be5de4a4ca7123b0894a48807d09ca0e445c282e48dc3601e141bd32b48cb"}, 2, "", "Usage:"},
		{[]string{}, 2, "", "Usage:"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		args := append([]string{"arbot", "-config", config, "classify"}, test.args...)
		code := Run(t.Context(), args, &stdout, &stderr)
		if code != test.code || stdout.String() != test.stdout || !strings.Contains(stderr.String(), test.stderr) {
			t.Errorf("classify %v: got status %d, stdout %q, stderr %q; want %d, %q, and stderr containing %q",
				test.args, code, stdout.String(), stderr.String(), test.code, test.stdout, test.stderr)
		}
	}
}
