# Repo-wide command runner.

prettier := "npm exec -y -- prettier@3.9.9"

# List available recipes.
default:
    @just --list

# Compile the arbot CLI tool.
build:
    go build ./cmd/arbot

# Run the arbot CLI tool, e.g. `just arbot --help`.
arbot *args:
    go run ./cmd/arbot {{args}}

# Lint and format-check the Go sources and Markdown documentation.
check:
    gofmt -d -l .
    go vet ./...
    {{prettier}} --check "**/*.md"

# Auto-fix formatting issues.
fix:
    go fmt ./...
    {{prettier}} --write "**/*.md"

# Run the Go tests. Specific tests to run can be specified with:
# `just test ./internal/config -run '^TestLoadSearchOrder$'`.
test *args="./...":
    go test {{args}}

# Run all pre-commit checks.
precommit: fix check test
