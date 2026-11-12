# C7: resource pressure (memory cap reproduced, disk-full analysed only)

Date: 2026-11-12. Test: `test/chaos/c7_memory_test.go` (`go test -tags chaos -count=1 -v -run TestC7 ./test/chaos`, about 1 to 3 minutes). Data:
`out/chaos/C7-memory-cap.json` (last run) and copies of three runs in `docs/evidence/C7-memory-cap.run{2,4,5}.json`; hang evidence in `docs/evidence/c7-hang-process.txt`.
Node: stock `geth 1.17.10 --dev`, Windows 10, node placed in a **Job Object with a 700 MB cap on committed memory**
(`internal/nodeproc/memlimit_windows.go`). Five runs of the whole test were made; they are summarised, not cherry-picked.

**Safety design.** The cap (700 MB) is below this lab's 1 GB line and enforced by the OS. If the cap cannot be applied the harness kills the node instead of running it uncapped. A watchdog kills the node if the job's
measured peak ever exceeds 900 MB (it never triggered; the highest peak was 729 MB). The job has kill-on-close, so a crashed test cannot leave a node behind. No disk-pressure experiment was run (see the last section).

## Summary

* A normal node is nowhere near a 700 MB cap: a 1,500-transaction flood peaked at **128 to 138 MB** committed, with every transaction mined.
* geth's default cache (`--cache 4096`, sanitised to 2,691 MB here, "Allocated trie memory caches clean=403 MiB dirty=672 MiB") **also ran fine under the cap**: peak 97 to 104 MB. My hypothesis that it would
  fail at startup was **wrong**: the caches are reserved lazily, not committed up front. A container limit smaller than the configured cache is therefore not noticed at startup; it bites later.
* One unbounded RPC is enough to kill a capped node: three concurrent `debug_traceCall` requests of an endless loop (`JUMPDEST; PUSH1 0; JUMP`, 50 M gas, default struct logger, which keeps every step in memory) took the node
  to **713 to 729 MB** in under 5 s, the OS refused the next allocation (`VirtualAlloc of 40345600 bytes failed with errno=1455`, `ERROR_COMMITMENT_LIMIT`) and the Go runtime printed `fatal error: out of memory`.
* **The crash did not always end the process.** In 3 of 5 runs the process stayed alive for the whole 75 s observation (and, in run 3, minutes) with **one thread left, listening sockets still open, RPC and metrics port not
  answering**; in 2 of 5 runs it exited with code 2 after 5.0 s. A liveness check on "process exists" says healthy; a check on RPC or metrics says dead.
* The recovery is the C1 problem again: an out-of-memory abort is an unclean shutdown. In every recorded run the restart on the same datadir rewound the head (4 to 0), took the `full-sync -> snap-sync` fallback
  and the node mined one transaction and then no more (`canMineSecondTx` false).

## Setup and hypotheses (written before the first run)

| Stage | Setup | Hypothesis |
|---|---|---|
| A control | cap 700 MB, `--cache 256`, `--dev.period 1`, 1,500 contract-call transactions at 150 tx/s | survives; peak far below the cap |
| B defaults | cap 700 MB, no `--cache` | does **not** survive: caches are allocated up front |
| C trace bomb | cap 700 MB, `--cache 256`, persistent datadir, `--dev.period 2`; 300 transactions first, then 3 concurrent `debug_traceCall` (endless loop via state override, 30 s tracer timeout) | the node passes the cap and aborts; the restart behaves like C1 |

## Observed

| | A control | B defaults | C trace bomb |
|---|---|---|---|
| Peak committed memory (job accounting) | 128 / 132 / 138 MB (runs 3, 5, 2) | 97 to 104 MB | **713 to 729 MB** (the peak can exceed the cap by about 2 to 4 %; accounting is in allocation-granularity steps) |
| Go heap in use (`system_memory_used`) | 61 to 67 MB | n/a | n/a (node dead) |
| Outcome | 1,500 of 1,500 mined, node alive | node alive and ready in 0.35 s | `fatal error: out of memory`, 3 client calls failed: `connection forcibly closed` (process exited) or `context deadline exceeded` after 60 s (process hung) |
| Time to node exit | n/a | n/a | **5.0 s, exit code 2** in 2 runs; **still alive after 75 s** in 3 runs |
| Probe while hung (runs 1, 3 and 4, probes every 5 s) | | | process alive, RPC no, metrics no, log file frozen at 478,831 bytes; by hand in run 3: `ThreadCount=1`, listening sockets still owned by the pid, client sockets in `CLOSE_WAIT` |
| Restart on the same datadir, same cap | | | ready in 0.30 s; **head 0** (was 4); 1 transaction mined, the second never got a receipt; log `Head state missing, repairing`, `Switching from full-sync to snap-sync reason="snap-sync incomplete"`, `Ignoring payload while snap syncing` |

Hypothesis verdicts: A **confirmed**; B **wrong** (lazy allocation); C **confirmed for the abort and the C1-style recovery**, with a surprise (the abort can hang).

## Root cause

