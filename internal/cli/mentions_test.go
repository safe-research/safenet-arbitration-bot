package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// mention is an arbot command line that a document mentions, such as
// "arbot calls -json -request-file request.json".
type mention struct {
	line int
	// global are the flags before the command, command is the command, or "" if
	// there is none, such as for "arbot -h", and flags are its flags, without their
	// dashes and values. late are the flags after its first argument, which it
	// reads as arguments.
	global  []string
	command string
	flags   []string
	late    []string
}

var (
	codeSpan    = regexp.MustCompile("``(.+?)``|`([^`]+)`")
	flagToken   = regexp.MustCompile(`^--?([a-zA-Z][a-zA-Z0-9-]*)`)
	usageFlag   = regexp.MustCompile(`(?m)^ {2}-([^\s=]+)(?: (\S+))?(?:\t|$)`)
	shellBreaks = []string{"|", ">", ">>", "<", "&", "&&", "||", ";", "2>", "2>&1"}
	// splitBreaks separates the breaks that may be attached to a word, such as in
	// "<request-id>;" or "-json|jq".
	splitBreaks = strings.NewReplacer(";", " ; ", "|", " | ", "&&", " && ")
)

// mentions returns the arbot command lines in the code spans and code blocks of
// a Markdown document. valued reports whether a flag of a command, or of arbot
// itself for the command "", takes a value, which isn't a command.
func mentions(text string, valued func(command, flag string) bool) []mention {
	var found []mention
	fence := false
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line, start := lines[i], i+1
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			fence = !fence
			continue
		}
		var code []string
		if fence {
			// A line that ends with a backslash goes on in the next one, unless that one
			// ends the code block.
			for strings.HasSuffix(line, `\`) && i+1 < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "```") {
				i++
				line = strings.TrimSuffix(line, `\`) + " " + lines[i]
			}
			code = []string{line}
		} else {
			for _, m := range codeSpan.FindAllStringSubmatch(line, -1) {
				code = append(code, m[1]+m[2])
			}
		}
		for _, c := range code {
			found = append(found, commandLines(start, c, valued)...)
		}
	}
	return found
}

// commandLines returns the arbot command lines in a piece of code, such as
// ".msgboard/bin/arbot info -json <request-id> > request.json".
func commandLines(line int, code string, valued func(command, flag string) bool) []mention {
	var found []mention
	words, quoted := shellWords(splitBreaks.Replace(code))
	for i, word := range words {
		// arbot may follow a path, "$(", or "=".
		if word[strings.LastIndexAny(word, "/$(=")+1:] != "arbot" {
			continue
		}
		m := mention{line: line}
		rest := words[i+1:]
		for len(rest) > 0 {
			token := strings.Trim(rest[0], "[](),.")
			if slices.Contains(shellBreaks, rest[0]) {
				rest = nil
				break
			}
			name := flagToken.FindStringSubmatch(token)
			if name == nil {
				break
			}
			m.global = append(m.global, name[1])
			rest = rest[1:]
			if valued("", name[1]) && !strings.Contains(token, "=") && len(rest) > 0 {
				rest = rest[1:]
			}
		}
		if len(rest) > 0 {
			command := strings.Trim(rest[0], "[](),.:")
			// A placeholder, such as <command>, or a path, such as ./cmd/arbot in
			// "go build -o arbot ./cmd/arbot", isn't a command.
			if command == "" || strings.ContainsAny(command, "<>{}/.") || slices.Contains(shellBreaks, rest[0]) {
				continue
			}
			m.command = command
			depth, value, argument := 0, false, false
			for _, word := range rest[1:] {
				// In a usage such as "[-block N | -request-file <path>]" or "<a | b>", | is an
				// alternative, not a pipe.
				if slices.Contains(shellBreaks, word) && !(word == "|" && depth > 0) {
					break
				}
				depth += strings.Count(word, "[") + strings.Count(word, "<") - strings.Count(word, "]") - strings.Count(word, ">")
				token := strings.Trim(word, "[](),.")
				name := flagToken.FindStringSubmatch(token)
				switch {
				case value:
					value = false
				case name == nil:
					argument = argument || word != "|"
				case argument:
					// A command stops reading flags at its first argument.
					m.late = append(m.late, name[1])
				default:
					m.flags = append(m.flags, name[1])
					value = valued(command, name[1]) && !strings.Contains(token, "=")
				}
			}
		}
		found = append(found, m)
	}
	for _, q := range quoted {
		found = append(found, commandLines(line, q, valued)...)
	}
	return found
}

// shellWords splits code into words at the spaces outside quotes, and removes
// the quotes, as a shell does. quoted are the quoted strings with a space,
// which may be code too, as in bash -c "arbot info -json <request-id>".
func shellWords(code string) (words, quoted []string) {
	var word, part strings.Builder
	var quote rune
	for _, r := range code {
		switch {
		case quote != 0 && r == quote:
			quote = 0
			if strings.Contains(part.String(), " ") {
				quoted = append(quoted, part.String())
			}
			part.Reset()
		case quote != 0:
			word.WriteRune(r)
			part.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t':
			if word.Len() > 0 {
				words = append(words, word.String())
				word.Reset()
			}
		default:
			word.WriteRune(r)
		}
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}
	return words, quoted
}

// usageFlags returns the flags that arbot's usage lists, for a command, or for
// arbot itself if command is "", and whether each takes a value.
func usageFlags(t *testing.T, config, command string) map[string]bool {
	t.Helper()
	args := []string{"arbot", "-config", config, "-h"}
	if command != "" {
		args = []string{"arbot", "-config", config, command, "-h"}
	}
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("%v: got status %d, stderr %q", args, code, stderr.String())
	}
	flags := map[string]bool{"h": false, "help": false}
	for _, m := range usageFlag.FindAllStringSubmatch(stderr.String(), -1) {
		flags[m[1]] = m[2] != ""
	}
	if len(flags) == 2 {
		t.Fatalf("%v: no flags in the usage %q", args, stderr.String())
	}
	return flags
}

func TestMentionedCommandsExist(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	flags := map[string]map[string]bool{"": usageFlags(t, config, "")}
	for name := range commands {
		flags[name] = usageFlags(t, config, name)
	}
	valued := func(command, flag string) bool { return flags[command][flag] }

	var files []string
	for _, pattern := range []string{"../../skills/*/*.md", "../../agents/*.md", "../../.claude/agents/*.md", "../../AGENTS.md"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	if !slices.Contains(files, "../../skills/arbitrate/SKILL.md") {
		t.Fatalf("got files %v, want the arbitrate skill among them", files)
	}
	count := 0
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range mentions(string(content), valued) {
			where := fmt.Sprintf("%s:%d", file, m.line)
			for _, flag := range m.global {
				if _, ok := flags[""][flag]; !ok {
					t.Errorf("%s: arbot has no flag -%s", where, flag)
				}
			}
			if m.command == "" {
				continue
			}
			count++
			commandFlags, ok := flags[m.command]
			if !ok {
				t.Errorf("%s: arbot has no command %q", where, m.command)
				continue
			}
			for _, flag := range m.flags {
				if _, ok := commandFlags[flag]; !ok {
					t.Errorf("%s: arbot %s has no flag -%s", where, m.command, flag)
				}
			}
			for _, flag := range m.late {
				t.Errorf("%s: arbot %s reads -%s as an argument, since it comes after one", where, m.command, flag)
			}
		}
	}
	if count < 50 {
		t.Errorf("found %d arbot commands, want more: is the parser broken?", count)
	}
}

func TestMentions(t *testing.T) {
	valued := func(_, flag string) bool {
		return slices.Contains([]string{"config", "block", "request-file", "verify"}, flag)
	}
	text := strings.Join([]string{
		"Run `.msgboard/bin/arbot -config c.json transfers -in -json -request-file <path> > out.json`.",
		"Build it with `go build -o .msgboard/bin/arbot ./cmd/arbot`, then run `arbot <command> -h` or `arbot -h`.",
		"```",
		"arbot events -request-file request.json <transaction-hash> | jq -c .",
		"```",
		"The `arbot` CLI, and ``arbot charter [-block N | -request-file <path>] [-verify <file>] | jq -r .``.",
		"```json",
		`{"command": "arbot info -json 0x01"}`,
		`request=$(arbot pending -json)`,
		"```",
		"Run `arbot tags <id>; arbot index -w`, `arbot pending -json|jq -r .`, `arbot info -json & jq -c .` and `arbot x <a | b> -json`.",
		"```sh",
		`arbot info \`,
		"  -json <request-id>",
		"```",
		"Run `arbot -config \"my settings.json\" info -json` and `arbot tags <request-id> -json`.",
		"```sh",
		`arbot info \`,
		"```",
		"Then run `arbot tags <request-id>`:",
		"```sh",
		"arbot pending -json",
		"```",
	}, "\n")
	got := mentions(text, valued)
	want := []mention{
		{line: 1, global: []string{"config"}, command: "transfers", flags: []string{"in", "json", "request-file"}},
		// The paths and the placeholder aren't commands.
		{line: 2},
		{line: 2, global: []string{"h"}},
		{line: 4, command: "events", flags: []string{"request-file"}},
		{line: 6},
		{line: 6, command: "charter", flags: []string{"block", "request-file", "verify"}},
		{line: 8, command: "info", flags: []string{"json"}},
		{line: 9, command: "pending", flags: []string{"json"}},
		// A break attached to a word ends the command, and | inside <...> doesn't.
		{line: 11, command: "tags"},
		{line: 11, command: "index", flags: []string{"w"}},
		{line: 11, command: "pending", flags: []string{"json"}},
		{line: 11, command: "info", flags: []string{"json"}},
		{line: 11, command: "x", late: []string{"json"}},
		// A command goes on after a backslash, a quoted value is one word, and a flag
		// after an argument is an argument.
		{line: 13, command: "info", flags: []string{"json"}},
		{line: 16, global: []string{"config"}, command: "info", flags: []string{"json"}},
		{line: 16, command: "tags", late: []string{"json"}},
		// A code block ends at its fence, even after a backslash.
		{line: 18, command: "info"},
		{line: 20, command: "tags"},
		{line: 22, command: "pending", flags: []string{"json"}},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got mentions\n%v\nwant\n%v", got, want)
	}
}
