# C6: JSON-RPC abuse and limits

Date: 2026-11-12. Test: `test/chaos/c6_rpc_abuse_test.go`. Data: `out/chaos/C6-rpc-abuse.json`. Node: stock `geth 1.17.10 --dev` with
HTTP on loopback, default limits, metrics on; a second node for the batch-response limit. Windows 10.

## Summary

Every limit that `geth --help` advertises held, and none of the abuse disturbed other clients. Batch of 1,001 items: refused in 1 ms. A 6 MB body:
HTTP 413. A client that sends half a request and stops is disconnected after exactly 30 s while 20 normal requests made during that time all
succeeded. 400 idle TCP connections cost nothing visible. An infinite loop in `eth_call` ends in "out of gas" after 54 ms because the 50 M
gas cap is reached long before the 5 s EVM timeout.

## Setup and hypothesis

Hypothesis (from the defaults in `geth --help`): batch limit 1000 (`--rpc.batch-request-limit`), body limit 5 MB
(`--rpc.http-body-limit`), HTTP read timeout 30 s, EVM timeout 5 s (`--rpc.evmtimeout`), batch response cap 25,000,000 bytes
(`--rpc.batch-response-max-size`); none of it disturbs other clients.

## Observed

| Probe | Observed |
|---|---|
| Batch of 1,000 `eth_blockNumber` | HTTP 200, 5.1 ms, 41,891 bytes back |
| Batch of 1,001 | HTTP 200, 1.0 ms, 78 bytes: a **single** error object `{"id":0,"error":{"code":-32600,"message":"batch too large"}}` for the whole batch |
| Body 4 MB (a `web3_sha3` over 4 MB of hex) | HTTP 200, 15.8 ms, correct hash |
| Body 6 MB | **HTTP 413**, 1.1 ms, text `content length too large (6291517>5242880)` |
| `eth_call` with code `5b 5f 56` (JUMPDEST PUSH0 JUMP) via a state override | HTTP 200 after 54 ms: `{"error":{"code":-32000,"message":"out of gas"}}` |
| 400 idle TCP connections, then a normal request | all 400 opened; the request got HTTP 200 in under 1 ms |
| Slow client: `POST` with `Content-Length: 1000`, 12 bytes of body, then silence | server closed the connection after **30.008 s** (read error EOF, empty reply); 20 of 20 other requests made meanwhile succeeded |
| Node health at the end | HTTP 200; `rpc_requests` +1,024, `rpc_success` +1,023, `rpc_failure` +1 |
| Second node, `--rpc.batch-response-max-size 3000`, batch of 100 `eth_getBlockByNumber(0, true)` | HTTP 200, 11,377 bytes; **98 of 100 items** are `{"code":-32003,"message":"response too large"}` |

Hypothesis verdict: confirmed on every point. Two details differ from what the flags suggest:

* The batch refusal is one error for the batch, not one per item, and the HTTP status is still 200. A client library that only checks the HTTP status misses it.
* The EVM timeout (5 s) never fires in this probe: the **gas cap** (50 M, `--rpc.gascap`) bites first. A call that burns time instead of gas
  (heavy precompiles, large memory copies) is what the 5 s timeout is for; it was not tested.

## Root cause / where the limits live

* Body size: `rpc/http.go:412` compares `Content-Length` with `httpBodyLimit` and answers 413 with "content length too large"; larger content lengths are
  refused before parsing.
* Batch size and response size: `rpc/handler.go:179 handleBatch`; `errMsgBatchTooLarge` at line 210 (`batch too large`);
  `batchResponseMaxSize` check at line 263 to 265 with `errMsgResponseTooLarge = "response too large"` (`rpc/errors.go:72`).
* Slow clients: `rpc.DefaultHTTPTimeouts` (`rpc/http.go:125`, copied into the node config at `node/defaults.go:65`) sets a 30 s read timeout; Go's `net/http`
  closes the connection when it fires. The server is goroutine-per-connection, so 400 idle sockets are 400 parked goroutines and cheap.

## Impact and what is NOT covered

* Safe by default on loopback. Not tested: thousands of connections (file descriptor limits), WebSocket subscriptions that never read
  (`--ws`), expensive `debug_trace*` calls (the `debug` namespace should never be exposed), request floods saturating CPU. No rate limiter exists in Geth; put a
  reverse proxy in front of any shared endpoint.
* The memory pressure part of the original C7 idea (below) was not attempted for the same reason: the machine has about 1 GB free.

## Action items

| # | Item | Status |
|---|---|---|
| 1 | Runbook: client libraries must check each element of a batch response and treat `-32600 batch too large` as a failure. | `docs/RUNBOOK.md` |
| 2 | Alert `RpcErrorRatioHigh` on `rpc_failure / rpc_requests`. Caveat from this run: one rejected batch counted as a single failure while 1,001 items were refused. | in alerts (untested) |
| 3 | Test a time-bound `eth_call` (precompile loop) to see the 5 s EVM timeout and the node's CPU behaviour. | open |

## Reproduce

`scripts/chaos.sh C6` (takes about 32 s because of the 30 s slow-client probe).

# C7 (disk / memory pressure): not run

Decision: not reproducible safely on this machine. Filling the disk risks the other work on the same drive; limiting memory below what
geth sanitises to would need a job object or container, and only about 1 GB of RAM was free while another build was running. What exists
instead: C4 measured memory under three `--cache` values (12.8 to 29.7 MB of heap, because caches fill lazily), and `system_memory_*`
and `MemoryHigh` are in the catalogue and alerts. Next step when a Linux box with Docker or cgroups is available: run geth under a
512 MB memory limit with `--cache 4096` and a state-heavy workload, and watch `system_memory_used` and the OOM kill.
