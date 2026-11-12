# C3: transaction flood beyond the txpool limits

Date: 2026-11-12 (first run, rewritten the same day after a second experiment). Tests: `test/chaos/c3_txpool_test.go` (the flood) and
`test/chaos/c3b_locals_test.go` (the experiment that explains it). Data: `out/chaos/C3-txpool-overflow.json`, `out/chaos/C3b-local-tracker.json`
(copy in `docs/evidence/`). Node: stock `geth 1.17.10 --dev --dev.period 4 --txpool.globalslots 64 --txpool.globalqueue 64 --txpool.accountslots 16 --txpool.accountqueue 16`,
in-memory chain, metrics on. Windows 10.

## Summary

With tiny pool limits and slow blocks, a client submitted 1,600 transactions, every `eth_sendRawTransaction` returned a hash, and only 192 were mined by the end of the
12-second drain. **Root cause (confirmed by experiment): this is not a silent eviction. The pool rejected 1,472 of them with `ErrUnderpriced`, and Geth's RPC layer
deliberately swallowed that error.** `EthAPIBackend.SendTx` treats "pool full" style rejections as *temporary*, returns success to the client and hands the transaction to the
**local-transaction tracker**, which re-submits whatever the pool does not hold, once at 10 s and then **once per minute**. The transactions are not lost, they are queued in the tracker
and trickle back in: with these limits 64 more were mined per minute (192 -> 256 -> 320 -> 384 over 230 s), so at that rate the 1,408 stragglers would need about 22 minutes (extrapolation, not measured to the end). With `--txpool.nolocals`
the same flood is answered with 1,472 explicit `transaction underpriced` errors and every accepted transaction is mined.

My first write-up of this scenario said the pool "accepts, then evicts silently". **That was wrong** (the counter `txpool_underpriced` made it look like evictions, and the
fact that no client error was seen made it look like acceptance). The corrected mechanism is below; the original numbers all stand.

## Setup and hypotheses

Three phases on one node, each a `loadgen` run with its own senders (C3), then a targeted experiment (C3b):

| Phase | Load | Purpose |
|---|---|---|
| P1 steady flood | 16 senders, 1,600 transactions, unthrottled, blocks every 4 s | overflow the pool |
| P2 nonce gaps | 8 senders, 320 transactions, every 3rd transaction skips a nonce that is never sent | fill the queued set |
| P3 replacements | 4 senders, 40 nonces, each sent four times: original, identical, +1 wei, +20% | price-bump rules |
| C3b default | the P1 flood again, then watch mined count, pool and the `txpool_local` gauge for 230 s | does the node deliver the "lost" transactions later? |
| C3b `--txpool.nolocals` | the P1 flood again | what does the client see without the tracker? |

Hypothesis 1 (before the first run): the pool never exceeds its limits and excess transactions are rejected at submission with explicit errors. **Half wrong:** limits held, but no
error reached the client.

Hypothesis 2 (written into `c3b_locals_test.go` before it ran, after reading the v1.17.10 source): `SendTx` swallows temporary rejections and the tracker re-submits them every
minute; with `--txpool.nolocals` the tracker is not created and the client gets errors. **Confirmed on every point.**

## Observed (C3, first run)

| | P1 steady | P2 gaps | P3 replace |
|---|---|---|---|
| Attempted / accepted at RPC | 1,600 / **1,600** | 320 / **320** | 160 / 80 |
| Errors returned to the client | **none** | **none** | `already-known` 40, `replacement-underpriced` 40 |
| Mined (included) | **192** | 0 | 40 |
| Max pending / max queued seen | 128 / 0 | 24 / 64 | 40 / 63 |
| `txpool_valid` delta | 272 | 328 | 84 |
| Discard counters (delta) | `txpool_underpriced` **2,816**, `txpool_pending_ratelimit` 64 | `txpool_queued_ratelimit` **232** | `txpool_known` 40, `txpool_pending_discard` 40 |

## Observed (C3b, the experiment)

| | default (tracker on) | `--txpool.nolocals` |
|---|---|---|
| Attempted / accepted at RPC | 1,600 / **1,600** | 1,600 / **128** |
| Errors seen by the client | none | `underpriced` **1,472** |
| Mined at the end of the 12 s drain | 192 | 128 |
| `txpool_underpriced` delta | 2,816 (identical to C3: the run reproduced) | 1,472 |
| `txpool_valid` delta | 272 (identical to C3) | 144 |
| `txpool_local` gauge | 1,472 | **0** |
| Mined after 60 / 120 / 180 / 230 s | 256 / 320 / 384 / 384 | no change (nothing left to deliver) |
| `txpool_local` after 60 / 120 / 180 s | 1,408 / 1,344 / 1,280 | 0 |

