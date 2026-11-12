# Contribution track (research only)

**Status: nothing has been posted, claimed, forked or opened.** Everything below is reading and proposals that need the
owner's explicit go. Checked on 2026-11-12 against the public GitHub API (read-only calls) and the v1.17.10 source.

## 1. The rules of go-ethereum, as written

Sources: `.github/CONTRIBUTING.md`, `README.md` ("Contribution" section), `AGENTS.md` (root of the v1.17.10 tree),
`.github/workflows/*`, `.github/CODEOWNERS`, `.golangci.yml`, `.github/stale.yml`, `.github/ISSUE_TEMPLATE/*`.

| Topic | Rule | Source |
|---|---|---|
| Workflow | Fork, fix, commit, open a PR against **`master`**. For complex changes, talk to the core devs first (the README says Discord; CONTRIBUTING.md still says Gitter and is out of date). | `CONTRIBUTING.md`, `README.md` |
| Formatting | `gofmt` (and `goimports`) on every modified file. | CONTRIBUTING; AGENTS.md "Pre-Commit Checklist 1" |
| Comments | Code documented per Effective Go commentary rules. | CONTRIBUTING |
| Commit message | `<package(s)>: description`, description short and lowercase, e.g. `core/vm: fix stack overflow in PUSH instruction`, `eth, rpc: make trace configs optional`. | CONTRIBUTING; AGENTS.md |
| PR title | Same format, `<top-level paths, comma separated>: description`. **Enforced by CI** (`.github/workflows/validate_pr.yml`): the title must match `^([\w\s,{}/.]+): .+` and every directory named must exist in the repo. A title starting with `feat:`, `fix:` or `chore:` (conventional-commits style) is treated as spam and the **PR is closed automatically**. | `validate_pr.yml` |
| Scope | "Keep changes minimal and focused", no unrelated refactors, no dependency changes unless required, no binaries in commits. | AGENTS.md |
| Tests | Run `go run ./build/ci.go test -short` while iterating and the full `go run ./build/ci.go test` before committing; CI runs tests on Linux, 32-bit and Windows. | AGENTS.md; `go.yml` |
| Build | `make all` builds every command (including `keeper`). | AGENTS.md |
| Lint | `go run ./build/ci.go lint` (golangci-lint: govet, staticcheck, revive subset, misspell, unused, usetesting, copyloopvar...). | AGENTS.md; `.golangci.yml` |
| Generated code | `go run ./build/ci.go check_generate` (`gen_*.go`); install generators with `make devtools`. | AGENTS.md |
| Dependencies | `go run ./build/ci.go check_baddeps`. | AGENTS.md |
| CLA / sign-off | **None found.** No CLA bot, no DCO check among the workflows (`freebsd.yml`, `go.yml`, `validate_pr.yml`), and CONTRIBUTING does not mention one. (Contrast: Besu uses DCO.) | workflows, CONTRIBUTING |
| License | Library (everything outside `cmd/`) LGPL-3.0, `cmd/` GPL-3.0. | README |
| Reviews | Owners by path in `CODEOWNERS` (examples: `core/`, `eth/`, `trie/`, `triedb/`: @rjl493456442; `internal/ethapi/`: @fjl, @lightclient; `eth/catalyst/`: @MariusVanDerWijden, @lightclient, @fjl, @jwasinger; `node/`, `rpc/`, `p2p/`: @fjl). Triage labels: `status:triage`, `status:marinating`, `status:on-hold`, `status:merge-at-meeting`. Stale bot: issues are marked stale after 366 days of inactivity and closed 42 days later (`stale.yml`). | `CODEOWNERS`, `stale.yml`, label list |
| Bug reports | Template asks: geth version, CL client, OS, expected / actual behaviour, steps to reproduce, logs as text. Label `type:bug` is applied by the template. | `ISSUE_TEMPLATE/bug.md` |
| Docs site | Contributions to geth.ethereum.org go to the **`website`** branch. | README |
| Labels worth filtering | `good first issue` (currently **0 open issues**), `help wanted` (6 open, all old), `type:docs`, `type:bug`, `performance`. | `gh api repos/ethereum/go-ethereum/labels` and searches |

