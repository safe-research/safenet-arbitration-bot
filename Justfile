# Repo-wide command runner.

prettier := "npm exec -y --prefer-offline -- prettier@3.9.9"

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
    go run ./internal/cmd/golines
    {{prettier}} --check "**/*.md"

# Auto-fix formatting issues.
fix:
    go fmt ./...
    {{prettier}} --write "**/*.md"

# Run the Go tests. Specific tests to run can be specified with:
# `just test ./internal/config -run '^TestLoadSearchOrder$'`.
test *args="./...":
    go test {{args}}

# Check that every sub-agent has Claude Code and Codex CLI stubs with matching
# names and descriptions.
check-agent-stubs:
    #!/usr/bin/env bash
    set -euo pipefail
    names() { ls "$1" | sed 's/\.[^.]*$//'; }
    diff <(names agents) <(names .claude/agents) \
        || { echo "agents/ and .claude/agents/ differ"; exit 1; }
    diff <(names agents) <(names .codex/agents) \
        || { echo "agents/ and .codex/agents/ differ"; exit 1; }
    fields='name|description'
    diff \
        <(sed -nE "s/^($fields): /\1: /p" .claude/agents/*.md) \
        <(sed -nE "s/^($fields) = \"(.*)\"$/\1: \2/p" .codex/agents/*.toml) \
        || { echo "Claude Code and Codex CLI stubs differ"; exit 1; }
    for name in $(names agents); do
        for stub in ".claude/agents/$name.md" ".codex/agents/$name.toml"; do
            grep -qxF "Follow the instructions in @agents/$name.md." "$stub" \
                || { echo "$stub does not point to agents/$name.md"; exit 1; }
        done
    done

# Run all pre-commit checks.
precommit: fix check test check-agent-stubs