1. **What exhausts the memory.** `debug_traceCall` with no explicit tracer uses the struct logger, which appends one log entry per EVM step to a slice that is only turned into JSON at the end. A loop of 3 steps per 12 gas in 50 M gas is
   about 12.5 million entries per call. The tracer timeout (30 s here, 5 s by default) and the gas cap (`--rpc.gascap` 50 M) are the only bounds; neither bounds *memory*. (From reading the behaviour; the exact per-entry size was not measured.)
   This is an RPC-level denial of service available to anyone who can reach the `debug` namespace, which is why `scripts/start-node.sh` exposing it is a lab convenience and SECURITY.md says never to expose it.
2. **Why the process dies.** The Job Object limit is on committed memory, so the allocation fails instead of the process being killed from outside. Go's runtime treats a failed `VirtualAlloc` as unrecoverable
   (`runtime.throw("out of memory")`), dumps all goroutines (about 480 of them, 400 to 470 KB of log) and exits with status 2. That exit is not a graceful shutdown: no "Persisting dirty state", so C1 applies.
3. **Why it sometimes hangs.** Not established. What was observed: after the fatal error the process had a single thread left and no longer served any socket, yet it did not exit for minutes (run 3, killed by hand after about 5 minutes; run 4, 75 s and killed by the harness).
   My candidate explanation is that the exit path itself needs memory the job no longer grants, but that is a guess; I did not test it (for example by raising the cap after the crash).

## Impact and operator lessons

* A memory limit lower than what an RPC can allocate turns one request into a node crash, and a crash into the C1 data loss on a default dev datadir. Real deployments need: the `debug` namespace off, `--rpc.evmtimeout`, a request
  concurrency limit in front of the node, and a limit *above* the node's working set with headroom (here the working set was 100 to 140 MB, 5 times lower than the cap, and still not safe).
* **Supervisors must check liveness by answering, not by existing.** `up == 0` (alert `GethDown`) would have fired in the hung case (the metrics endpoint stops answering) and `NoNewBlocks` after 4 minutes; a process-exists check would not.
* Alerts relevant here: `MemoryHigh` (`system_memory_used > 2e9`) would not have fired (the node died at 730 MB committed, and the Go heap number is lower than the committed number); a threshold relative to the actual cap is needed. Not changed in this pass.
* Memory is not what `--cache` makes the node use at startup (B): sizing the container from the cache flag alone is wrong in both directions.

## Disk full: analysis only, not reproduced

Filling a disk is the one experiment that can damage the machine (the C: drive is the system drive and other work shares it), so it was **not run**. What the v1.17.10 source says, to be tested on a disposable
volume (a small VHD, or a Linux tmpfs of 64 MB) some day:

* `cmd/utils/cmd.go:120-150` (`StartNode`): geth watches free space on the datadir's volume. The threshold is `--datadir.minfreedisk` (MB); the default is `2 * TrieDirtyCache` (512 MB with defaults) and, if `--cache` or
  `--cache.gc` is set, `2 * cache * gc% / 100`. Below the threshold it logs `Low disk space. Gracefully shutting down Geth to prevent database corruption` and sends itself SIGTERM; between one and two times the threshold it only warns.
  The check runs every 30 s (`monitorFreeDiskSpace`), so a writer faster than the headroom per 30 s can still hit a full disk first.
* In `--dev` without `--datadir` the instance directory is empty and the monitor returns immediately: **no protection for in-memory dev nodes** (and nothing to protect). `labnode` does not call `utils.StartNode`, so it
  has no disk monitor either (a gap in the lab's own node, not in geth).
* What to expect when a write does fail: Pebble returns an I/O error to the caller, geth's database layers log `Failed to write` style errors and the process is likely to stop with a fatal error, which is an unclean shutdown
  (C1). Not verified here.
* Test plan for the owner: a disposable volume, `--datadir.minfreedisk 0` to disable the monitor, flood until the volume is full, record the log, the exit code, and what a restart does; then repeat with the default monitor and
  see whether it shuts down in time at the flood rate used.

## Action items

| # | Item | Status |
|---|---|---|
| 1 | Job Object memory cap in `nodeproc` / `nodectl` / `start-node.sh --mem-limit-mb`, with a stub on other platforms | done |
| 2 | Note in RUNBOOK and SECURITY: `debug` is a DoS lever; liveness by answering | done |
| 3 | Explain the hang (candidate: exit path needs memory). Test by raising the job limit right after the fatal error and seeing whether the process then exits | open |
| 4 | An alert on committed memory relative to the container limit instead of the fixed `MemoryHigh` 2 GB | open |
| 5 | Disk-full experiment on a disposable volume | open, needs the owner's decision |
| 6 | Linux counterpart of the cap (cgroup `MemoryMax`) so that the CI chaos job can run C7 | open; the test skips itself on Linux |

## Reproduce

`go test -tags chaos -count=1 -v -timeout 10m -run TestC7 ./test/chaos` (Windows only; skips elsewhere). A capped node by hand: `scripts/start-node.sh --period 2 --mem-limit-mb 700 -- --cache 256`, then
three parallel `cast rpc debug_traceCall '{"to":"0x00000000000000000000000000000000000000aa","gas":"0x2faf080"}' latest '{"timeout":"30s","stateOverrides":{"0x00000000000000000000000000000000000000aa":{"code":"0x5b600056"}}}'`.
Expect a crash within seconds. Keep the cap at 700 MB or lower; do not run it uncapped, an uncapped node would try to allocate several GB.
