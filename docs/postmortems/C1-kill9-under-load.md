# C1: `kill -9` under load, restart on the same datadir

Date of the run: 2026-11-12. Test: `test/chaos/c1_kill9_test.go` (`go test -tags chaos -run TestC1 ./test/chaos`).
Raw data: `out/chaos/C1-kill9-under-load.json`, node logs in `out/chaos/logs/`. Node: stock `geth 1.17.10 --dev --datadir D --dev.period 2`.
Windows 10, `TerminateProcess` as the "kill -9". **Severity (as a lab finding): high. A dev node with a persistent datadir does not survive a
hard kill.**

## Summary

A node with a backlog was killed. After the restart the JSON-RPC server answered in 0.3 to 0.5 s, so every health check
would have said "up", but the chain had been rewound to **genesis (5 blocks and 1,648 mined transactions gone)** and the node
was **stuck**: it accepted one more block and then never produced another. Re-running the same scenario with
`--gcmode archive --state.scheme hash` lost nothing and recovered every transaction that had only been in the pool.

## Setup and hypothesis

Flood: loadgen, 4 senders, 300 tx/s, simple transfers; blocks every 2 s so a backlog builds up. The test waits until at least
5 blocks exist, at least 200 transactions are pending and at least 1,000 were accepted, records the canonical transactions,
kills the process, restarts it on the same datadir, then accounts for every accepted transaction hash against the canonical chain.

Hypothesis written before the first run: sealed blocks survive; unmined pool transactions are lost except those brought back by the
txpool journal; the node is usable again within seconds.

## Timeline (variant `default`, times from the node log, wall clock)

| Time | Event |
|---|---|
| 10:31:04 | First process boots (`booted=` field of the later warning). |
| ~10:31:12 | Flood running, head 5, 209 transactions pending, 1,652 transactions mined in blocks 1..5. Process killed. |
| 10:31:13.27 | Second process starts. `State scheme set to already existing scheme=path`. |
| 10:31:13.289 | `Loaded most recent local block number=4`, then **`Head state missing, repairing number=4`**, `Genesis block reached`. |
| 10:31:13.291 | `Loaded most recent local block number=0`. Headers are still known up to 4. |
| 10:31:13.307 | **`Switching from full-sync to snap-sync reason="snap-sync incomplete"`**, `Unclean shutdown detected`. |
| ~10:31:13.7 | RPC answers (0.42 s after process start; 0.49 s after the kill). |
| 10:31:13.771 | `Loaded local transaction journal transactions=1861 dropped=0`. |
| 10:31:15.772 | **`Ignoring payload while snap syncing number=2 ... reason="forced head needed for startup"`**. Block 1 was sealed, block 2 was refused. |
| +5 s, +20 s | Test sends a transaction: it is mined in block 1. A second transaction sent 5 s later never gets a receipt within 15 s. Head stays at 1 for the next 90 s. |

## Expected vs observed (numbers from the run)

| Quantity | `default` (path scheme, `full`) | `--gcmode archive --state.scheme hash` |
|---|---|---|
| Head before kill | 5 | 5 |
| Transactions mined before kill | 1,652 | 1,652 |
| Pool before kill (pending) | 209 | 203 |
| Transactions accepted by the node (hashes returned) | 1,857 | 1,851 |
| Head right after restart | **0** | 5 |
| Blocks lost | **5** | 0 |
| Kill to RPC ready | 0.49 s | 0.35 s |
| Mined-before-kill transactions still canonical afterwards | **0 of 1,648** | 1,648 of 1,648 |
| Pool-only transactions that ended up mined | 0 of 209 | **203 of 203** |
| Can the node mine a new transaction? | one, then no (second tx: `context deadline exceeded`) | yes, and keeps producing (head 12 at the end) |
| `eth_getBalance(dev)` right after restart | works | works |
| Local journal | 1,861 txs loaded, none usable | 1,855 loaded; re-submitted to the pool ~10 s after start and all mined |

