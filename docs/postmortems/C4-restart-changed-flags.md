# C4: restart the same datadir with different `--cache`, `--gcmode`, `--state.scheme`

Date: 2026-11-12. Test: `test/chaos/c4_config_test.go`. Data: `out/chaos/C4-restart-changed-flags.json`.
Node: stock `geth 1.17.10 --dev --datadir D`. Windows 10.

## Summary

`--cache` is a free runtime knob and `--gcmode archive` can be switched on for an existing path-scheme database without complaint.
Changing the **state scheme** of an existing database is refused at startup with a clear fatal error, and the refusal leaves the
datadir untouched. All successful restarts returned the same head and mined a new transaction.

## Setup and hypothesis

Seed: default flags, 400 transactions at 100 tx/s (head 405 after funding), graceful stop. Datadir 5,279,695 bytes. Then five restarts
on the same datadir, each followed by a head read and one mined transaction, then a graceful stop. Node health also read from the
metrics endpoint (`system_memory_used`).

Hypothesis before the run: `--cache` is harmless; the other two are either refused with an error or silently accepted. Unknown which.

## Observed

| Restart flags | Result | Ready | Head | New tx mined | Evidence |
|---|---|---|---|---|---|
| (seed, defaults) | | | 405 | | `Sanitizing cache to Go's GC limits provided=4096 updated=2691`, `Allocated trie memory caches clean=403MiB dirty=672MiB` |
| `--cache 128` | started | 0.56 s | 405 | yes | `clean=19MiB dirty=32MiB`; `system_memory_used` 29.7 MB |
| `--cache 2048` | started | 0.36 s | 405 | yes | `clean=307MiB dirty=512MiB`; 12.8 MB |
| `--gcmode archive` | started | 0.32 s | 405 | yes | `Enabling recording of key preimages since archive mode is used`, `Disabled transaction unindexing for archive node`, `State scheme set to already existing scheme=path` |
| `--state.scheme hash` | **did not start**, exit status 1 | n/a | n/a | n/a | `Fatal: Failed to register the Ethereum service: incompatible state scheme, stored: path, provided: hash` |
| `--gcmode full --state.scheme path` | started | 0.31 s | 405 | yes | `State scheme set by user scheme=path` |

Hypothesis verdict: confirmed, and the answer to "refused or silently accepted" is: **scheme refused, gcmode accepted silently.**

## Interpretation and root cause

* The cache split is derived at start-up from `--cache` (50 % database, 15 % trie clean, 25 % trie dirty/gc, 10 % snapshot;
  `cmd/utils/flags.go:1855-1920`). It sizes in-memory structures only, so any value is valid for any database. Note that the *dirty*
  trie cache (672 MB by default here) is what the path database may hold unflushed: a bigger cache means more state at risk in a
  crash (C1) and a longer "Persisting dirty state" at a graceful stop.
* The state scheme is stored with the database. `rawdb.ParseStateScheme(requested, chainDb)` (error text at `core/rawdb/accessors_trie.go:291`, called from
  `cmd/utils/flags.go:2548`) compares the flag with the stored scheme and returns the fatal error above. Converting needs a resync or
  re-import, not a flag.
* `--gcmode archive` on a path-scheme database only changes how new blocks are retained and enables key-preimage recording; it does not
  rewrite existing history (and archive with path scheme is a different thing from archive with hash scheme, which is the one C1 shows
  as crash-safe). This lab did **not** test whether the path+archive combination survives `kill -9`.

## Impact for operators

* Tuning `--cache` needs no migration; check the log line `Allocated trie memory caches` to see what you actually got, because Geth
  sanitises the flag against the Go GC limit of the machine (4096 became 2691 here).
* A wrong `--state.scheme` is caught immediately. A wrong `--gcmode` is not: archive mode can be enabled by accident and the node
  starts, logging two INFO/WARN lines that are easy to miss.

## Action items

| # | Item | Status |
|---|---|---|
| 1 | Runbook entry "changing storage flags on an existing datadir" with this table. | `docs/RUNBOOK.md` |
| 2 | Test archive+path under `kill -9` (not covered by C1). | open |
| 3 | Measure graceful-stop time against `--cache` on a datadir with more state (the 5 MB datadir here is too small to show it). | open |

## Reproduce

`scripts/chaos.sh C4`.
