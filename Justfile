# Repo-wide command runner.

prettier := "npm exec -y -- prettier@3.9.9"

# List available recipes.
default:
    @just --list

# Lint and format-check the Go sources and Markdown documentation.
check:
    gofmt -d -l .
    go vet ./...
    {{prettier}} --check "**/*.md"

# Auto-fix formatting issues.
fix:
    go fmt ./...
    {{prettier}} --write "**/*.md"
