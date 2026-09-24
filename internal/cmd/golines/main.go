// Command golines checks that Go comments are wrapped to fill up to 80 columns,
// not counting indentation.
//
// Usage:
//
//	go run ./internal/cmd/golines [path ...]
//
// Each path is a Go file, or a directory that is searched recursively, and
// defaults to the current directory. Hidden directories, testdata, and vendor
// are skipped.
//
// Only full-line "// " comments are checked, one paragraph at a time. Blank
// comment lines, indented lines (code blocks and lists), and directives such as
// "//go:generate" separate paragraphs and are not checked themselves. Within a
// paragraph, a line must not exceed 80 columns unless it can't be broken, and
// the first word of the next line must not fit on it. Text in braces or double
// quotes, such as a JSON example, counts as a single word.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// width is the maximum width of a comment line, not counting indentation.
const width = 80

var progname = filepath.Base(os.Args[0])

// issue is a comment line that is wrapped incorrectly.
type issue struct {
	pos token.Position
	msg string
}

func main() {
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "Usage: %s [path ...]\n", progname)
	}
	flag.Parse()
	paths := flag.Args()
	if len(paths) == 0 {
		paths = []string{"."}
	}

	var found int
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			issues, err := checkFile(path)
			for _, issue := range issues {
				fmt.Printf("%s: %s\n", issue.pos, issue.msg)
			}
			found += len(issues)
			return err
		})
		if err != nil {
			die(1, "%v", err)
		}
	}
	if found > 0 {
		os.Exit(1)
	}
}

// checkFile parses the Go file at path and checks its comments.
func checkFile(path string) ([]issue, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	return check(fset, file, src), nil
}

// line is a full-line comment in a paragraph.
type line struct {
	pos token.Pos
	// text is the comment text after "// ".
	text string
}

// check returns the incorrectly wrapped comment lines of file, parsed from src.
func check(fset *token.FileSet, file *ast.File, src []byte) []issue {
	var issues []issue
	for _, group := range file.Comments {
		var paragraph []line
		for _, comment := range group.List {
			text, ok := strings.CutPrefix(comment.Text, "// ")
			if !ok || text == "" || text[0] == ' ' || text[0] == '\t' || !fullLine(fset, src, comment.Slash) {
				issues = append(issues, checkParagraph(fset, paragraph)...)
				paragraph = nil
				continue
			}
			paragraph = append(paragraph, line{comment.Slash, text})
		}
		issues = append(issues, checkParagraph(fset, paragraph)...)
	}
	return issues
}

// fullLine reports whether only whitespace precedes pos on its line in src.
func fullLine(fset *token.FileSet, src []byte, pos token.Pos) bool {
	offset := fset.File(pos).Offset(pos)
	start := bytes.LastIndexByte(src[:offset], '\n') + 1
	return len(bytes.TrimSpace(src[start:offset])) == 0
}

// checkParagraph returns the incorrectly wrapped lines of a paragraph.
func checkParagraph(fset *token.FileSet, paragraph []line) []issue {
	var issues []issue
	for i, l := range paragraph {
		n := len("// ") + utf8.RuneCountInString(l.text)
		if n > width && len(words(l.text)) > 1 {
			issues = append(issues, issue{fset.Position(l.pos), fmt.Sprintf("comment line is %d columns, longer than %d", n, width)})
		}
		if i+1 < len(paragraph) {
			next := words(paragraph[i+1].text)[0]
			if n+1+utf8.RuneCountInString(next) <= width {
				issues = append(issues, issue{fset.Position(l.pos), fmt.Sprintf("comment line is %d columns, and %q from the next line fits within %d", n, next, width)})
			}
		}
	}
	return issues
}

// words splits text into the words that wrapping may put on separate lines.
// Text in braces or double quotes stays in one word, even if it has spaces.
func words(text string) []string {
	var words, current []string
	depth, quoted := 0, false
	for _, word := range strings.Fields(text) {
		current = append(current, word)
		depth += strings.Count(word, "{") - strings.Count(word, "}")
		if strings.Count(word, `"`)%2 == 1 {
			quoted = !quoted
		}
		if depth <= 0 && !quoted {
			words = append(words, strings.Join(current, " "))
			current, depth = nil, 0
		}
	}
	if current != nil {
		words = append(words, strings.Join(current, " "))
	}
	return words
}

// die prints an error message prefixed with the program name to stderr and
// exits with the given status code.
func die(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", progname, fmt.Sprintf(format, args...))
	os.Exit(code)
}
