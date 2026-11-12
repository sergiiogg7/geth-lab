# Plan: geth-lab

Portfolio project P3 of `JOBS.md`, re-planned on 2026-11-12 from the Besu/Java version (superseded; ideas on chaos scenarios,
runbook format, postmortems and the contribution track were reused). Type: operate, extend and stress a real execution client (Geth, in Go) and prepare an
upstream contribution track. No frontend.

Rules inherited from `PROGRESS.md`: one repo at a time; no commit or push without the owner's approval; no `Co-Authored-By` lines; never claim an audit;
nothing posted externally (the contribution track is research and documentation only).

| Date | Change |
|---|---|
| 2026-11-12 | Plan written for Geth/Go (client, language and shape changed from the Besu/Java plan). |
| 2026-11-12 | Built autonomously to a demonstrable state in one session; status table and deviations below. |
| 2026-11-12 | Second pass: observability verified natively, C3 root cause confirmed, C7 memory cap run, chaos workflow, upstream study, licence. See the status rows below. |

## 1. Goal and scope

**Goal:** show that the owner can operate a real Ethereum execution client, extend it with Go code, stress it with failure scenarios that produce
postmortems, and has a documented path to contribute upstream. A recruiter or tech lead can clone, run in minutes, see a demo, and read.

**In scope:** single `geth --dev` node (and `labnode`); scripts; JSON-RPC checks with `cast`; Go tools (`loadgen`, `labnode`, `watch`, `nodectl`);
metrics catalogue and Prometheus/Grafana/alert files; chaos scenarios C1 to C7; code tour; contribution research; demo and docs.

**Out of scope:** multi-node networks (Geth dropped Clique/PoA multi-node private networks in 1.14; a PoS devnet needs Kurtosis/Docker and a consensus client per node);
mainnet or testnet operation; production hardening; anything posted to GitHub.

## 2. Verified facts that shaped the design

| Fact | Evidence | Consequence |
|---|---|---|
| go-ethereum v1.17.10 is the latest release (2026-10-23). | `gh api releases/tags/v1.17.10`; module in cache. | Pinned in `go.mod`; `geth` built from the same module (`docs/TOOLCHAIN.md`). |
| `go install .../cmd/geth@v1.17.10` works with `CGO_ENABLED=0`. | Built in about 3 minutes with `-p 2`. | No release-archive fallback needed. |
| `geth --dev` mines on demand (period 0) through a txpool subscription in `simulated_beacon_api.go`, or every N seconds. | Observed and read. | Scenarios use `--dev.period` to build backlogs. |
| The dev chain activates forks up to Bogota at time 0 and uses two-dimensional gas. | Fork banner; `OnOpcodeV2` costs. | The tracer uses the V2 hooks and reports `stateGas`. |
| A hard kill of a persistent dev node rewinds the head and wedges block production. | C1, C2, demo. | Became the headline finding and shaped the runbook and alerts. |
| This machine has 7.9 GB RAM, about 1 GB free, no Docker, no real Python. | Checked. | One node at a time; Prometheus/Grafana untested; no Python anywhere. |

## 3. Design as built

* **Node under test:** stock `geth --dev` and `labnode` (embeds `node.New` + `eth.New` + `catalyst.NewSimulatedBeacon`). Same flag names, started by `scripts/start-node.sh` or `internal/nodeproc`.
* **Extension approach (deviation-free, preferred option of the brief):** embed Geth as a library. `lab_` namespace through `node.RegisterAPIs`; tracer through
  `tracers.DefaultDirectory.Register` and `core/tracing.Hooks`. The fallback (replay program + RPC proxy) was not needed.
* **Pure logic separated from I/O** (`internal/chainwatch`, `internal/labapi` helpers, `internal/loadgen` classification and percentiles) so it is unit tested without a node.
* **Process control:** `internal/nodeproc` (Go tests) and `cmd/nodectl` (scripts) give graceful stop on Windows by giving the node a private hidden console and sending Ctrl-Break.
* **Observability:** Geth's own Prometheus endpoint, `scripts/metrics-scrape.sh`, `watch -serve` status page, plus untested Prometheus/Grafana files.

## 4. Phases, exit criteria and status

