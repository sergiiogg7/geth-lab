# Metrics catalogue (geth v1.17.10, real names)

Everything here comes from scraping a running node, not from documentation:

```
scripts/start-node.sh --persist --period 1        # starts geth with --metrics --metrics.port 6060
bin/loadgen -txs 600 -mode contract               # make some traffic
scripts/metrics-scrape.sh --names                 # "<family> <type>" per line
scripts/metrics-scrape.sh --raw                   # the Prometheus exposition text
curl http://127.0.0.1:6060/debug/metrics/prometheus
```

Endpoint: `http://127.0.0.1:6060/debug/metrics/prometheus` (Prometheus text format) and `/debug/metrics` (JSON). Metrics are
**off** unless `--metrics` is given. The number of families grows with activity because many are only created on first use:
739 right after start, 787 after the demo, 827 after a 600-transaction load. A geth name like `chain/head/block` is exported
as `chain_head_block`.

## How to read the exposition

* `gauge`: a value (`chain_head_block 6`).
* `summary` + `<name>_count counter`: geth timers and meters. Quantile series carry a **space** before the label, which is
  unusual: `txpool_reorgtime {quantile="0.99"} 515800`. Durations are in **nanoseconds**.
* Several families that behave as counters are typed `gauge` (for example `txpool_underpriced`, `rpc_requests`): they only ever
  increase, so use `increase()` / `rate()` on them anyway. An upstream issue about the `_count` and `_sum` of
  `ResettingSample` summaries resetting on every scrape is open (#34628, with a fix PR #34658): do not alert on `rate()` of
  those until it is merged.
* `geth_info` carries labels: `{arch="amd64", os="windows", protocols="eth/72,eth/71,eth/70,eth/69,snap/2,snap/1", version="1.17.10-stable"} 1`.

## What an operator watches, and why

### Chain progress (is the node alive and advancing?)

| Metric | Type | Why you care |
|---|---|---|
| `chain_head_block` | gauge | Height of the executed head. Flat for N periods = block production or import stalled (alert `NoNewBlocks`). |
| `chain_head_header` | gauge | Highest known header. `header - block` > 0 for a while means headers are known but the state is not (alert `HeadStateBehindHeader`). Measured caveat: after the C1 `kill -9` rewind the header gauge fell together with the block gauge (13 to 1 both), so this gap did **not** appear; use `HeadWentBackwards` for that. |
| `chain_head_safe`, `chain_head_finalized`, `chain_head_receipt` | gauge | Fork-choice markers. In `--dev` the beacon finalizes every 32nd block (`devEpochLength`), so it is `0` for the first 31 blocks. |
| `chain_inserts` (+`_count`) | summary | Time to insert a block (validate + execute + write), ns. p99 climbing = slow disk or heavy blocks. |
| `chain_execution`, `chain_validation`, `chain_write`, `chain_crossvalidation` (+`_count`) | summary | The insert time broken down: EVM execution, header/body validation, database write. |
| `chain_account_reads`, `chain_storage_reads`, `chain_code_reads`, `chain_account_updates`, `chain_storage_updates`, `chain_account_commits`, `chain_storage_commits`, `chain_triedb_commits` (+`_count`) | summary | State access cost per block: where execution time goes (IO versus trie hashing). |
| `chain_mgasps` (+`_count`) | summary | Throughput in million gas per second. |
| `chain_reorg_add`, `chain_reorg_drop`, `chain_reorg_executes` | gauge | Blocks added/dropped by chain reorganisations. `increase(chain_reorg_drop[10m]) > 0` = alert `ChainReorg`. |
| `chain_info` | gauge | Chain metadata labels. |
| `chain_ancient_bytes`, `chain_ancient_write`, `chain_ancient_sync` | gauge / summary | Freezer (old blocks) activity. |

### Transaction pool

| Metric | Type | Why you care |
|---|---|---|
| `txpool_pending` | gauge | Executable transactions waiting for a block. Compare with `--txpool.globalslots` (5120). Alert `TxPoolBacklog`. |
| `txpool_queued` | gauge | Transactions with a nonce gap. A growing queue with flat pending = clients sending out of order. |
| `txpool_pending_accounts`, `txpool_queued_accounts` | gauge | Distinct senders. |
| `txpool_valid`, `txpool_invalid`, `txpool_known` | gauge (counter-like) | Accepted, rejected, duplicate submissions. |
| `txpool_underpriced`, `txpool_overflowed` | gauge (counter-like) | **Rejection attempts because the pool was full or the price was too low, including the local tracker's retries.** In C3 a client got 1600 transaction hashes back, `txpool_underpriced` grew by 2816 and only 192 were mined in the first 12 s: the RPC layer swallowed the rejections and the tracker retried them (the postmortem corrects an earlier "silent eviction" reading). Alert `TxPoolDroppingTransactions`. |
| `txpool_pending_ratelimit`, `txpool_queued_ratelimit` | gauge | Per-account cap hits (`accountslots`, `accountqueue`). |
| `txpool_pending_discard`, `txpool_pending_replace`, `txpool_queued_discard`, `txpool_queued_replace`, `txpool_queued_eviction`, `txpool_pending_nofunds`, `txpool_queued_nofunds` | gauge | The reasons behind removals and replacements. |
| `txpool_local` | gauge | Number of RPC-submitted transactions the **local tracker** holds (and journals). If it is larger than `txpool_pending + txpool_queued`, the node told clients "accepted" for transactions the pool refused and will retry them once a minute (C3; 1,600 after the C3 flood, 1,472 then 1,408 as they were mined). Alert `TxPoolTrackerBacklog`. |
| `txpool_slots`, `txpool_throttle`, `txpool_reservations_0` | gauge | Slot usage and throttling. |
| `txpool_reorgtime` (+`_count`), `txpool_reheap`, `txpool_dropbetweenreorg` | summary | Cost of resetting the pool on a new head. |
| `blobpool_*` (16 families) | gauge | Same for blob transactions. |

### JSON-RPC

| Metric | Type | Why you care |
|---|---|---|
| `rpc_requests`, `rpc_success`, `rpc_failure` | gauge (counter-like) | Request volume and failures; batch items count individually (C6: 1,024 requests for a few batches). Alert `RpcErrorRatioHigh`. |
| `rpc_duration_all` (+`_count`) | summary | Latency of every call, ns. |
| `rpc_duration_<method>_success`, `rpc_duration_<method>_failure` (+`_count`) | summary | Per-method latency, e.g. `rpc_duration_eth_sendRawTransaction_success`. Appear on first call of the method. |

### Process and host

| Metric | Type | Why you care |
|---|---|---|
| `system_memory_used`, `system_memory_held`, `system_memory_allocs`, `system_memory_frees`, `system_memory_objects` | gauge | Go heap numbers. `system_memory_used` was 18 to 63 MB in the lab runs even with a 4 GB `--cache`: caches fill lazily. Alert `MemoryHigh`. |
| `system_memory_pauses` (+`_count`) | summary | Garbage-collection pauses. |
| `system_cpu_procload`, `system_cpu_sysload`, `system_cpu_syswait`, `system_cpu_goroutines`, `system_cpu_threads`, `system_cpu_schedlatency` | gauge / summary | CPU pressure, goroutine leaks. |
| `system_disk_readbytes`, `system_disk_writebytes`, `system_disk_readcount`, `system_disk_writecount` | gauge | Process IO. |
| `geth_info` | gauge | Version labels: scrape it to see which version each node runs. |

### Database

| Metric | Type | Why you care |
|---|---|---|
| `eth_db_chaindata_disk_size`, `eth_db_chaindata_disk_read`, `eth_db_chaindata_disk_write` | gauge | Pebble size and IO. Size 1.78 MB after the 600-tx load. |
| `eth_db_chaindata_ancient_size`, `_read`, `_write` | gauge | Freezer. |
| `eth_db_chaindata_compact_time`, `eth_db_chaindata_compact_writedelay_counter`, `_duration` | gauge | LSM compaction stalls: `writedelay` growing = write stalls. |
| `eth_db_chaindata_tables_*` (7 families) | gauge | Per-level Pebble statistics. |
| `pathdb_dirty_state_read/write`, `pathdb_dirty_node_read/write`, `pathdb_clean_*`, `pathdb_commit_*` | gauge | The in-memory layers of the path-based trie database: hit/miss ratios, commits. A rising `pathdb_dirty_*_write` with no commit is the amount that a `kill -9` would lose (C1). |
| `state_snapshot_*`, `trie_prefetch_*`, `filtermaps_*` | gauge | Snapshot, prefetcher and log-index internals. |

### Networking and sync (zero or idle in `--dev`, important on a real node)

| Metric | Type | Why you care |
|---|---|---|
| `p2p_peers`, `p2p_peers_inbound`, `p2p_peers_outbound` | gauge | Peer count. 0 on `--dev`; alert `NoPeers` is for real networks. |
| `p2p_dials`, `p2p_dials_success`, `p2p_dials_error_*` (9 reasons), `p2p_ingress`, `p2p_egress` | gauge | Connectivity health and traffic. |
| `eth_downloader_headers_in`, `eth_downloader_bodies_*`, `eth_downloader_receipts_*`, `eth_downloader_chain_progress`, `eth_downloader_import_*` | gauge | Sync progress. |
| `eth_protocols_eth_*`, `eth_protocols_snap_sync_*` | gauge / summary | Protocol-level handshake errors and snap-sync state/heal progress. |

## Alert rules built on these names

`monitoring/prometheus/alerts.yml` (11 rules): `GethDown`, `NoNewBlocks`, `HeadWentBackwards`, `HeadStateBehindHeader`, `TxPoolBacklog`,
`TxPoolDroppingTransactions`, `TxPoolTrackerBacklog`, `ChainReorg`, `RpcErrorRatioHigh`, `MemoryHigh`, `NoPeers`. They were run natively against Prometheus 3.16.0 on 2026-11-12
(`promtool check rules`, `/api/v1/rules`, and controlled failures that drove `GethDown`, `NoNewBlocks`, `HeadWentBackwards` and `TxPoolDroppingTransactions` to `firing`; see
[OBSERVABILITY.md](OBSERVABILITY.md)). Two facts from those runs changed this catalogue: after a `kill -9` wedge `chain_head_header` fell together with `chain_head_block`, so
`HeadStateBehindHeader` does **not** detect it (`HeadWentBackwards` does); and `txpool_local` (the local-transaction tracker, see C3) is the number that exposes "accepted but not in the pool".
The Docker Compose file is still untested. Without Prometheus, `bin/watch -serve 127.0.0.1:8081` gives a status page and `scripts/metrics-scrape.sh` a quick text view.
