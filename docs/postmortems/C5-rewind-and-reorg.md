# C5: chain rewind and reorg, and how `watch` reacts

Date: 2026-11-12. Test: `test/chaos/c5_reorg_test.go`. Data: `out/chaos/C5-rewind-and-reorg.json`.
Nodes: stock `geth --dev --dev.period 1` (rewind with `debug_setHead`) and `labnode --dev.period 1` (rewind with `lab_fork`). Windows 10.

## Summary

A dev node has one block producer, so a natural fork cannot happen. Two forced ways were tested: `debug_setHead(n)` on stock geth and
the lab's `lab_fork(depth)`. Both put the head back 3 blocks (12 to 9) and, because the simulated beacon then builds **new** blocks on top of
the earlier head, both end as a real reorg: the same heights come back with different hashes. `bin/watch` reported both
the same way: `REWIND head #12 -> #9` immediately, and 0.9 s later `REORG depth=3`. All 22 transactions that were in the dropped blocks
were mined again in the new blocks.

## Setup and hypothesis

Node producing a block per second; `watch` (the real binary, 100 ms poll) attached; loadgen sends 20 transactions so the soon-dropped
blocks are not empty; wait for 4 empty blocks; head 12; trigger; wait 8 s; compare.

Hypothesis before the run: `debug_setHead` is seen as a REWIND only (old blocks are deleted, not replaced); `lab_fork` is seen as a REORG.

## Observed

| | stock geth `debug_setHead(0x9)` | labnode `lab_fork(0x3)` |
|---|---|---|
| Head before / right after trigger / 8 s later | 12 / 9 / 17 | 12 / 9 / 17 |
| Block at height 12 before vs after | different hashes (`0xb1a1...` vs `0x670b...`) | different (`0x2510...` vs `0x06e4...`) |
| Transactions mined before the trigger | 22 | 22 |
| Still canonical afterwards | 22 | 22 |
| `watch` events | `REWIND head #12 -> #9 (dropped 3 blocks, now at known canonical 0x176a503a)`, then `REORG depth=3 added=1 ancestor=#9 old=#12 0xb1a10a10 new=#10 0x538cbdb3` | `REWIND head #12 -> #9 ...`, then `REORG depth=3 added=1 ancestor=#9 old=#12 0x2510a77a new=#10 0x39e03a1a` |
| `watch` summary line | `blocks=39 reorgs=1 (max depth 3) rewinds=1 outages=0 avgBlockTime=1.00s avgFill=0.1%` | same |

Hypothesis verdict: **half wrong.** The rewind itself is a REWIND, but because the beacon keeps producing blocks, `debug_setHead` also ends in a reorg
from a follower's point of view. The two triggers are indistinguishable to `watch`.

## Root cause / what actually happens

* `debug_setHead` (`eth/api_backend.go:67 SetHead` -> `core/blockchain.go:826 SetHead`, `setHeadBeyondRoot` 1061) rewinds and **deletes**
  the blocks above the target. `lab_fork` calls `SimulatedBeacon.Fork` (`eth/catalyst/simulated_beacon.go:377`) -> `BlockChain.SetCanonical`
  (`core/blockchain.go:2901`), which moves the canonical pointer back and **keeps the old blocks** as a side chain.
* In both cases the next `sealBlock` (`simulated_beacon.go:175`) notices `curForkchoiceState.HeadBlockHash != CurrentBlock().Hash()` ("Reset to
  CurrentBlock in case of the chain was rewound", line 184), re-bases on the new head and asks the miner for a block with a fresh
  timestamp and random value, so the hash differs from the dropped block at the same height.
* Transactions of dropped blocks went back into the pool and were mined again in the replacement blocks (22 of 22 still canonical). The
  tracker in `internal/chainwatch` therefore needs the "rewind then replacement" rule: a head that goes back onto a known canonical block is a
  REWIND; the first child that has a different hash at a height seen before turns it into a REORG with `depth = old head - ancestor`.

## Watch behaviour checked by unit tests

`internal/chainwatch/chainwatch_test.go`: linear chain, skipped heads (not a reorg), rewind then replacement, direct branch switch
(depth 3, 4 added, ancestor 0) and an unknown ancestor (error). 5 tests, no node needed.

## Action items

| # | Item | Status |
|---|---|---|
| 1 | `watch` distinguishes REWIND, REORG, SKIP and prints depth; alert `ChainReorg` on `increase(chain_reorg_drop[10m])`. | done / in alerts file (untested) |
| 2 | Not covered: a reorg at depth > `MaxWalkBack` (256) is reported with an error; reorgs while `watch` is polling slowly (SKIP path covered by unit test only). | open |
| 3 | Check whether `chain_reorg_drop` actually moves for `debug_setHead` (it counts reorg drops, setHead is a rewind). Not scraped during this run. | open |
| 4 | Finalized markers: dev mode finalizes every 32nd block (`devEpochLength`, `simulated_beacon.go:46`); a rewind below the finalized block was not tried. | open |

## Reproduce

`scripts/chaos.sh C5`, or `scripts/demo.sh` section 5 (`lab_fork 0x3` with `watch` running).