The steps happen exactly at the 60 s marks of the watch (the tracker's one-minute timer), 64 transactions each (equal to `--txpool.globalslots 64`; I did not verify that this is why).
A manual run of the same flood with Prometheus attached (`docs/evidence/alert-txpool-dropping.txt`) showed `txpool_local` = 1,600 right after the flood and falling to 1,472 and 1,408 as
transactions were mined: the node *tracks* what it told the client it accepted.

## Root cause (v1.17.10 source, line numbers are for that tag)

1. `eth/api_backend.go:361-381 SendTx`: `err := txPool.Add(tx)`. If `localTxTracker == nil` the error goes back to the client. Otherwise: if the error is **not** a "temporary reject"
   it goes back; if it **is**, the transaction is passed to `localTxTracker.Track(tx)` and the function returns `nil`.
2. `core/txpool/locals/errors.go:29 IsTemporaryReject` lists `txpool.ErrUnderpriced`, `legacypool.ErrTxPoolOverflow`, `ErrFutureReplacePending`, `ErrInflightTxLimitReached`,
   `ErrOutOfOrderTxFromDelegated`, `ErrAuthorityReserved`.
3. When the pool is full, `LegacyPool.add` (`core/txpool/legacypool/legacypool.go:700-707`) returns `ErrUnderpriced` for a new transaction that is not better than the cheapest one in the pool.
   `pricedList.Underpriced` (`list.go:596-616`) uses `h.cmp(worst, tx) >= 0`, so a transaction **with the same price as the worst one is underpriced**. All 16 senders paid the same
   1 gwei tip, so once the pool was full everything after it was "underpriced". This also answers the open question of the first write-up (why no submission was *rejected* as
   underpriced: they were, the error never left the node).
4. `core/txpool/locals/tx_tracker.go:36 recheckInterval = time.Minute`, `:213` first check after 10 s: the tracker asks the pool whether it still has each tracked transaction and calls
   `pool.Add(resubmits)` for the rest. That is the 64-per-minute trickle. It also explains the counter: `txpool_underpriced` 2,816 = the 1,472 first-pass rejections plus the roughly
   1,344 transactions the 10-second recheck tried again and the pool rejected again (1,472 + 1,344 = 2,816; the 1,344 is my arithmetic from `txpool_local` 1,472 minus the 128 already mined, not a separate measurement).
   `txpool_underpriced` therefore counts **rejection attempts including tracker retries**, not evicted transactions.
5. `eth/backend.go:341`: the tracker exists only if `!config.TxPool.NoLocals`. With `--txpool.nolocals` step 1 returns the real error. The flag's help text says only "Disables price exemptions
   for locally submitted transactions", which does not mention that it also changes what the client is told.

Not established: whether the 64-per-minute rate is exactly `globalslots`; the exact split of the 272 `txpool_valid` between first pass and rechecks; behaviour with
`--txpool.rejournal` / `--txpool.journal` changes. P2's `txpool_queued_ratelimit` 232 was not re-analysed here; it comes from `truncateQueue`, a different path (a queue cut after acceptance).

## Impact

* "Hash returned" means "the node took responsibility" for locally submitted transactions, not "the pool holds it". Under overload the delivery is delayed by **minutes**, at a rate set by
  the pool limits, and the client sees no error and no hint. A client with its own timeout (typical: 1 to 2 minutes) will resubmit with a higher fee, which is an unnecessary replacement
  race, or conclude the transaction is lost.
* The node's memory and journal grow with the number of tracked transactions (`txpool_local`, journal file `transactions.rlp`).
* In a multi-tenant deployment (a public RPC endpoint) every client is "local" to that node: the behaviour is a property of RPC submission, not of who the sender is.
* Per-account limits make a single busy sender hit `ratelimit` long before the global limit; the queued set is a cheap place to park junk (P2: 64 gapped transactions filled the queue).

## Action items

| # | Item | Status |
|---|---|---|
| 1 | Alert `TxPoolDroppingTransactions` on `increase(txpool_underpriced[5m]) > 0 or increase(txpool_overflowed[5m]) > 0`. | **verified firing** on a real flood in 13 s (`docs/evidence/alert-txpool-dropping.txt`) |
| 2 | Add `txpool_local` to the dashboard and runbook: a growing gauge with an empty pool means the tracker is holding transactions the pool refused. | in `docs/METRICS.md` and `docs/RUNBOOK.md`; not on the dashboard yet |
| 3 | Loadgen reports `acceptedButNotIncluded`, the number that exposes this. | done |
| 4 | Operators who want back-pressure instead of silent queuing: run with `--txpool.nolocals` (clients get `transaction underpriced`) or size `--txpool.globalslots/globalqueue` for the expected burst. | documented; tested (C3b) |
| 5 | Candidate upstream docs note: the `--txpool.nolocals` help text and the `eth_sendRawTransaction` documentation should say that a successful return can mean "queued in the local tracker". Not filed; needs the owner's go (`docs/CONTRIBUTIONS.md` section 3). | proposed |
| 6 | Explain the 64-per-minute rate (read `pool.Add` batch handling, run with `--txpool.globalslots 128`, expect 128 per minute). | open, cheap |

## Reproduce

`scripts/chaos.sh C3` (first run) and `go test -tags chaos -count=1 -v -timeout 20m -run TestC3b ./test/chaos` (about 4.5 minutes). By hand:
`scripts/start-node.sh --period 4 -- --txpool.globalslots 64 --txpool.globalqueue 64 --txpool.accountslots 16 --txpool.accountqueue 16`, then
`bin/loadgen -senders 16 -txs 1600 -drain 12s`, and watch `curl -s 127.0.0.1:6060/debug/metrics/prometheus | grep -E '^txpool_(local|underpriced)'`.
For the contrast, add `-- --txpool.nolocals` to the start command.
