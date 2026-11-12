# CLAUDE.md (geth-lab)

Hands-on Geth lab: operate a dev node, extend it in Go, break it on purpose, write postmortems. Module
`github.com/sergiiogg7/geth-lab`, go-ethereum **v1.17.10** as a library and as the `geth` binary. Read `README.md`, then
`PLAN.md` (status and deviations).

Parent rules (from `../CLAUDE.md`): work only in this repo; never commit or push without the owner's approval; no
`Co-Authored-By` lines; never claim an audit; post nothing externally (no GitHub issues/comments/PRs/forks; the contribution
track is documentation only).

## Environment (author's machine, Windows + Git Bash)

Start every shell with `source /c/Users/srg/tools/env.sh` (Go 1.27.4, `CGO_ENABLED=0`, `GOFLAGS=-mod=mod`). Foundry is on PATH.
`python`/`python3` are Windows Store stubs that hang: never run them. Memory is tight (about 1 GB free): run one node at a time,
`go build -p 2`, kill background processes when done (`scripts/stop-node.sh --kill`, `taskkill //F //IM geth.exe`).
Use the Edit/Write tools for multi-line file edits (`sed -i` with `c\` and `\t` mangled a file once).

## Commands

```bash
scripts/build.sh                      # bin/{loadgen,watch,labnode,nodectl}
scripts/start-node.sh [--persist] [--client labnode] [--period N] [-- extra flags]
scripts/stop-node.sh [--kill]         # graceful (default) or kill -9
scripts/smoke.sh                      # JSON-RPC checks with cast
scripts/demo.sh                       # ~2 min narrated demo with assertions (output captured in docs/DEMO.md)
scripts/chaos.sh [C1|C2|...]          # chaos scenarios -> out/chaos/*.json (+ node logs)
go vet ./...                          # static checks
go test ./...                         # fast unit tests (no node needed)
go test -tags e2e -count=1 ./test/e2e                      # starts labnode, ~5 s
go test -tags chaos -count=1 -v -timeout 20m ./test/chaos  # all scenarios, ~6 min; -run 'TestC1' for one
```

## Layout

* `cmd/loadgen`, `cmd/watch`, `cmd/labnode`, `cmd/nodectl`: binaries. `internal/*`: the logic (devnet helpers, loadgen engine,
  chainwatch reorg tracker, gasprofile tracer, labapi RPC namespace, nodeproc process harness).
* `test/chaos` (build tag `chaos`), `test/e2e` (tag `e2e`): tests that start real processes.
* `monitoring/` (verified natively, `scripts/observability-up.sh`), `cmd/obscheck`, `docs/` (NODE-OPS, METRICS, RUNBOOK, geth-code-tour, CONTRIBUTIONS, DEMO, TOOLCHAIN, postmortems/).

## Conventions

* English everywhere in the repo. Comments explain *why* and use Java analogies where they help the owner, who is learning Go.
* Pure logic in `internal/` with unit tests; I/O shells in `cmd/`. Keep `internal/chainwatch` free of network code.
* Chaos tests record observations with `result.set`; postmortems quote `out/chaos/*.json`. Never write a number in a
  postmortem that did not come from a run.
* Anything that depends on go-ethereum internals gets the version in a comment or doc. Re-run chaos after upgrading.
* `bin/`, `out/`, `.lab/` are git-ignored.
