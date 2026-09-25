# Safenet Arbitration Bot

This repository contains a set of tools and agent configurations to help arbitrate disputed Safenet transaction proposals.

## Requirements

- Just: to simplify orchestrating execution of various commands
- Go: for building the `arbot` tool, which includes a set of helpful commands for various arbitration-related tasks
- NodeJS: for Markdown formatting with prettier.
- jq: for checking the formatting of JSON test data.
- Foundry: for running the end-to-end tests on a local `anvil` node. The tests are skipped when `anvil` is not installed.