(The 1,648 vs 1,652: four of the mined transactions are the funding transfers of the senders, which are not in the loadgen hash list.)

Hypothesis verdict: **wrong in the default configuration**, right with archive+hash. Sealed blocks did not survive, and the node
did not become usable again.

## Root cause

1. The default state scheme (path) keeps the latest block states in memory as diff layers (up to 128 layers,
   `triedb/pathdb/config.go:70 maxDiffLayers`) and writes them to disk when the buffer fills or at a **clean shutdown**
   (`BlockChain.Stop` -> `triedb.Journal`, which logs "Persisting dirty state"; `core/blockchain.go:1441`). A hard kill skips that.
   On the next start the disk layer is the genesis state, so `core/blockchain.go:497/508` ("Head state missing, repairing")
   rewinds the head to the newest block with state, which is block 0. The blocks themselves are in the database, but the canonical
   head is moved back, which is why the transactions are "lost".
2. `eth/downloader/syncmode.go:49`: the node is configured for full sync, but `CurrentBlock` is 0 while `CurrentSnapBlock` is 4, which
   the code reads as "snap sync was in progress and not finished" and switches to snap sync (line 51 logs it). `syncModer.get`
   returns snap mode for good once it is set (line 83).
3. In snap mode the Engine API refuses payloads: `eth/catalyst/api.go:1064 delayPayloadImport` logs "Ignoring payload while snap
   syncing" (line 1095) and answers SYNCING. A real network has a consensus client that can force-update the head to restart sync;
   the dev-mode `SimulatedBeacon` cannot, so block production stops. Block 1 is accepted only because of a special case
   for the first block (`eth/catalyst/api.go:995`, `block.NumberU64() != 1`).
4. The txpool journal cannot help: the local-transaction tracker re-submits journaled transactions 10 s after start
   (`core/txpool/locals/tx_tracker.go:213`), but the state is back at genesis (the senders' funding transfers are no longer part of it), and in any case no block can be
   built while the node is in snap mode. (Why exactly the 1,861 journaled transactions were not mined was not investigated further.)
5. With `--gcmode archive --state.scheme hash`, `writeBlockWithState` commits the state of every block as it is inserted
   (`core/blockchain.go:1782`). The head state exists at restart, nothing is rewound, full sync stays on, the beacon keeps
   working and the journal replays the pool.

## Impact

* "RPC is up" is not a readiness signal. The failure looks healthy for about two seconds.
* A developer or CI job that kills a persistent dev node loses the chain and then waits for blocks that never come.
* The same wedge exists in the closest upstream report, issue #28222 (genesis plus default sync mode), which has two open fix PRs
  (see `docs/CONTRIBUTIONS.md`).

## Action items

| # | Item | Status |
|---|---|---|
| 1 | Runbook: after any unclean stop of a persistent dev node, treat the datadir as lost: `scripts/reset.sh`, or run with `--gcmode archive --state.scheme hash` if you need crash safety. | in `docs/RUNBOOK.md` |
| 2 | Alert `HeadStateBehindHeader` (`chain_head_header - chain_head_block > 3`) detects this condition; `NoNewBlocks` catches the stall. | in `monitoring/prometheus/alerts.yml` (untested) |
| 3 | Add the readiness check "mine one transaction" to scripts that restart a node, not only "RPC answers". | `internal/nodeproc` + `mineOne` in the chaos tests; smoke.sh polls the receipt |
| 4 | Re-run this test against the branches of PR #36037 and PR #35567 and report the result on issue #28222. | **proposed, needs the owner's go** |
| 5 | Measure a real power loss. A process kill does not drop data the OS already buffered, so this test cannot say what an actual power cut does to Pebble's WAL. | not reproducible safely here |

## Reproduce

`scripts/demo.sh` (section 7) or `scripts/chaos.sh C1`. The variants differ only in the two flags above.
