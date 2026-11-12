# Postmortems

Each file describes a failure injected on purpose into a real `geth 1.17.10 --dev` node (or `labnode`), with the
timeline, expected versus observed, numbers from the run, root cause and action items. Numbers come from
`out/chaos/*.json`, written by `go test -tags chaos ./test/chaos` (run on 2026-11-12, Windows 10, one full pass: 6 scenarios, all passed, 349 s).
Hypotheses were written down before the runs; where they were wrong, the postmortem says so.

| ID | Scenario | One-line result |
|---|---|---|
| [C1](C1-kill9-under-load.md) | `kill -9` under load, restart on the same datadir | Default config: head rewound from 5 to 0, 1,648 mined transactions gone, node stuck in snap-sync mode and stops producing blocks, although RPC answers in 0.5 s. With `--gcmode archive --state.scheme hash`: nothing lost, 203 of 203 pool-only transactions recovered by the journal. |
| [C2](C2-graceful-vs-kill.md) | Graceful stop vs hard kill | Graceful: 77 ms, same head, fully functional. Kill: 46 ms, head 8 to 0, stuck. Block database stays internally consistent. |
| [C3](C3-txpool-overflow.md) | Flood beyond txpool limits | 1,600 submissions accepted with no error, 192 mined: accepted transactions are evicted silently (2,816 eviction marks). Gaps fill the queue exactly to its cap; replacements need a 10% bump. |
| [C4](C4-restart-changed-flags.md) | Same datadir, changed `--cache` / `--gcmode` / `--state.scheme` | `--cache` free; `--gcmode archive` silently accepted; `--state.scheme hash` on a path database refused with a clear fatal error. |
| [C5](C5-rewind-and-reorg.md) | Rewind and reorg (`debug_setHead`, `lab_fork`) | Both produce `REWIND` then `REORG depth=3` in `watch`; transactions of dropped blocks are re-mined. |
| [C6](C6-rpc-abuse.md) | RPC abuse | All advertised limits hold (batch 1000, body 5 MB, 30 s slow-client cut-off, response cap); node unaffected. |
| C7 | Disk / memory pressure | **Not run**, reasons at the end of the C6 file. |

Template used: Summary, Setup and hypothesis, Timeline (when it helps), Observed (table), Root cause, Impact, Action items, Reproduce.
