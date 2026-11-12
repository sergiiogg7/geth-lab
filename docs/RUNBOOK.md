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
| Prometheus + Grafana | `scripts/observability-up.sh`, `scripts/observability-down.sh` | native, no Docker; see `docs/OBSERVABILITY.md` |
| Verify the stack | `bin/obscheck -prom http://127.0.0.1:9090 -grafana http://127.0.0.1:3000` | targets, every panel query, rules, provisioning |
| Start with a memory cap | `scripts/start-node.sh --mem-limit-mb 700` | Windows Job Object cap on committed memory (C7) |
| Stop politely | `scripts/stop-node.sh` | flushes state ("Persisting dirty state") |
| Stop hard | `scripts/stop-node.sh --kill` | only for experiments |
| Wipe everything | `scripts/reset.sh` | kills the node and deletes `.lab/` |

**Readiness is not "RPC answers".** The node answered in 0.3 to 0.5 s after every start, including starts that left it broken. A restart
is healthy only if (1) `eth_blockNumber` is not lower than before the stop, (2) `eth_getBalance` works on `latest`, (3) a new transaction gets a receipt
within a few block periods. The chaos tests (`mineOne`) and `scripts/demo.sh` use that definition. Also note that for a short time after start
`eth_getTransactionReceipt` returns `-32000 transaction indexing is in progress`.

## Recover

### After an unclean stop of a persistent dev node (C1, C2)

Alert signature: `HeadWentBackwards`, then `NoNewBlocks` after 4 minutes (both measured). The wedge needs the rewind to leave a snap head above the block head, so it is not certain: a run killed at block 55 rewound to 0 and recovered by itself (`docs/evidence/alert-headwentbackwards.txt`).
Symptoms: log has `Head state missing, repairing`, `Switching from full-sync to snap-sync reason="snap-sync incomplete"`, later
`Ignoring payload while snap syncing`; `eth_blockNumber` is far below the old head; blocks stop after block 1.
Action: the node is not repairable in place. `scripts/reset.sh` and start again (chain lost; this is a dev chain). To avoid it next time,
stop with `scripts/stop-node.sh`, or run the archive+hash configuration above. Transactions that were only in the pool are lost with a
default-config datadir (journal is re-submitted after 10 s but nothing can mine); with archive+hash they come back by themselves ~10 s after the start.

### Pool is full / transactions disappear (C3)

A hash returned by `eth_sendRawTransaction` is not a promise: the node may be holding the transaction in its local tracker and retrying once a minute (C3: 1,600 accepted, 192 mined after 12 s, +64 per minute afterwards; with `--txpool.nolocals` the client gets the error instead). Poll `eth_getTransactionReceipt` with a timeout and resend with the
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

Alert rules: `monitoring/prometheus/alerts.yml`, run natively against Prometheus 3.16.0 (`scripts/observability-up.sh`, details and evidence in [OBSERVABILITY.md](OBSERVABILITY.md)). Each section is the target of the rule's `runbook` annotation.

### Alert: GethDown
`up{job="geth"} == 0`. Check the process (`scripts/stop-node.sh` says "no node running" or not), the log tail (`tail -50 .lab/node.log`), the metrics port
(`curl -s http://127.0.0.1:6060/debug/metrics/prometheus | head`). Restart with the same flags; then apply the readiness definition above.

### Alert: NoNewBlocks
`changes(chain_head_block[2m]) == 0`. Only meaningful with `--dev.period`. Check `eth_blockNumber` twice, the log for `Ignoring payload while snap syncing`
(then see "After an unclean stop"), and `txpool_status` (pending transactions with no blocks = beacon wedged).

### Alert: HeadWentBackwards
`chain_head_block - max_over_time(chain_head_block[5m]) < 0`: the block head is lower than it was within the last 5 minutes. This is the measured signature of the unclean-shutdown rewind (C1): head 13 to 1 after `kill -9`, alert fired within one scrape (`docs/evidence/alert-c1-wedge.txt`, `alert-headwentbackwards.txt`). It also fires, by design, after `debug_setHead` or `lab_fork` and when a node is replaced by a fresh chain. Follow "After an unclean stop" below: check whether `NoNewBlocks` follows (wedged) or the chain grows again (recovered, as in the 55-block run).

### Alert: HeadStateBehindHeader
`chain_head_header - chain_head_block > 3`: headers known but state not (seen during snap sync). **It did not fire in the C1 wedge**: the header gauge fell together with the block gauge. Do not rely on it for unclean shutdowns; use `HeadWentBackwards` and `NoNewBlocks`.

### Alert: TxPoolBacklog
`txpool_pending > 4000` for 5 minutes. Blocks are full (`lab_blockFill`, `bin/watch` shows the gas fill percentage) or the producer is slow. Raise `--dev.gaslimit` / `--miner.gaslimit`,
shorten `--dev.period`, throttle senders.

### Alert: TxPoolDroppingTransactions
The pool is refusing transactions (`ErrUnderpriced` when full; C3). **Clients may not see an error**: RPC-submitted transactions are handed to the local tracker and retried once a minute, so this alert is often the only signal. Identify the senders (`lab_txpoolStats.largestAccountPending`), widen the pool limits, or start the node with `--txpool.nolocals` so clients get `transaction underpriced` immediately. Verified firing on a real flood in 13 s (`docs/evidence/alert-txpool-dropping.txt`).

### Alert: TxPoolTrackerBacklog
`txpool_local - (txpool_pending + txpool_queued) > 100` for 1 minute: the node told clients "accepted" for more transactions than the pool holds (C3 root cause). They are delivered later, at the pool's pace (64 per minute with the C3 limits), or never if the node restarts without a journal. Same remedies as above.

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
