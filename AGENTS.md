# Safenet Arbitration Bot

This bot helps the Safenet Security Council arbitrate disputed Safenet transactions. When sentinels split on a transaction, the `SentinelOracle` request is `FROZEN` and the Council must rule `secure` or `insecure` (or decide it is `out of scope`) under the Safenet Arbitration Charter. The bot collects the evidence, runs deterministic Charter checks in code, falls back to an LLM for ambiguous cases, and drafts a ruling for humans to review. It also keeps a text corpus of past rulings that it uses as precedent.

> The repository is greenfield. This file describes the target architecture. Keep it up to date as components land, and replace "planned" wording with what actually exists.

## Protocol Background

### Sentinel oracle

- **Proposal.** A Safe transaction is proposed via `Consensus.proposeTransaction(oracle, oracleData, transaction)`. This emits `TransactionProposed(safeTxHash, safeId, oracle, epoch, oracleData, transaction)` and calls `SentinelOracle.postRequest`. The `transaction` is a `SafeTransaction` struct: `chainId, safe, to, value, data, operation, safeTxGas, baseGas, gasPrice, gasToken, refundReceiver, nonce`.
- **Request ID → transaction.** The oracle `requestId` is the Consensus `transactionProposal` message hash, `domainSeparator().transactionProposal(epoch, oracle, keccak256(oracleData), safeTxHash)`. `SentinelOracle` does not store the transaction. To find it, compute the proposal block as `getRequest(id).terms.commitDeadline - COMMIT_WINDOW`. Then take the `TransactionProposed` logs from `Consensus` in that block and pick the one whose recomputed message hash equals `requestId`.
- **Request lifecycle.** Sentinels bond and `commit`, then `reveal(requestId, approve, salt, reason)`. `finalize` settles the request:
  - a unanimous vote resolves it directly to `RESOLVED_APPROVED` or `RESOLVED_DENIED`;
  - no revealed votes time it out (`TIMED_OUT`, `RequestTimedOut`);
  - a split vote moves it to `FROZEN` and emits `DisputeTriggered(requestId, deadline)`. A dispute is only arbitrated in this `FROZEN` state.
