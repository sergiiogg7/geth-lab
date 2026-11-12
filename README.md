# geth-lab

[![ci](https://github.com/sergiiogg7/geth-lab/actions/workflows/ci.yml/badge.svg)](https://github.com/sergiiogg7/geth-lab/actions/workflows/ci.yml)
[![chaos](https://github.com/sergiiogg7/geth-lab/actions/workflows/chaos.yml/badge.svg)](https://github.com/sergiiogg7/geth-lab/actions/workflows/chaos.yml)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A hands-on lab around **Geth** (go-ethereum, the most widely used Ethereum execution client, written in Go): operate it, extend it with Go code,
break it on purpose, and write down what happened. Built to be cloned, run in minutes and read.

* **Operate:** scripts to start, stop, kill, reset and smoke-test a `geth --dev` node, Prometheus metrics catalogue from a real scrape, runbook. Prometheus and Grafana run natively (no Docker) and are verified through their APIs, with alerts driven to firing by real failures (`docs/OBSERVABILITY.md`).
* **Extend (Go):** `labnode`, a binary that embeds Geth **as a library** and adds a custom `lab_` JSON-RPC namespace and a native EVM tracer built on
  `core/tracing` hooks. `loadgen`, a transaction flood with latency percentiles and txpool behaviour. `watch`, a head follower that detects reorgs, rewinds, stalls and RPC outages.
* **Break:** seven automated failure scenarios on real node processes (kill -9 under load, graceful vs hard stop, txpool overflow, storage-flag changes,
  rewind/reorg, RPC abuse, a memory-capped node), each with a written postmortem that quotes numbers from the run.
* **Read:** a source tour of go-ethereum v1.17.10 with verified file and line anchors, and a research note for an upstream contribution track.

Not a product, not audited, test keys only (see [SECURITY.md](SECURITY.md)). One node only: Geth no longer supports multi-node PoA private networks, and a
multi-node proof-of-stake devnet needs Docker plus a consensus client per node, which the author's 8 GB machine cannot run
(reasoning in [docs/NODE-OPS.md](docs/NODE-OPS.md)).

## Quickstart (Go 1.27+, Foundry `cast`, Git Bash or any bash)

```bash
go install github.com/ethereum/go-ethereum/cmd/geth@v1.17.10   # 1. the stock client (about 3 min the first time)
scripts/build.sh                                               # 2. loadgen, watch, labnode, nodectl into ./bin
scripts/start-node.sh --client labnode --period 1 && scripts/smoke.sh   # 3. a node with the lab_ namespace, checked with cast
bin/loadgen -mode contract -data 64 -txs 800 -rate 250         # 4. flood it, read the report
scripts/demo.sh                                                # 5. the whole story with assertions (about 2 minutes)
```

Stop with `scripts/stop-node.sh`. Output of a real demo run: [docs/DEMO.md](docs/DEMO.md).

## Architecture

```
  bash scripts                      Go tools (this module)                      the node under test
  ------------                      ----------------------                      -------------------
  start/stop/reset  ---nodectl--->  cmd/nodectl (detached start, Ctrl-Break)
  smoke.sh (cast) -----JSON-RPC--------------------------------------------->   geth --dev          (stock binary, v1.17.10)
  demo.sh / chaos.sh                cmd/loadgen  internal/loadgen -----------+    or
                                    cmd/watch    internal/chainwatch (pure)  +-> labnode            (cmd/labnode)
                                    test/chaos   internal/nodeproc            |     node.New + eth.New + SimulatedBeacon
                                    test/e2e                                  |     + internal/labapi  -> lab_* namespace
                                                                              |     + internal/gasprofile -> "gasProfile" tracer
  monitoring/ (Prometheus, Grafana, alerts: verified natively)  <---- /debug/metrics/prometheus (port 6060)
```

`geth --dev` runs a post-Merge chain with a built-in *simulated beacon* (a fake consensus client), a prefunded account and no networking. `labnode` is the same
thing assembled by hand in about 150 lines, plus the lab's extensions. Both accept the same flags, so scripts and tests start either.

## What each part shows

| Part | Run it | What it shows |
|---|---|---|
| `scripts/*.sh` | `scripts/start-node.sh --persist`, `scripts/smoke.sh` | Node operation: fresh vs persistent datadir, graceful vs hard stop, JSON-RPC checks, metrics scrape |
| `cmd/labnode` + `internal/labapi` | `cast rpc lab_nodeInfo`, `lab_txpoolStats`, `lab_blockFill 0x5`, `lab_fork 0x3` | Extending Geth in Go: embedding the node as a library, registering a custom RPC namespace (reflection-based, like a controller scan) |
| `internal/gasprofile` | `cast rpc lab_gasProfile <txhash>` or `debug_traceTransaction <hash> '{"tracer":"gasProfile"}'` | A native tracer on `core/tracing.Hooks`: gas per opcode and category, including the new state-gas dimension |
| `cmd/loadgen` | `bin/loadgen -scenario gap -txs 300` | Load generation: senders, rate, calldata size, contract calls, nonce gaps, replacements; p50/p90/p99 inclusion latency, txpool high-water marks, error classification |
| `cmd/watch` | `bin/watch -serve 127.0.0.1:8081` | Observability without Docker: block time, gas fill, REWIND/REORG/SKIP/STALL/RPC DOWN, JSON status page |
| `test/chaos` | `scripts/chaos.sh` | Failure injection with measured results; see postmortems |
| `docs/geth-code-tour.md` | read | How Geth is built: startup, node lifecycle, RPC, txpool, simulated beacon, EVM, state DB, sync, metrics |
| `docs/CONTRIBUTIONS.md` | read | go-ethereum's contribution rules as written, verified open issues, fallback repos. Research only: nothing was posted |

## Results in one table

| Scenario | Finding (details in [docs/postmortems](docs/postmortems/README.md)) |
|---|---|
| C1 kill -9 under load | `geth --dev --datadir` does **not** survive it: head 5 to 0, 1,648 mined txs gone, block production stuck in snap-sync mode; RPC still answers in 0.5 s. `--gcmode archive --state.scheme hash` loses nothing. |
| C2 graceful vs kill | Graceful stop 77 ms and fully functional; kill 46 ms and broken. |
| C3 txpool overflow | 1,600 accepted, 192 mined after 12 s. Root cause confirmed: the RPC layer swallows "pool full" rejections and a local tracker retries them once a minute (+64 mined per minute). With `--txpool.nolocals` the client gets 1,472 explicit errors. |
| C4 changed flags | `--cache` free, `--gcmode archive` silently accepted, `--state.scheme` mismatch refused. |
| C5 reorg | `debug_setHead` and `lab_fork` both look like REWIND then REORG depth 3. |
| C6 RPC abuse | Batch, body, slow-client and response limits all held. |
| C7 memory pressure | A 700 MB-capped node is fine under load (peak ~135 MB); 3 concurrent `debug_traceCall` endless loops crash it (713 to 729 MB, out of memory), the crashed process sometimes stays alive without answering (3 of 5 runs), and the restart hits the C1 wedge. Disk-full analysed from source only. |

## Tests

```bash
go vet ./...                                              # clean
go test ./...                                             # 22 fast unit tests, no node needed
go test -tags e2e -count=1 ./test/e2e                     # 1 end-to-end test: starts labnode (about 5 s)
go test -tags chaos -count=1 -v -timeout 30m ./test/chaos # C1 to C7 plus C3b, real processes (about 15 minutes; C7 is Windows only)
```

CI: [ci.yml](.github/workflows/ci.yml) (vet, gofmt, build, unit tests, e2e on Linux) on every push; [chaos.yml](.github/workflows/chaos.yml) runs the chaos suite on Linux weekly and on demand (see PLAN.md for its result).

## Honest limits

* The scenarios were written and first run on Windows 10 + Git Bash. The chaos suite also passed on an Ubuntu 24.04 GitHub runner (run 43274596212, 12.5 minutes; C7 skipped there): the C1 wedge (head 0 after restart, second transaction never mined) and the C3b numbers (1,472 underpriced errors with `--txpool.nolocals`, 384 mined after 230 s by default) reproduced exactly. macOS was not run.
* Prometheus, Grafana and the alert rules were verified natively through their APIs, not visually (no browser was available). The Docker Compose variant in `monitoring/` is untested.
* Timings come from one 8 GB laptop with other work running. They are indicative.
* A process kill is not a power cut; the C1/C2 findings are about `kill -9`.
* MIT licence ([LICENSE](LICENSE)) for this repository's own code. It uses go-ethereum (LGPL-3.0 library, GPL-3.0 `cmd/`) as a dependency; check their terms before redistributing binaries.

## Documentation map

[PLAN.md](PLAN.md) status and deviations - [docs/NODE-OPS.md](docs/NODE-OPS.md) flags and startup - [docs/RUNBOOK.md](docs/RUNBOOK.md) operate and recover -
[docs/METRICS.md](docs/METRICS.md) metric catalogue - [docs/OBSERVABILITY.md](docs/OBSERVABILITY.md) Prometheus and Grafana - [docs/upstream/](docs/upstream/README.md) go-ethereum #28222 study - [docs/geth-code-tour.md](docs/geth-code-tour.md) source tour - [docs/postmortems/](docs/postmortems/README.md) -
[docs/DEMO.md](docs/DEMO.md) - [docs/CONTRIBUTIONS.md](docs/CONTRIBUTIONS.md) - [docs/TOOLCHAIN.md](docs/TOOLCHAIN.md) - [CLAUDE.md](CLAUDE.md) conventions.