| # | Phase | Exit criteria | Status |
|---|---|---|---|
| 0 | Toolchain and spikes | `geth` 1.17.10 built; flags verified with `--help`; metrics endpoint scraped; `docs/TOOLCHAIN.md` | **done** |
| 1 | Node ops | start/stop/kill/reset scripts; smoke test passes against geth and labnode; `docs/NODE-OPS.md` | **done** (smoke run on both) |
| 2 | Go tools | `loadgen`, `labnode` (`lab_*` + tracer), `watch` build and are tested | **done**: 18 unit tests + 1 e2e test |
| 3 | Observability | real metric names in `docs/METRICS.md`; Prometheus/Grafana/alerts run and proven through APIs | **done** natively (Prometheus 3.16.0, Grafana 13.3.1; `docs/OBSERVABILITY.md`); 4 alerts driven to firing; Docker Compose still **untested** |
| 4 | Chaos scenarios | C1..C7 automated, each with a postmortem built from recorded numbers | **done**: C3 root cause confirmed (C3b), C7 memory cap run (Windows only); disk-full analysed, not reproduced |
| 5 | Code tour | `docs/geth-code-tour.md` with verified anchors | **done** |
| 6 | Contribution track | rules recorded, verified open candidates, fallback repos | **done** (research only; `docs/upstream/` studies #28222 read-only; nothing posted, no PR code built) |
| 7 | Demo and docs | demo run for real; README, RUNBOOK, SECURITY, CLAUDE, CI | **done**; CI green on push; chaos workflow added (result below) |
| 8 | Owner study and decisions | read the tour and postmortems; decide on the follow-ups in section 6 | **not started (owner)** |

## 5. Deviations from the brief and from the old plan

| Deviation | Why |
|---|---|
| Single node instead of a multi-node network. | Geth has no multi-node Clique; a PoS devnet needs Docker (not available). Stated in the brief. |
| C7 disk-full not reproduced. | Memory cap was run safely (Job Object, 700 MB); filling a disk can damage the shared system drive. Analysis only. |
| Prometheus/Grafana verified through APIs only. | No browser for screenshots; Docker Compose path untested. |
| `nodectl` helper binary added. | A bash script cannot send Ctrl-C to a background Windows process; broadcasting to the shared console interrupted the calling shell too, so the node gets a private console. |
| Chaos tests live in Go with a build tag instead of bash scripts. | Easier to assert on numbers and to reuse `loadgen`; `scripts/chaos.sh` wraps them. |
| `lab_fork` keeps the old branch as a side chain; `debug_setHead` deletes it. | Both end up as REWIND then REORG for a follower (C5). Documented rather than hidden. |
| Hypotheses were wrong several times (C1, C2, C3, C5, C7-B). | Recorded as such in the postmortems. C3's first root cause (silent eviction) was wrong and was corrected by C3b. |
| MIT licence added by the autonomous pass. | A default choice, flagged for the owner to confirm. |
| No `git commit` was made; `git init` only. | Rule. |

## 6. Risks, known issues and next steps (for the owner)

| Item | Detail |
|---|---|
| Version sensitivity | Findings describe v1.17.10. Two open upstream PRs (#36037, #35567) may change the C1 behaviour; re-run `scripts/chaos.sh C1` against them. |
| Linux runs | Linux paths are exercised by `.github/workflows/ci.yml` (unit + e2e) and `chaos.yml` (chaos suite); chaos run 43274596212 on ubuntu-24.04 passed (about 12.5 minutes, all scenarios PASS, C7 SKIP) and reproduced the C1 wedge and the C3b numbers. C7 skips on Linux (no cgroup counterpart yet). |
| C3 root cause | Confirmed (local-transaction tracker, C3b). Open: why the retry yield is 64 per minute (suspected `--txpool.globalslots`); split of `txpool_valid`. |
| Pending items | C7 hang explanation (candidate: exit path needs memory), disk-full on a disposable volume, committed-memory alert relative to a limit, Linux memory cap, running the `docs/upstream/REPRODUCTION-RECIPE.md` against a PR branch, deciding whether to post `DRAFT-comment-28222.md`. |
| Timings | One 8 GB laptop with other work running. |
| Next steps needing the owner's go | (1) confirm the MIT licence; (2) no tags or releases were made, decide on a first release; (3) upstream follow-ups listed in `docs/CONTRIBUTIONS.md` section 2; (4) optionally test the Docker Compose path on a Docker machine. |
| Study order | `docs/geth-code-tour.md`, then `docs/postmortems/C1`, then the Go code: `internal/chainwatch`, `internal/labapi`, `cmd/labnode`, `internal/loadgen`. |

## 7. Definition of done (for this autonomous pass)

* `go vet ./...` clean; `go test ./...` green; e2e green; chaos suite green (6 scenarios) - see README "Tests".
* `scripts/demo.sh` ran end to end with all assertions; output in `docs/DEMO.md`.
* Docs complete and honest about what is untested or not done.
* `PROGRESS.md` and `JOBS.md` are **not** edited here; the parent updates them.

## 8. What the owner can say in an interview

* "I embedded Geth as a library, added a custom RPC namespace and a native EVM tracer, and I can explain how Geth registers both."
* "I killed a Geth dev node under load and found that the default configuration loses the chain and stalls block production while RPC still answers; I traced it to the path-database journal, the sync-mode rule and the Engine API refusing payloads, and I found the one configuration that survives."
* "I measured that a node can accept a transaction and silently evict it later, and wrote down which counters show it."
* "I have a source tour of Geth with verified line anchors and a researched, not yet acted on, upstream contribution plan."