AI-assisted work: `AGENTS.md` exists in the repo, i.e. the maintainers expect coding agents and tell them to keep changes
minimal. Every line submitted should still be understood by the owner (the owner's decision, restated here).

## 2. Candidate issues (all "proposed, needs owner's go")

Method: listed open issues by label and by search, then searched for open pull requests mentioning each number.
**Four attractive bugs already have an open PR and were therefore dropped**: #35482 `net_listening` (PR #35504),
#35479 `eth_simulateV1` gas-limit check (PR #35523), #35483 `admin_startWS` (PR #35503), #34628 Prometheus
`ResettingSample` (PR #34658), #33744 txlookup cleanup on rewind (PR #33745). The following had no open PR on
2026-11-12 and no assignee.

| # | Link | Title | Why tractable | What I would change | Risk |
|---|---|---|---|---|---|
| 1 | https://github.com/ethereum/go-ethereum/issues/35481 | `debug_getModifiedAccountsByHash/Number` omit accounts deleted between the compared blocks | Reporter supplies a deterministic failing test (`eth/api_debug_deleted_fixture_test.go`) and the cause: `getModifiedAccounts` uses a one-directional `trie.NewDifferenceIterator`, so accounts that exist only in the old trie are never reported. 0 comments. Verified in v1.17.10: `eth/api_debug.go:380 getModifiedAccounts` calls `trie.NewDifferenceIterator(oldIt, newIt)` at line 402. Tests go next to `eth/api_debug_test.go`. | Iterate the difference in both directions (or compare old-only leaves) and add the reporter's test. Small diff with a regression test. | Maintainers may prefer another approach; ask first. |
| 2 | https://github.com/ethereum/go-ethereum/issues/33233 | A potential goroutine leak in `core/state/snapshot/generate.go` | Concrete: a blocked channel send after a generation failure under a specific interleaving; reporter lists a reproduction recipe with injected sleeps. Two people confirmed it still applies (last comment 2026-04-17). | Make the blocking send select on the abort/stop channel; add a test only if the maintainers want one. | Needs reading the snapshot generator carefully; touches `core/state/` (owner @rjl493456442). |
| 3 | https://github.com/ethereum/go-ethereum/issues/31508 | Node doesn't shutdown gracefully during block import | Maintainer (rjl493456442) already described the approach: propagate the interrupt into chain insertion instead of checking only between batches. Directly related to lab scenario C2 (graceful stop timing). | Pass a stop signal to `BlockChain.insertChain` (`core/blockchain.go:1917`) so large imports abort on `Got interrupt`. | **Re-verify first**: v1.17.10 already has `procInterrupt` (`core/blockchain.go:371`) and `errInsertionInterrupted` (line 123), so this may be partly fixed since the report (v1.15.6). Medium risk, core path, needs design agreement. |
| 4 | https://github.com/ethereum/go-ethereum/issues/31794 | The prestate tracer reports that the code was loaded when only the balance is read | Maintainer answered it is by design and `disableCode: true` exists. A documentation or godoc change in `eth/tracers/native/prestate.go` that states this (and where the website documents tracers, on the `website` branch) would close the question. | Docs/comment only: `eth/tracers/native/prestate.go:82` has the `DisableCode` option; the godoc and the website tracer page could say that the whole account is reported and how to exclude code. Ask on the issue whether a docs PR is welcome. | Lowest risk; may be judged unnecessary. |

### Lab-derived finding, strongest candidate: dev node wedged after `kill -9` (not yet reported as such)

Reproduced twice (C1, C2, and the demo): a `geth --dev --datadir D` that is killed and restarted rewinds the head to
genesis ("Head state missing, repairing"), then `eth/downloader/syncmode.go:49` logs `Switching from full-sync to snap-sync
reason="snap-sync incomplete"` and `eth/catalyst/api.go:1095` ignores every payload the simulated beacon produces
("Ignoring payload while snap syncing"). The chain accepts one block and then never advances. The closest existing issue is
**#28222** "Geth should handle genesis block with default syncmode" (open, labelled help wanted), which has two open
fix PRs: #36037 (`eth/catalyst: permit payload import when parent state is local and disable snapsync at genesis`) and #35567
(`eth/downloader: switch to full sync when progressing past genesis`). Whether they fix the dev-node crash case is untested.
Useful, honest next steps (each needs the owner's go): build one of those PR branches locally and re-run
`go test -tags chaos -run TestC1 ./test/chaos` against it; then comment on #28222 with the reproduction and the result.
The reproduction is one command: `scripts/demo.sh` section 7, or the C1 test.

Related existing issues (context, not for taking): #31586 "Geth just keeps rewinding after crash in hash mode" (17 comments),
#34744 "Error in block freeze operation" (21 comments), #31327 "SimulatedBeacon can spinloop when txpool closing and doCommit
closing race" (assigned).

## 3. Smaller lab observations that could become issues or doc fixes (unfiled)

* `--dev.period` help text says "0 = mine only if transaction pending", but `SimulatedBeacon.Start` does nothing for period 0 (`eth/catalyst/simulated_beacon.go:157`); on-demand mining is implemented in the API constructor `newSimulatedBeaconAPI` (`simulated_beacon_api.go:35`). The behaviour is right, the code placement is surprising.
* The dev banner says "Starting Geth on Ethereum mainnet..." in dev mode (cosmetic).
* RPC-submitted transactions are accepted (hash returned) and then silently evicted when the pool is full (C3): worth a docs note on `--txpool.*` or on `eth_sendRawTransaction`.
* `debug_setHead` followed by new dev blocks looks like a reorg to followers (C5): fine, but not documented for dev mode.
* A fresh node answers receipt lookups with `-32000 transaction indexing is in progress` for a short time after start (found by `scripts/smoke.sh`).

## 4. Fallback repositories (verified to exist and be active on 2026-11-12)

| Repo | Link | Notes |
|---|---|---|
| foundry-rs/book | https://github.com/foundry-rs/book | 23 open `good first issue`s, all documentation: e.g. #813 "Document `testFail_` as anti-pattern", #820 "Document that `vm.expectRevert` should not be used for low-level calls", #1289 "document best practices of measuring gas when using transient storage". The lab already uses `cast` daily. |
| foundry-rs/foundry | https://github.com/foundry-rs/foundry | No open `good first issue` or `help wanted`; large and fast-moving; good for bug reports with reproductions from this lab. |
| ethereum/execution-apis | https://github.com/ethereum/execution-apis | The JSON-RPC specification. The lab's `lab_`, `txpool_` and error-code observations are material for spec issues. No good-first-issue label open. |
| ethereum/execution-specs | https://github.com/ethereum/execution-specs | Python reference implementation; default branch `forks/bogota`; 1 `help wanted` open. (The old `ethereum/execution-spec-tests` repo is **archived** since 2026-07.) |
| ethereum/hive | https://github.com/ethereum/hive | Client integration test harness. Needs Docker. |
| ethereum/go-ethereum website | branch `website` of the main repo | Docs fixes: #31764 "Private network without kurtosis" (labelled `type:docs`) is exactly this lab's single-node finding. |

## 5. What the owner could say

"I ran a Geth dev node through kill -9, flood and RPC-abuse scenarios and wrote postmortems; one of them reproduces a
dev-mode wedge that matches open issue #28222, and I have notes on the PRs that try to fix it." (Only true after the owner
has actually done the follow-up in section 2; today the accurate sentence is "I found and documented it".)
