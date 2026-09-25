---
name: add-check
description: Add a deterministic check that classifies Safenet requests as insecure, out-of-scope, or secure under the Safenet Arbitration Charter. Use when turning a Charter rule, part of a rule, or a precedent pattern into Go code that `arbot classify` runs.
---

# Add a Deterministic Check

A check is a `check` value in `internal/checks` that decides a class of calls that a Safe makes, by direct application of the Charter, without judgment. It has the verdict for its class, the rule ID for an `insecure` verdict, a short description of the class, and a function that reports whether a call is in the class. `arbot classify` runs the checks in the `checks` variable in `internal/checks/checks.go`:

- the `out-of-scope` checks, then the `insecure` ones, run on every call, and the first check that matches a call decides the request;
- otherwise, the request is `secure` if a `secure` check matches every call, and unclassified if any call matches none.

`arbot classify -list` lists them in that order. Requests that every check abstains on go to the LLM stage, so a check that abstains when in doubt costs little, and one that decides wrongly misleads the Council.

## Inputs

Before writing any code, make sure you have:

- **The case**: the class of calls the check decides, and the verdict it returns for them: `insecure` under a rule ID such as `R-4.1`, `out-of-scope` under § 3.9, or `secure`.
- **The Charter text**: the rule, its exceptions, and the definitions it uses, from the Charter version that applies to the requests the check is for. Fetch it with `arbot charter`, or `arbot charter -request-file <path>` for a request that `arbot info -json` wrote.
- **Example requests**, if any: IDs of requests that the check should decide, and of similar ones that it should abstain on. Past rulings in `corpus/` are a good source: scan them with `head -n 13 corpus/*.md`.

Ask the user for any input that the request doesn't settle. Don't guess a rule ID, an exception, or a verdict.

## Is the Case Deterministic?

Check that the case can be decided in code before writing it (invariant 2 in `AGENTS.md`, and § 3.7 of the Charter):

- **`insecure`**: the rule must be a deterministic one, in Part A of Article IV, or a codified rule that the Charter treats as deterministic. Principle-based rules, such as the target-manipulation rules of Part B, need an evidence-grounded Council finding, so a check can't decide them. The check must also rule out every exception to the rule. If an exception can't be ruled out from the data, abstain.
- **`out-of-scope`**: the request must be outside the scope or the networks of Article I, as a matter of fact rather than judgment. An example is a Safe on a network that the Charter doesn't cover.
- **`secure`**: a transaction is secure only if it passes every rule of Article IV. A `secure` check therefore matches only a class of calls that has no effect that any rule considers, whatever the other calls in the transaction do, so that a transaction whose calls all match `secure` checks passes every rule. An example is a call to `signMessage` on the Safe. A call that is only secure on its own, such as a transfer that a principle-based rule could weigh with others, doesn't qualify. A check that only shows that its own rule isn't broken must abstain.

If the case needs judgment anywhere, stop and tell the user why it isn't deterministic, rather than writing a check that approximates it.

A check receives one call at a time, with the `safeID` of the Safe that makes it, its address and chain ID, from `internal/checks/transaction.go`. The calls are those that the Safe transaction makes, followed by a synthetic call that pays the largest gas refund that the transaction allows, if any. Delegate calls to MultiSend and MultiSendCallOnly are decoded into the calls that they make, a call that is sure to revert is replaced by a placeholder from `revertingMultiSend`, and a MultiSend that makes no calls by one from `emptyMultiSend` (see `expandTransactionCalls` in `internal/checks/multisend.go`), so don't decode MultiSend in a check. A check sees neither the Safe transaction's own fields nor the other calls. If the case depends on the other calls in the transaction, it can't be a check. If the case needs other data, such as the Safe's owners or code at `safeBlock`, stop and agree with the user on how to extend the `check` type first. A check that reads such data should cache it by `safeID`, since it runs once for each call. Any such data must come from the Safe's chain at `safeBlock`, or from Ethereum Mainnet at `ethereumBlock`, and never from later blocks (§ 2.8 and § 3.6).

## Steps

1. **Write the check** in its own file, `internal/checks/<name>.go`, where `<name>` is a lowercase, underscored name for what it decides, such as `settings_change.go`. Declare it as a `check` variable, with a comment that quotes or paraphrases the rule, cites its ID and sections, and names the Charter version it was written against (its IPFS CID). Its fields are:
   - `verdict`: `OutOfScope`, `Insecure`, or `Secure`;
   - `rule`: the rule ID, such as `R-4.1`, for `Insecure` only;
   - `description`: a short, lowercase description of the class, such as `calls addOwnerWithThreshold on the Safe`. `arbot classify` prints it after the verdict and the rule, on one line, for every request in the class, so it must hold for all of them;
   - `fn`: a function that reports whether a call is in the class. It returns `false` whenever the call isn't clearly in the class, so that the check abstains. It returns an error only when it can't read them, never to express a verdict. It decodes call data with `internal/solabi`, and compares addresses and hashes as `ethrpc` values rather than strings.

   If one rule decides several classes, such as different settings changes, write one check per class, each with its own description.

2. **Register the check** in the `checks` variable. `arbot classify` groups the checks by verdict, so their position only orders them among the checks with the same verdict. A request that matches two checks with different verdicts is a bug in one of them, not a matter of order. `TestChecks` checks that every check has a verdict, a description, a function, and a rule only if it is `insecure`.

3. **Test the check** in `internal/checks/<name>_test.go`, with a table of calls, or of requests built with the `request` helper in `checks_test.go` to go through `components`. Cover:
   - each way the request can be in the class;
   - each exception, and each near miss, such as a different selector, target, or operation, where the check must abstain;
   - malformed call data, which must abstain or return an error rather than decide.

   For example requests, save each one with `arbot info -json <request-id> | jq . > internal/checks/testdata/<request-id>.json`, and test that `Classify` classifies it as expected. The files must stay formatted as `jq .` formats them.

4. **Try it on the example requests** with `go run ./cmd/arbot classify <request-id>`, and check that `go run ./cmd/arbot classify -list` lists the check where you expect it. Compare the verdicts to the requests' onchain outcomes. If a check disagrees with a Council ruling, stop and tell the user rather than changing the check to fit.

5. **Run `just precommit`**, which formats the sources and runs the same checks and tests as CI.
