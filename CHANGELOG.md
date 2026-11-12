# Changelog

All notable changes to this project, in the style of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Derived from the Conventional Commits
history. No version has been tagged or released; entries are grouped by the date of the work.

## [Unreleased]

### Added
- `scripts/observability-up.sh` / `observability-down.sh`: Prometheus and Grafana run natively, without Docker; `cmd/obscheck` verifies targets, every dashboard query (directly and through Grafana), alert rules and provisioning through their APIs.
- Alerts `HeadWentBackwards` and `TxPoolTrackerBacklog`; a dashboard panel for `txpool_local`.
- `docs/OBSERVABILITY.md` with real API output and four alerts driven to `firing` by controlled failures (`docs/evidence/`).
- C3b test (`test/chaos/c3b_locals_test.go`) that confirms the root cause of C3.
- C7 memory-cap scenario and postmortem; `internal/nodeproc.LimitMemory` (Windows Job Object), `nodectl start -mem-mb`, `start-node.sh --mem-limit-mb`.
- `.github/workflows/chaos.yml`: the chaos suite on Linux, weekly and on demand.
- `docs/upstream/`: read-only study of go-ethereum #28222 and its open PRs, a reproduction recipe (not run) and a draft comment (not posted).
- MIT `LICENSE`, CI/licence badges, repository description and topics.

### Changed
- C3 postmortem rewritten: the cause is the local-transaction tracker in `SendTx`, not a silent eviction.
- `NoPeers` no longer fires on `--dev` nodes; the dashboard no longer says "untested".
- `ci.yml` also vets the build-tagged tests; the on-demand chaos job moved to `chaos.yml`.
- The chaos harness scrapes metrics with a timeout.
- README, RUNBOOK, METRICS, SECURITY, PLAN and CONTRIBUTIONS updated to match.

### Fixed
- `observability-down.sh` stops Prometheus and Grafana by pid even when they ignore the interrupt.

## 2026-11-12: first published state

### Added
- Node scripts (`start-node.sh`, `stop-node.sh`, `reset.sh`, `smoke.sh`, `metrics-scrape.sh`, `demo.sh`, `chaos.sh`) and `cmd/nodectl` for graceful stop on Windows.
- `internal/devnet`, `internal/nodeproc`, `cmd/loadgen`, `cmd/watch` (reorg, rewind, stall detection), `internal/gasprofile` (native tracer), `cmd/labnode` with the `lab_` RPC namespace.
- Chaos scenarios C1 to C6 with postmortems, an end-to-end test, a metrics catalogue, Prometheus/Grafana/alert files (then untested), runbook, code tour and contribution research.
- GitHub Actions workflow for vet, gofmt, build, unit and e2e tests.
