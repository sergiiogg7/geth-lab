# Runbook: operating the lab node

Scope: a single `geth --dev` node (or `labnode`) on one machine. Every entry points at the experiment that proved it. Commands run in
Git Bash from the repo root.

## Start, stop, check

| Task | Command | Notes |
|---|---|---|
| Build tools | `scripts/build.sh` | `bin/loadgen`, `bin/watch`, `bin/labnode`, `bin/nodectl` |
| Start, throwaway chain | `scripts/start-node.sh` | in-memory; everything is lost on stop |
| Start, persistent | `scripts/start-node.sh --persist` | datadir `.lab/datadir`, log `.lab/node.log` |
| Start with periodic blocks | `... --period 2` | needed for alert `NoNewBlocks` to make sense |
| Start the extended node | `... --client labnode` | adds `lab_*` and the `gasProfile` tracer |
| Start with crash safety | `scripts/start-node.sh --persist -- --gcmode archive --state.scheme hash` | the only configuration that survived `kill -9` (C1); only valid for a **new** datadir |
| Smoke test | `scripts/smoke.sh` | non-zero exit on failure |
| Status | `bin/watch -once` (JSON) or `bin/watch -serve 127.0.0.1:8081` (page) | no Prometheus needed |
| Metrics | `scripts/metrics-scrape.sh` | names in `docs/METRICS.md` |
| Stop politely | `scripts/stop-node.sh` | flushes state ("Persisting dirty state") |
| Stop hard | `scripts/stop-node.sh --kill` | only for experiments |
| Wipe everything | `scripts/reset.sh` | kills the node and deletes `.lab/` |

**Readiness is not "RPC answers".** The node answered in 0.3 to 0.5 s after every start, including starts that left it broken. A restart
is healthy only if (1) `eth_blockNumber` is not lower than before the stop, (2) `eth_getBalance` works on `latest`, (3) a new transaction gets a receipt
within a few block periods. The chaos tests (`mineOne`) and `scripts/demo.sh` use that definition. Also note that for a short time after start
`eth_getTransactionReceipt` returns `-32000 transaction indexing is in progress`.

## Recover

### After an unclean stop of a persistent dev node (C1, C2)

Symptoms: log has `Head state missing, repairing`, `Switching from full-sync to snap-sync reason="snap-sync incomplete"`, later
`Ignoring payload while snap syncing`; `eth_blockNumber` is far below the old head; blocks stop after block 1.
Action: the node is not repairable in place. `scripts/reset.sh` and start again (chain lost; this is a dev chain). To avoid it next time,
stop with `scripts/stop-node.sh`, or run the archive+hash configuration above. Transactions that were only in the pool are lost with a
default-config datadir (journal is re-submitted after 10 s but nothing can mine); with archive+hash they come back by themselves ~10 s after the start.

### Pool is full / transactions disappear (C3)

A hash returned by `eth_sendRawTransaction` is not a promise. Poll `eth_getTransactionReceipt` with a timeout and resend with the
same nonce and a fee at least 10% higher on both tip and fee cap (`replacement transaction underpriced` otherwise). To look: `cast rpc txpool_status`,
`cast rpc lab_txpoolStats` on labnode, counters `txpool_underpriced`, `txpool_*_ratelimit`. Remedy: raise `--txpool.globalslots / globalqueue` or
slow the sender down.

### Changing flags on an existing datadir (C4)

| Flag | Safe on existing data? |
|---|---|
| `--cache N` | yes, any value; check `Allocated trie memory caches` in the log |
| `--gcmode archive` | starts silently; changes retention from then on |
| `--state.scheme hash` on a path DB (or the reverse) | refused: `incompatible state scheme, stored: path, provided: hash`. Resync or start a new datadir. |

### Rewinding the chain on purpose (C5)

`cast rpc debug_setHead 0x<n>` on stock geth, or `cast rpc lab_fork 0x<depth>` on labnode (needs an empty pool). Followers see
`REWIND` then `REORG`. Do not do this on a datadir you care about.

### RPC misuse (C6)

Defaults hold: batches over 1000 items are refused with `-32600 batch too large` (HTTP 200, one error object), bodies over 5 MB get
HTTP 413, half-sent requests are cut after 30 s, batch responses over `--rpc.batch-response-max-size` return `-32003 response too large` per item.

## Alert response

Alert rules: `monitoring/prometheus/alerts.yml` (untested: no Docker here). Each section is the target of the rule's `runbook` annotation.

### Alert: GethDown
`up{job="geth"} == 0`. Check the process (`scripts/stop-node.sh` says "no node running" or not), the log tail (`tail -50 .lab/node.log`), the metrics port
(`curl -s http://127.0.0.1:6060/debug/metrics/prometheus | head`). Restart with the same flags; then apply the readiness definition above.

### Alert: NoNewBlocks
`changes(chain_head_block[2m]) == 0`. Only meaningful with `--dev.period`. Check `eth_blockNumber` twice, the log for `Ignoring payload while snap syncing`
(then see "After an unclean stop"), and `txpool_status` (pending transactions with no blocks = beacon wedged).

### Alert: HeadStateBehindHeader
`chain_head_header - chain_head_block > 3`: the unclean-shutdown signature (C1). Follow "After an unclean stop".

### Alert: TxPoolBacklog
`txpool_pending > 4000` for 5 minutes. Blocks are full (`lab_blockFill`, `bin/watch` shows the gas fill percentage) or the producer is slow. Raise `--dev.gaslimit` / `--miner.gaslimit`,
shorten `--dev.period`, throttle senders.

### Alert: TxPoolDroppingTransactions
Evictions happening (C3). Identify the senders (`lab_txpoolStats.largestAccountPending`), check whether clients resend; widen pool limits.

### Alert: ChainReorg
Blocks dropped from the canonical chain. In a dev node: someone ran `debug_setHead` or `lab_fork`. Confirm with `bin/watch` history.

### Alert: RpcErrorRatioHigh
`rate(rpc_failure) / rate(rpc_requests) > 10%`. Find the failing method (`rpc_duration_<method>_failure_count` families), clients hitting `batch too large`, or
nonce/underpriced errors from a flood.

### Alert: MemoryHigh
`system_memory_used > 2e9`. Lower `--cache` (C4 table), restart gracefully.

### Alert: NoPeers
Not applicable to `--dev` (no networking). For a real node: check `admin_peers`, firewall, `--bootnodes`.

## Known limits of this runbook

* Single node only; nothing here covers multi-node networks, which need a consensus client per execution client.
* Behaviour was observed on Windows with a process kill; a real power cut may differ (Pebble WAL fsync policy not tested).
* Linux/macOS: scripts and tests should work (the Windows-specific code is isolated), but they were not run there.
