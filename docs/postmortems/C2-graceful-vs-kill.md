# C2: graceful stop versus hard kill, and the integrity of the datadir

Date: 2026-11-12. Test: `test/chaos/c2_graceful_test.go`. Data: `out/chaos/C2-graceful-vs-kill.json`.
Node: stock `geth 1.17.10 --dev --datadir D --dev.period 1`, default storage flags. Windows 10.

## Summary

Same workload, two ways of stopping. The graceful stop (Ctrl-Break, which Go maps to `os.Interrupt`) took 77 ms and the node
came back at the same head with a working chain. The hard kill took 46 ms and the node came back with **the head rewound from block 8 to
block 0 and block production stuck** (the C1 failure). The chain that remained was internally consistent in both cases: the
integrity check found no broken parent links; what the hard kill damaged is availability of state, not the block database.

## Setup and hypothesis

Load: 8 s at 150 tx/s from 4 senders (about 1,050 transactions), then stop, restart on the same datadir, check:
head, parent-hash linkage of every block, and "functional" = two transactions sent 4 s apart must both be mined.

Hypothesis (before the run): graceful stop takes noticeably longer because it flushes state; after it the node returns at the same
head; after the hard kill the datadir needs a repair step on the next start but the chain survives.

## Observed

| Quantity | graceful | hard kill |
|---|---|---|
| Transactions accepted | 1,047 | 1,046 |
| Head before stop | 8 | 8 |
| Time to stop | **0.077 s** | 0.046 s |
| Time to RPC ready after restart | 0.31 s | 0.31 s |
| Head after restart | **8** | **0** (8 blocks lost) |
| Blocks checked / parent-link problems | 9 / none | 1 / none |
| First transaction after restart mined | yes | yes |
| Second transaction (4 s later) mined | yes | **no** (`receipt: context deadline exceeded`) |

Log evidence, graceful stop (`graceful.stopLogHighlights`):

```
Got interrupt, shutting down...
HTTP server stopped
Persisting dirty state            head=8 layers=8
Persisted dirty state to file     path=...\geth\triedb\merkle.journal size=26.86KiB elapsed=14.003ms
Blockchain stopped
```

Log evidence, restart after the kill: `Head state missing, repairing number=7`, `Switching from full-sync to snap-sync
reason="snap-sync incomplete"`, `Unclean shutdown detected booted=...`, then `Ignoring payload while snap syncing number=2`.
After the graceful stop the restart log has none of those lines: just `Loaded most recent local block number=8` and
`Loaded local transaction journal transactions=1051`.

Hypothesis verdict: the stop-time difference is **small** (about 30 ms, the journal is 27 KiB), so "graceful takes much longer" was wrong
for a dev chain this size. "Chain survives the kill" was **wrong** (see C1).

## Root cause

Same as C1. The 14 ms in `Persisting dirty state` is exactly the work the hard kill skips: writing the in-memory trie layers
(`layers=8`, one per block, `triedb/pathdb/journal.go:321/329`) to `geth/triedb/merkle.journal`. Without that file the
path database cannot present a state for the head, so the head is rewound and sync mode flips. The block database itself
(Pebble) was consistent: the integrity walk reads blocks 0..head and parent hashes match.

Note on the head numbers: head before the kill was 8 and the log says `Loaded most recent local block number=7` at the restart, i.e. the
process had also lost the very last block's data (probably it was still being written when the process died) before the state repair rewound further.

## Action items

| # | Item | Status |
|---|---|---|
| 1 | Scripts stop nodes gracefully by default (`scripts/stop-node.sh`, `bin/nodectl stop`); `--kill` exists on purpose for scenarios. | done |
| 2 | On Windows a background process cannot get Ctrl-C from bash. `nodectl` starts the node with its own hidden console and attaches to it to send Ctrl-Break. First attempt, broadcasting to the shared console, interrupted the calling shell too. Keep the private-console design. | done, documented in `cmd/nodectl/main.go` |
| 3 | Runbook: "never kill a persistent dev node; if you must, expect to reset the datadir". | `docs/RUNBOOK.md` |
| 4 | Longer-running chain (more blocks, bigger state) would make the graceful stop time grow; not measured. | open |

## Reproduce

`scripts/chaos.sh C2`. By hand: `scripts/start-node.sh --persist --period 1`, load, then `scripts/stop-node.sh` or `scripts/stop-node.sh --kill`, restart.
