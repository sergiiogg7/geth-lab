# Demo: captured output of `scripts/demo.sh`

This is the **real, unedited output** of one run of `scripts/demo.sh` on 2026-11-12 (Windows 10, Git Bash, geth 1.17.10, Go 1.27.4),
with only two cosmetic changes: ANSI colours removed and the absolute repo path replaced by `.`. The script exited 0 with all 7 assertions passing.
Run it yourself: `scripts/demo.sh` (about 2 minutes; needs Go, Foundry's `cast`, and `geth` on PATH or in `$GOPATH/bin`).

What each section shows:

| Section | What it demonstrates |
|---|---|
| 0 and 1 | Building the tools; starting `labnode` (Geth embedded as a Go library) and a `cast`-based JSON-RPC smoke test |
| 2 | `watch`: follows heads, prints block time and gas fill; also serves a status page |
| 3 | `loadgen`: 800 contract calls at 250 tx/s with throughput, inclusion-latency percentiles, gas fill and txpool behaviour; the `lab_txpoolStats` and `lab_blockFill` custom RPC methods |
| 4 | The custom `gasProfile` EVM tracer through `lab_gasProfile`: one `SSTORE` costs 10,100 execution gas plus 97,920 state gas on this dev chain |
| 5 | A forced reorg of depth 3 and the watcher's `REWIND` then `REORG` |
| 6 | Real Prometheus metrics straight from the node |
| 7 | Chaos C1 on stock geth: `kill -9` under load. Default config: chain rolled back to genesis and block production stuck (asserted). Archive + hash: survives (asserted). Full write-up: `docs/postmortems/C1-kill9-under-load.md` |

Notes on reading the output: "RPC ready after Ns" includes the start-up time of the `cast` process used for polling (about 0.3 s);
`included 197 tx/s` is limited by the 1 s block period and the 8.7 M gas per block the node fits (about 255 of these contract calls);
inclusion latency is measured by the client from send to first observation of the block.

```text

### 0. Clean slate and build the lab tools
 OK built: labnode.exe loadgen.exe nodectl.exe watch.exe 
    geth binary: geth  ->  Version: 1.17.10-stable

### 1. Start labnode: Geth embedded as a Go library + the lab_ JSON-RPC namespace
    dev mode = one node, simulated consensus client, a block every second, prefunded dev account
 OK RPC ready after 0.6s at http://127.0.0.1:8545 (pid 10100, log ./.lab/node.log)
 in-memory chain: state disappears when the node stops
 OK recipient balance 0 -> 1
 OK lab_nodeInfo: {"version":"labnode/0.1.0 (go-ethereum v1.17.10)","chainId":1337,"datadir":"","persistent"
 OK lab_blockFill: [{"number":3,"hash":"0x48e48037a55b15fdeb153b171cbe73366cbbd135c4bfe79c86a667d2e44eb95c","
 OK smoke test passed

### 2. Watcher: follows heads, prints block time/gas, detects reorgs (also serves a status page)
10:44:01.132 block  #4 0xe9bfd943 txs=0 gas=0/11544982 (0.0%) blocktime=1s
10:44:02.132 block  #5 0x82f39c88 txs=0 gas=0/11556255 (0.0%) blocktime=1s
10:44:03.134 block  #6 0x976679e2 txs=0 gas=0/11567539 (0.0%) blocktime=1s

### 3. Load: 800 contract calls (1 SSTORE each) with 64 bytes of calldata from 8 senders at 250 tx/s
funding 8 senders (waiting for receipts)...
scenario=steady mode=contract senders=8 dataBytes=64 gas/tx=146587
attempted=800 accepted=800 included=800 acceptedButNotIncluded=0
send phase 3.21s (249 accepted tx/s), total 4.11s, included 197 tx/s
inclusion latency ms (n=800): min 23 p50 567 p90 955 p99 1041 max 1068
blocks with txs=4 avgGasFill=59.2% maxTxsPerBlock=253
txpool: max pending=210 max queued=0 | final pending=0 queued=0
--- txpool as the lab_ namespace sees it, and the last blocks:
{"pending":0,"queued":0,"pendingAccounts":0,"queuedAccounts":0,"pendingBytes":0,"minTipWei":"0","maxTipWei":"0","largestAccountPending":0,"observedAt":"2026-11-12T10:44:08.2775757Z"}
[{"number":11,"hash":"0xf43cf515225780c712588fbdb61bd5c868e6c109bcbe79daa2b38e7c6d949bf9","txs":57,"gasUsed":1951338,"gasLimit":11624124,"fillPercent":16.78696820508797,"intervalSeconds":1,"baseFeeWei":"4098462
{"number":10,"hash":"0x6f4cc8aa6b89485a7e35e524acace1923850b4548ec3eaaddf41573554c180c0","txs":252,"gasUsed":8626968,"gasLimit":11612785,"fillPercent":74.28853629857093,"intervalSeconds":1,"baseFeeWei":"3863844
{"number":9,"hash":"0x05272e5d22f806c1bfa0e7211cc39e3bb14856cd79b0ac0d0271169b67cc2c8f","txs":253,"gasUsed":8661202,"gasLimit":11601457,"fillPercent":74.65615741195265,"intervalSeconds":1,"baseFeeWei":"36395041
{"number":8,"hash":"0x4f3507a435c74f940b5ca258a35fbc39b9617a8131ffeba6e22f97953fda4971","txs":238,"gasUsed":8245612,"gasLimit":11590140,"fillPercent":71.14333390278289,"intervalSeconds":1,"baseFeeWei":"34567842
 OK ASSERT all 800 txs were mined (txpool empty)

### 4. Custom EVM tracer: where did the gas of one transaction go? (lab_gasProfile)
    counter contract 0xcf950f044e11d2e0318314ecd940ec19abf51130, call tx 0x95ea9c8e68e6bc4bdc40020fc6cbf9ccd3f15863921c913282bbe6d7749995e5
{"gasUsed":131130,"intrinsicGas":21000,"failed":false,"steps":7,"frames":1,"maxDepth":1,"reverts":0,"storageWrites":1,"executionGas":12210,"stateGas":97920,"gasByCategory":{"compute":10,"storage":110120},
  ops:
    {"op":"SSTORE","count":1,"gas":10100,"stateGas":97920},
    {"op":"SLOAD","count":1,"gas":2100,"stateGas":0},
    {"op":"PUSH0","count":2,"gas":4,"stateGas":0},
    {"op":"ADD","count":1,"gas":3,"stateGas":0},
    {"op":"PUSH1","count":1,"gas":3,"stateGas":0},
    {"op":"STOP","count":1,"gas":0,"stateGas":0}]}
 OK ASSERT profile says exactly one SSTORE

### 5. Force a real reorg of depth 3 and see the watcher react
{"oldHead":"0x76294a186921ce550e0411e96aad76d477e7f53da77423bb3629aa1acb0e08f5","oldNumber":17,"newHead":"0x5977e0ce8a33672e68021f230d5071422bb1978f561a6ac8f70a36c843c3c551","newNumber":14}
10:44:14.734 REWIND head #17 -> #14 (dropped 3 blocks, now at known canonical 0x5977e0ce)
10:44:15.333 REORG  depth=3 added=1 ancestor=#14 old=#17 0x76294a18 new=#15 0xc6a2ad70
 OK ASSERT watcher reported a REORG
--- status page JSON (http://127.0.0.1:8081/status.json):
{"rpc":"http://127.0.0.1:8545","client":"labnode/vlabnode/0.1.0(go-ethereumv1.17.10)/windows-amd64/go1.27.4","up":true,"head":20,"headHash":"0x22f23b9cae427222abe3d85fc6d6e1d2ef8f0af2e4e3f92bff0f214f4ab72a46","lastBlockAt":"2026-11-12T10:44:20.334444Z","secondsSinceLastBlock":0.3941505,"blocksSeen":20,"reorgs":1,"rewinds":1,"max


### 6. Prometheus metrics straight from the node (no Prometheus needed)
metric families: 785
--- selected values ---
chain_head_block 20
chain_head_finalized 0
chain_head_header 20
chain_head_safe 20
chain_inserts_count 23
p2p_peers 0
rpc_failure 0
rpc_requests 1162
rpc_success 1162
txpool_invalid 0
==> graceful stop pid 10100 (labnode)

### 7. Chaos C1: kill -9 under load, then restart on the SAME datadir (stock geth, defaults)
    hypothesis: sealed blocks survive; only unmined pool txs are at risk
 OK RPC ready after 0.6s at http://127.0.0.1:8545 (pid 8212, log ./.lab/node.log)
    before kill: head=6 txpool={"pending":"0x217","queued":"0x0"}
==> hard kill pid 8212
 OK RPC ready after 0.6s at http://127.0.0.1:8545 (pid 9200, log ./.lab/node.log)
    after restart: head=0
WARN [11-12|10:44:35.593] Head state missing, repairing            number=6 hash=a691c0..f670fe
WARN [11-12|10:44:35.607] Switching from full-sync to snap-sync    reason="snap-sync incomplete"
WARN [11-12|10:44:35.607] Unclean shutdown detected                booted=2026-11-12T10:44:22+0000 age=13s
 OK ASSERT kill -9 rolled the chain back (blocks were lost)
    is the node functional? send one tx now, then another 6 s later (period 2 s => each should be mined)
    head after first tx: 1, 8 s after the second tx: 1
 OK ASSERT block production is stuck after the crash (see docs/postmortems/C1)
    log lines 'Ignoring payload while snap syncing': 1
==> hard kill pid 9200
    mitigation: same crash with --gcmode archive --state.scheme hash
 OK RPC ready after 0.6s at http://127.0.0.1:8545 (pid 16120, log ./.lab/node.log)
==> hard kill pid 16120
 OK RPC ready after 0.6s at http://127.0.0.1:8545 (pid 20536, log ./.lab/node.log)
    before kill: head=6  after restart: head=5
 OK ASSERT archive+hash loses at most the in-flight block
 OK ASSERT block production continues after the crash
==> graceful stop pid 20536 (geth)

### Done
 OK demo finished, all assertions passed
exit=0
```
