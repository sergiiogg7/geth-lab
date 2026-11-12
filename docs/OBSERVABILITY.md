# Observability: Prometheus and Grafana against the lab node

Verified on 2026-11-12 on Windows 10 with **Prometheus 3.16.0** and **Grafana 13.3.1** run natively (no Docker), against `geth 1.17.10 --dev`. No browser was
available, so the proof is API output (`bin/obscheck`, saved under `docs/evidence/`), not screenshots. Nothing here says how the dashboard *looks*.

## Run it

```bash
scripts/build.sh                                         # includes bin/obscheck
scripts/start-node.sh --period 1                         # node with metrics on 127.0.0.1:6060 (METRICS_PORT to change)
scripts/observability-up.sh                              # Prometheus :9090, Grafana :3000 (PROM_PORT, GRAFANA_PORT to change)
bin/obscheck -prom http://127.0.0.1:9090 -grafana http://127.0.0.1:3000   # proof: targets, every panel query, rules, provisioning
scripts/observability-down.sh [--clean]                  # stops both by pid (never by process name); --clean deletes .obs/
```

Grafana: `http://127.0.0.1:3000/d/geth-lab-overview`, user `admin`, password `lab-throwaway` (loopback only; override with `GRAFANA_PASSWORD`).
Binaries are found through `PROM_HOME` / `GRAFANA_HOME`, `PATH`, or the author's tools folder. Runtime files go to the git-ignored `.obs/`. The scripts generate
loopback configs from the files in `monitoring/` (the Docker config points at `host.docker.internal`). `promtool check config` runs on every start.
`monitoring/docker-compose.yml` is an optional Docker path and is **untested** (no Docker on the author machine).

## What was proven (real output: `docs/evidence/obscheck-healthy.txt`, `obscheck-final.txt`)

| Check | Result |
|---|---|
| Prometheus target `job=geth instance=dev-node http://127.0.0.1:56060/debug/metrics/prometheus` | `health=up` |
| Every dashboard panel query (17 expressions, 8 panels) run straight on Prometheus | all return series; under load `rate(rpc_requests[1m])` = 75 to 81 per second |
| The same 17 queries through Grafana's datasource proxy `/api/ds/query` | all return a frame |
| Alert rules loaded (`/api/v1/rules`) | 11 of 11 `health=ok` |
| Grafana provisioning | datasource `Prometheus` (uid `prometheus`) and dashboard `geth-lab-overview` in folder `geth-lab` present via API |

What the first native run changed: the dashboard title said "untested" (removed); `NoPeers` sat in `pending` forever on a `--dev` node (it now excludes instances labelled `dev-*`);
`HeadStateBehindHeader` turned out not to detect the C1 wedge (below), so `HeadWentBackwards` was added; `TxPoolTrackerBacklog` and a tracker panel were added after C3. No metric name
needed correcting: all 17 expressions used real names from `docs/METRICS.md`.

## Panels (dashboard `monitoring/grafana/dashboards/geth.json`)

| Panel | Expression | Meaning |
|---|---|---|
| Head block | `chain_head_block` | executed chain height |
| Head block vs header | `chain_head_block`, `chain_head_header` | a gap means headers without state (snap sync). They fell together in the C1 wedge |
| TxPool pending / queued | `txpool_pending`, `txpool_queued` | executable / nonce-gapped transactions |
| TxPool discards (rate) | `rate(txpool_underpriced[1m])`, `..._overflowed`, `..._pending_ratelimit`, `..._queued_ratelimit` | rejection attempts, including the local tracker's retries (C3) |
| Block insert time | `chain_inserts{quantile="0.5"/"0.99"}` | ns per block insert |
| JSON-RPC requests / failures | `rate(rpc_requests[1m])`, `rate(rpc_failure[1m])` | volume and failures |
| Process memory | `system_memory_used`, `system_memory_held` | Go heap numbers, not committed memory (C7) |
| Local tracker backlog | `txpool_local`, `txpool_pending + txpool_queued` | transactions the node accepted from RPC vs those the pool holds |

## Alerts and what happened when they were driven (`monitoring/prometheus/alerts.yml`)

| Alert | Rule (short) | Drill and measured transition |
|---|---|---|
| `GethDown` | `up == 0` for 30 s | node killed by pid: `inactive -> pending` 17:17:30, `pending -> firing` 17:18:00 (`alert-gethdown.txt`) |
| `NoNewBlocks` | `changes(chain_head_block[2m]) == 0` for 2 m | C1 wedge (kill -9 under load, restart): `inactive -> pending` 17:20:50, `-> firing` 17:22:50 (`alert-c1-wedge.txt`) |
| `HeadWentBackwards` | `chain_head_block - max_over_time(chain_head_block[5m]) < 0` | same drill repeated: `inactive -> firing` within one scrape of the restart; head 55 to 0 (`alert-headwentbackwards.txt`) |
| `TxPoolDroppingTransactions` | `increase(txpool_underpriced[5m]) > 0 or ...overflowed` | C3 flood: `inactive -> firing` in 13 s (`alert-txpool-dropping.txt`) |
| `TxPoolTrackerBacklog` | `txpool_local - (txpool_pending + txpool_queued) > 100` for 1 m | C3 flood: `pending` 17:53:51, `firing` 17:54:50 (`alert-txpool-tracker-backlog.txt`) |
| `HeadStateBehindHeader` | `chain_head_header - chain_head_block > 3` | **did not fire** in the wedge: the header gauge fell with the block gauge (13 to 1 both). Kept for snap-sync situations, untested there |
| `NoPeers` | `p2p_peers{instance!~"dev-.*"} == 0` | `inactive` on the dev node (before the fix it was stuck in `pending`) |
| `TxPoolBacklog`, `ChainReorg`, `RpcErrorRatioHigh`, `MemoryHigh` | see file | loaded and healthy; **not driven** to firing. (`HeadWentBackwards` also fired once when a fresh chain replaced the node, as it should) |

Each alert's runbook section is in `docs/RUNBOOK.md`.

## Caveats

* `obscheck` proves that queries return data and rules evaluate, not that a human finds the dashboard readable.
* Prometheus and Grafana ignored the interrupt sent by `nodectl stop` on Windows; the down script kills them by the pid it recorded after 8 s (Prometheus' TSDB is crash-safe).
* Timings are from one laptop with other work running; `for:` durations dominate them.
