# C3: transaction flood beyond the txpool limits

Date: 2026-11-12. Test: `test/chaos/c3_txpool_test.go`. Data: `out/chaos/C3-txpool-overflow.json`. Node: stock
`geth 1.17.10 --dev --dev.period 4 --txpool.globalslots 64 --txpool.globalqueue 64 --txpool.accountslots 16 --txpool.accountqueue 16`,
in-memory chain, metrics on. Windows 10.

## Summary

With tiny pool limits and slow blocks, a client can submit transactions that the node **accepts (returns a hash) and later
discards without telling anyone**. In phase 1, 1,600 submissions all succeeded at the RPC level (zero errors), but only 192 were ever
mined; the pool was empty at the end. The only trace is the pool counters (`txpool_underpriced` rose by 2,816 in that phase).
Replacements behave as documented: an identical resend is "already known", a replacement with less than a 10% bump is
"replacement transaction underpriced", a bump of 20% replaces.

## Setup and hypothesis

Three phases on one node, each a `loadgen` run with its own senders:

| Phase | Load | Purpose |
|---|---|---|
| P1 steady flood | 16 senders, 1,600 transactions, unthrottled, blocks every 4 s | overflow the pool |
| P2 nonce gaps | 8 senders, 320 transactions, every 3rd transaction skips a nonce that is never sent | fill the queued set |
| P3 replacements | 4 senders, 40 nonces, each sent four times: original, identical, +1 wei, +20% | price-bump rules |

Hypothesis before the run: the pool never exceeds its limits; excess transactions are rejected at submission with explicit errors
(`txpool is full`, `transaction underpriced`); gaps stay queued and are not mined; replacement needs a 10% bump.

## Observed

| | P1 steady | P2 gaps | P3 replace |
|---|---|---|---|
| Attempted / accepted at RPC | 1,600 / **1,600** | 320 / **320** | 160 / 80 |
| Errors returned to the client | **none** | **none** | `already-known` 40, `replacement-underpriced` 40 |
| Mined (included) | **192** | 0 | 40 |
| Max pending / max queued seen | 128 / 0 | 24 / 64 | 40 / 63 |
| Final pending / queued | 0 / 0 | 24 / 64 | 0 / 63 |
| `txpool_valid` delta | 272 | 328 | 84 |
| Discard counters (delta) | `txpool_underpriced` **2,816**, `txpool_pending_ratelimit` 64 | `txpool_queued_ratelimit` **232** | `txpool_known` 40, `txpool_pending_discard` 40 (the replaced originals) |

Reading the numbers:

* P1: 1,600 accepted, 192 mined = 3 blocks x 64 pending slots (one block every 4 s during the 12 s drain). The remaining 1,408 accepted
  transactions are neither mined nor pending at the end (final pending 0): they were evicted after being accepted.
* P2: 320 accepted, but only 24 pending + 64 queued = 88 remain; 232 were dropped from the queue (`txpool_queued_ratelimit` 232). The
  queue holds exactly `--txpool.globalqueue 64`. Nothing is mined because pending transactions wait for the next 4 s block and the
  run ends after 2 s.
* P3: of 160 submissions, 40 originals were accepted, 40 identical resends were rejected, 40 `+1 wei` replacements rejected, 40 `+20%`
  replacements accepted. 40 mined. The `63` queued left over are from P2's senders (the pool is shared).

Hypothesis verdict: pool size limit held (max pending 128, i.e. up to 2x `globalslots` transiently, never unbounded), but the
**"explicit rejection at submission" part was wrong**: overflow is silent.

## Root cause

The code path is `LegacyPool.add` (`core/txpool/legacypool/legacypool.go:660`), lines 700 to 759. When the pool is full
(`all.Slots() + new > GlobalSlots + GlobalQueue`, line 701):

1. if the new transaction is not better than the cheapest one in the pool (`priced.Underpriced`, line 703), it is **rejected** with
   `ErrUnderpriced` and the client sees an error;
2. otherwise the pool makes room by **evicting the cheapest existing transactions** (`priced.Discard`, line 720; removal at
   line 755) and each eviction is counted with `underpricedTxMeter.Mark(1)` (line 752) and logged only at Trace level (line 751).
   The sender of an evicted transaction already holds its hash; nobody tells it.

The measured `txpool_underpriced` delta of 2,816 in P1 is that line-752 counter, i.e. evictions of already-accepted transactions. Later
cleanup in `truncatePending` (`:1431`) and `truncateQueue` (`:1511`) produces the `ratelimit` counters (64 and 232). That no submission
received `ErrUnderpriced` in P1 means every new transaction compared as *better* than the cheapest queued one, although all
senders used the same 1 gwei tip. The loadgen refreshes its fee cap from the current base fee every 500 ms, so a price drift between
submissions is the likely reason, but this was **not verified** (action item 3).

## Impact

* A client that treats "hash returned" as "will be mined" can lose transactions under load without any error. It must poll for the
  receipt, with a timeout and resubmission, or track `txpool_*` discard counters on the node it uses.
* Per-account limits (16 executable, 16 queued here; 16 and 64 by default) make a single busy sender hit `ratelimit` long before the
  global limit.
* The queued set is a cheap place to park junk: 64 gapped transactions filled the whole queue in P2.

## Action items

| # | Item | Status |
|---|---|---|
| 1 | Alert `TxPoolDroppingTransactions` on `increase(txpool_underpriced[5m]) > 0 or increase(txpool_overflowed[5m]) > 0`. | in `monitoring/prometheus/alerts.yml` (untested) |
| 2 | Loadgen reports `acceptedButNotIncluded`, which is the number that exposes this behaviour. | done |
| 3 | Find out why no P1 submission was rejected as underpriced (log fee caps per submission, run with identical fixed fee caps) before relying on this explanation upstream. | open |
| 4 | Candidate docs note for `--txpool.*` / `eth_sendRawTransaction`: "accepted does not mean retained". | proposed in `docs/CONTRIBUTIONS.md` section 3; needs the owner's go |
| 5 | Repeat with `--txpool.nolocals` and with fixed identical fee caps to see whether submissions are rejected instead of evicted later. | open |

## Reproduce

`scripts/chaos.sh C3`. By hand: `scripts/start-node.sh --period 4 -- --txpool.globalslots 64 --txpool.globalqueue 64 --txpool.accountslots 16 --txpool.accountqueue 16`,
then `bin/loadgen -senders 16 -txs 1600` and `bin/loadgen -scenario gap -gap-every 3 -senders 8 -txs 320`, then `bin/loadgen -scenario replace -senders 4 -txs 40`.