- **Arbitration outcomes.** From `FROZEN`, the arbitrator (the Council's Arbitrator Safe) takes one of three paths:
  - `resolveDispute(requestId, approveWins, context)`, emitting `DisputeResolved(requestId, outcome, slashed, context)`. `approveWins = true` is a `secure` ruling and `false` is `insecure`. The losing side's sentinels are slashed.
  - `markOutOfScope(requestId, context)`, emitting `DisputeOutOfScope`.
  - No action. `timeoutArbitration` becomes callable after `deadline`, emitting `ArbitrationTimedOut`.

  Out of scope and timeout have the same economic effect: fees are refunded and bonds are returned in full. `context` is either the ruling text itself or an IPFS CID pointing to it.

- **Sentinel votes.** Each revealed vote carries a `reason` string that the protocol does not validate. By convention, `secure` votes have an empty reason and `insecure` votes cite a rule such as `R-4.1` (§ 2.14).
- **Two chains.** `Consensus` and `SentinelOracle` run on Gnosis Chain (chain ID 100). The disputed Safe can be on any network the Charter covers. Read the Safe's state (codehash, owners, modules, transfer history) on the Safe's own chain, at the last block before the proposal time. Evidence must not include effects of the transaction itself or anything that happened later (§ 2.8, § 3.6).
- **Deployments.** Compile the addresses of the oracle-backed `Consensus` and `SentinelOracle` deployments into `arbot` as defaults. The Beta network's `Consensus` (`0x223624cBF099e5a8f8cD5aF22aFa424a1d1acEE9`) predates the oracle flow and is not compatible with it.

### Arbitration Charter

The effective Charter is the IPFS document referenced by the ENS name `charter.safenet-gov.eth`, which `SentinelOracle.charterEns()` also returns. The version that applies is the one the name pointed to _when the transaction was proposed_ (§ 2.12, § 5.4). Earlier versions can be identified from the name's onchain update history. Any copy on GitHub is a draft and is not authoritative. `arbot` must resolve and fetch the applicable version for every request. The points below summarize one draft version; always apply the fetched text.

- **Scope (Article I).** Networks: Ethereum Mainnet (1), Arbitrum One (42161), and Gnosis Chain (100). Only official Safe smart account versions 1.3.0, 1.4.1, and 1.5.0 are covered, from `github.com/safe-fndn/safe-smart-account` (§ 2.1). A request outside these networks or the Charter's transaction-security scope is `out of scope` (§ 3.9). Calls to the Safenet Guard's `announceTransaction` and `cancelAnnouncement` are auto-allowed by the Guard and never reach sentinels (§ 2.18).
- **Outcomes.** An in-scope ruling is only ever `secure` or `insecure`; there is no `unknown` (§ 2.13). A transaction is secure only if it passes every Article IV rule (§ 3.7). If admissible evidence on a material question is genuinely evenly balanced, the ruling is `insecure` (§ 3.8). A ruling is valid only if it is submitted within four weeks of arbitration starting (§ 2.17). A transaction that entered arbitration is never attested, whatever the ruling.
- **Evidence.** Admissible evidence is onchain data at proposal time, protocol-recorded purpose, and public, attributable, reasonably verifiable offchain context (§ 3.3). Private, unverifiable, or after-the-fact user-intent statements are excluded (§ 3.5). Offchain security evidence may be used if it was available before the ruling. It must be cited with its source, observation time, and reliability basis, and never presented as if it had been available earlier (§ 3.4, § 3.6).
- **Rules (Article IV).** Rule IDs follow the pattern `R-<article>.<n>`.

  | Rule | Kind | Summary |
  | --- | --- | --- |
  | R-4.1 | Deterministic | Settings change (singleton, modules, fallback handler, guard, module guard, owners, threshold) is insecure. **Exception:** all of the following hold — non-batched `execTransaction`, `to` is the Safe itself, `operation = 0`, `value = 0`, canonically ABI-encoded, and the call is `disableModule(...)` or `setFallbackHandler(address(0))`. |
  | R-4.2 | Deterministic | A delegatecall that changes any Safe storage slot is insecure. **Exception:** writes to the `signedMessages` mapping. |
  | R-4.3 | Principle | Sending value to a recipient outside the expected target set (§ 2.4) is insecure. Address poisoning is a key signal. |
  | R-4.4 | Principle | Granting approvals or operator rights to an address outside the expected target set is insecure. |
  | R-4.5 | Mixed | A functionally unlimited approval is insecure. A max `uint256` ERC-20 approval fails immediately. Other amounts are weighed against token supply and the interaction (§ 2.5). |
  | R-4.6 | Principle | Interacting with a target shown by admissible evidence to be malicious, compromised, or high-risk is insecure. |

- **Precedent (Article V).**
  - A prior ruling is rebuttable precedent: a later ruling must identify the closest analogous prior ruling and either follow it or explain why it departs (§ 5.1, § 6.4).
  - A novel fact pattern must be reasoned by analogy and flagged for possible Charter amendment.
  - A ruling explanation must identify the Safe and network, the Charter version, at least one rule ID, the precedents applied, the material evidence, any recusals, and the reason (§ 5.2).
  - The onchain `DisputeResolved` record is authoritative. Offchain indexes, including this corpus, are informational only.

## Architecture

### Invariants

1. **The bot holds no keys and never writes onchain.** It only produces drafts. Humans on the Council review a draft and submit the ruling through the Arbitrator Safe.
2. **Deterministic rules live in Go code, not prompts.** A deterministic check returns either `insecure` with a rule citation or `abstain`. It never returns `secure`, because passing one rule says nothing about the others (§ 3.7). Any `secure` outcome comes from the LLM stage and is reviewed by a human.
3. **LLM output is always a draft.** It must cite rules, name the precedents it applied or distinguished, and meet the ruling-explanation requirements of § 5.2: Safe and network, Charter version, rule IDs, material evidence, and the reason. Only admissible evidence may be used (§ 3.3–3.5).
4. **Sane defaults, no required configuration.** RPC URLs come from <https://chainlist.org/rpcs.json>. IPFS content (Charter versions, `context` CIDs) is fetched through public gateways. Overrides may exist, but no command may require them.

### Components (planned)

- **`cmd/arbot`**: the single top-level Go CLI. Every capability lives here as a subcommand: fetching a request and its transaction, resolving the applicable Charter, reading a Safe's history, verifying that an account is an official Safe (codehash of a § 2.1 version at a given block), running deterministic checks, and indexing and linting the corpus. Put logic in `internal/` packages and keep `cmd/` thin.
- **Deterministic checks**: one Go check per Charter rule, or per part of a rule, each tagged with its rule ID. Add a check only for cases the Charter makes unambiguous. For example, R-4.1 makes a settings change such as `addOwnerWithThreshold` insecure unless it matches an allowed exception.
- **Agent layer**: `claude` and `codex` for now. Agents get their data by shelling out to `arbot`. They must not reimplement chain reads or checks in ad-hoc scripts. Agent skills live in `skills/<name>/SKILL.md`, with `.claude/skills` and `.codes/skills` symlinked to that directory. Examples: a skill that drafts a ruling for a request, and a skill that turns a precedent pattern into a new deterministic check.
- **Future direction**: using a general-purpose coding agent is a stopgap. The plan is to move to a purpose-built arbitration agent (for example, on Google's ADK) with a narrow toolset backed by the same `arbot` commands. Build tooling so that move is easy: stable CLI output (JSON where machines consume it) and no dependence on one agent's quirks.

### GitHub workflows (planned)

- **Arbitrate** (`workflow_dispatch`, input `request_id`): fetches the request, then runs the deterministic checks. If a check fails, it drafts the ruling from that failure. Otherwise it runs the agent with the relevant corpus precedents. It then opens an issue with the classification and the draft ruling for Council review.
- **Index** (daily `cron`): scans `DisputeResolved` and `DisputeOutOfScope` events since the last indexed block, fetches any IPFS `context`, and writes corpus records. It opens a PR so a human can review any LLM-written summaries or tags.

## Rulings Corpus

`corpus/` holds one Markdown file per onchain-recorded arbitration outcome, named `corpus/<requestId>.md`. Every file starts with a header that has **exactly 13 lines**. Agents can therefore scan all past cases with `head -n 13 corpus/*.md` and then read in full only the cases that look relevant.

```
---
request: 0x<requestId>
outcome: secure | insecure | out-of-scope
rules: R-4.1, R-4.5            (or -)
chain: <Safe chain ID>
safe: 0x<Safe address>
charter: <IPFS CID of applicable Charter version>
proposed: <ISO-8601 UTC proposal time>
record: 0x<Gnosis Chain tx hash of resolveDispute/markOutOfScope>
precedents: <comma-separated request IDs applied or distinguished, or ->
tags: <comma-separated kebab-case fact-pattern keywords>
summary: <one line describing the fact pattern and holding>
---
```

Rules for the header:

- Every key is always present, in this order, on a single line. Use `-` when a field is empty.
- Headers are generated and validated by `arbot`, not written by hand.
- If the header format changes, change the line count here, in the tool, and in the skills together.

The body follows the header. It contains the decoded transaction, the sentinel votes and reasons, the verbatim ruling `context` (or the fetched IPFS document), and any analysis notes.

## Development

Repo-wide commands are recipes in the [Justfile](./Justfile). Go builds `arbot`, and Node.js runs Prettier, which formats Markdown using the settings in `.prettierrc`.

```sh
just fix                                     # gofmt + prettier --write
just check                                   # gofmt check, go vet, prettier --check
go build ./...
go test ./...
go test ./internal/<pkg> -run '^TestName$'   # single test
go run ./cmd/arbot --help
```

Run `just fix`, `just check`, and the tests before committing. Tests that need chain access should run against pinned blocks so their results stay reproducible.
