# geth-lab

A hands-on lab around **Geth** (go-ethereum, the most widely used Ethereum execution client, written in Go): operate it, extend it with Go code,
break it on purpose, and write down what happened. Built to be cloned, run in minutes and read.

* **Operate:** scripts to start, stop, kill, reset and smoke-test a `geth --dev` node, Prometheus metrics catalogue from a real scrape, runbook.
* **Extend (Go):** `labnode`, a binary that embeds Geth **as a library** and adds a custom `lab_` JSON-RPC namespace and a native EVM tracer built on
  `core/tracing` hooks. `loadgen`, a transaction flood with latency percentiles and txpool behaviour. `watch`, a head follower that detects reorgs, rewinds, stalls and RPC outages.
* **Break:** six automated failure scenarios on real node processes (kill -9 under load, graceful vs hard stop, txpool overflow, storage-flag changes,
  rewind/reorg, RPC abuse), each with a written postmortem that quotes numbers from the run.
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
  monitoring/ (Prometheus, Grafana, alerts: untested)  <---- /debug/metrics/prometheus (port 6060)
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
| C3 txpool overflow | 1,600 accepted submissions, 192 mined: acceptance is not retention. |
| C4 changed flags | `--cache` free, `--gcmode archive` silently accepted, `--state.scheme` mismatch refused. |
| C5 reorg | `debug_setHead` and `lab_fork` both look like REWIND then REORG depth 3. |
| C6 RPC abuse | Batch, body, slow-client and response limits all held. |
| C7 disk / memory pressure | Not run (not safe on this machine). |

## Tests

```bash
go vet ./...                                              # clean
go test ./...                                             # 18 fast unit tests, no node needed
go test -tags e2e -count=1 ./test/e2e                     # 1 end-to-end test: starts labnode (about 5 s)
go test -tags chaos -count=1 -v -timeout 20m ./test/chaos # 6 scenarios / 12 tests, real processes (about 6 minutes)
```

CI definition: [.github/workflows/ci.yml](.github/workflows/ci.yml) (**never run on a runner**; the commands in it were run by hand).

## Honest limits

* Windows 10 + Git Bash is the only environment the code was run on. The OS-specific code (process control, Ctrl-Break) has Unix counterparts that are untested.
* `monitoring/` (Prometheus, Grafana, alert rules) is untested: no Docker on the author's machine. Metric names and the scrape path are verified.
* Timings come from one 8 GB laptop with other work running. They are indicative.
* A process kill is not a power cut; the C1/C2 findings are about `kill -9`.
* No licence has been chosen for this repository yet. go-ethereum is LGPL-3.0 (library) / GPL-3.0 (`cmd/`).

## Documentation map

[PLAN.md](PLAN.md) status and deviations - [docs/NODE-OPS.md](docs/NODE-OPS.md) flags and startup - [docs/RUNBOOK.md](docs/RUNBOOK.md) operate and recover -
[docs/METRICS.md](docs/METRICS.md) metric catalogue - [docs/geth-code-tour.md](docs/geth-code-tour.md) source tour - [docs/postmortems/](docs/postmortems/README.md) -
[docs/DEMO.md](docs/DEMO.md) - [docs/CONTRIBUTIONS.md](docs/CONTRIBUTIONS.md) - [docs/TOOLCHAIN.md](docs/TOOLCHAIN.md) - [CLAUDE.md](CLAUDE.md) conventions.
