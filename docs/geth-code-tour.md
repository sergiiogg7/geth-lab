# Geth code tour (go-ethereum v1.17.10)

A guided reading list of the Geth source, written for a Java backend developer. Every path and line number
below was checked against the module cache copy of **v1.17.10**
(`$GOPATH/pkg/mod/github.com/ethereum/go-ethereum@v1.17.10`). Line numbers drift between releases: treat them as
"search for this symbol", not as permanent addresses.

How to read each section: **Question** (what problem the code solves), **Entry point**, **Key types**,
**Flow**, **Java analogy**, and **Where the lab touches it**.

Contents: [1 Startup and CLI](#1-startup-and-cli-cmdgeth) - [2 Node and lifecycles](#2-the-node-container-and-service-lifecycle-node) -
[3 JSON-RPC server](#3-json-rpc-server-rpc-and-an-eth_-method-end-to-end) - [4 Txpool](#4-the-transaction-pool-coretxpool) -
[5 Block production in dev mode](#5-block-production-in-dev-mode-ethcatalyst-and-miner) - [6 EVM entry](#6-evm-entry-corestate_transitiongo-corevm) -
[7 State and trie databases](#7-state-and-trie-databases) - [8 Sync](#8-sync-ethdownloader) - [9 Metrics](#9-metrics) -
[10 Cheat sheet: where the lab plugs in](#10-where-the-lab-plugs-in)

A mental model before diving in: **Geth is the "execution client".** After the Merge it does not decide which
block comes next; a "consensus client" (Lighthouse, Prysm...) does, and talks to Geth over the authenticated
**Engine API**. Geth validates and executes blocks, keeps state, serves JSON-RPC, and keeps the transaction pool.
In `--dev` mode there is no consensus client, so Geth contains a fake one (`SimulatedBeacon`, section 5).

---

## 1. Startup and CLI (`cmd/geth`)

**Question:** how does a command line become a running node?

**Entry point:** `cmd/geth/main.go`
- `func main()` (line 270) builds the `urfave/cli` app and runs it.
- `func init()` (203) registers the app: the default action is `geth` (296), plus sub-commands (`console`,
  `init`, `db`, `snapshot`, `dumpconfig`...). The flag list is a big slice; `utils.DeveloperFlag` is at line 128.
- `func prepare(ctx)` (279) sets caches, logging, metrics flags.
- `func geth(ctx)` (296) is the default action: `makeFullNode` then `startNode` (312) then `stack.Wait()`.

**Key code**
- `cmd/geth/config.go`: `makeConfigNode` (166) loads defaults + TOML + flags and calls `node.New`;
  `makeFullNode` (226) adds the services: `utils.RegisterEthService` (the Ethereum backend), the filter API,
  and in dev mode the simulated beacon (lines 299 to 311) or otherwise the Engine API for a real consensus client
  (`catalyst.Register`, 321).
- `cmd/utils/flags.go`: every flag definition (`DeveloperFlag` 162, `DeveloperPeriodFlag` 167) and the code that
  turns flags into config structs: `SetEthConfig` (1790), the dev genesis branch (2016 to 2097),
  `RegisterEthService` (2255), `SetupMetrics` (2371).

**Flow:** `main -> app.Run -> geth() -> makeFullNode() -> [node.New, eth.New via RegisterEthService, register
APIs/lifecycles] -> startNode() -> node.Start() -> wait for Ctrl-C -> node.Close()`.

**Java analogy:** `main()` + Spring Boot `SpringApplication.run`. `urfave/cli` is picocli; `gethConfig` is
`@ConfigurationProperties`; `RegisterEthService` is a `@Bean` method.

**Where the lab touches it:** `cmd/labnode/main.go` re-implements `makeFullNode` for dev mode in about 150
lines (a good way to see which parts of `cmd/geth` are essential). `scripts/start-node.sh` passes the flags.

---

## 2. The node container and service lifecycle (`node/`)

**Question:** who owns the datadir lock, the RPC servers, the database handles, and starts/stops services in
order?

**Entry point:** `node/node.go`
- `type Node` holds config, the account manager, the lifecycle list, RPC servers (`http`, `ws`, `httpAuth`...).
- `func New(conf *Config)` (77): validates config, takes the datadir lock, creates the RPC server objects.
- `func (n *Node) Start()` (163): `openEndpoints` (267) first (listeners up), then calls `Start()` on every registered
  `Lifecycle` in order; if one fails the started ones are stopped.
- `RegisterLifecycle` (548), `RegisterProtocols` (562), `RegisterAPIs` (573): how a service plugs itself in.
- `func (n *Node) Close()` (206) stops lifecycles in reverse order and releases the datadir.
- `startRPC` (376) decides which of HTTP / WS / IPC / authenticated Engine API endpoints exist and which API
  namespaces each exposes.

**Key types:** `node.Lifecycle` (`node/lifecycle.go:23`: `Start() error`, `Stop() error`), `rpc.API`
(`{Namespace, Service, Authenticated}`), `node.Config` (`node/config.go`), HTTP server wrapper
`httpServer` in `node/rpcstack.go` (`start` 135, `ServeHTTP` 206, `enableRPC` 307).

**Flow:** services call `stack.RegisterAPIs(...)` and `stack.RegisterLifecycle(...)` while being constructed
(see `eth/backend.go` lines 385 to 387); `Start()` then brings up transports and starts lifecycles.

**Java analogy:** `ApplicationContext` with `SmartLifecycle` beans; `RegisterAPIs` is registering
`@RestController`s with the embedded Tomcat.

**Where the lab touches it:** `cmd/labnode` calls `node.New`, `stack.RegisterAPIs` (the `lab_` namespace) and
`stack.RegisterLifecycle(beacon)`.

---

## 3. JSON-RPC server (`rpc/`) and an `eth_` method end to end

**Question:** how does `{"method":"eth_blockNumber"}` reach a Go function?

**Entry points:** `rpc/server.go`
- `NewServer` (63), `RegisterName(name, receiver)` (108), `ServeCodec` (117) for long-lived connections (WS, IPC),
  `serveSingleRequest` (165) for HTTP.
- `rpc/http.go`: `Server.ServeHTTP` (373) reads one body (limit `--rpc.http-body-limit`, 5 MB default; larger gives
  HTTP 413) and hands it to the handler.
- `rpc/handler.go`: `handleMsg` (299) dispatches single messages, `handleBatch` (179) batches (limit
  `--rpc.batch-request-limit`, 1000; response size cap `--rpc.batch-response-max-size`, 25 MB: items beyond it come back as
  `response too large`), `handleCallMsg` (518) and `handleCall` (553) resolve the method and call it.
- `rpc/service.go`: `registerName` (60) uses **reflection** (`suitableCallbacks` 119, `newCallback` 139) to turn
  every exported method of a struct into `namespace_methodName` (first letter lower-cased); `callback.call` (187)
  invokes it; `serviceRegistry.callback` (99) is the lookup.

**An `eth_blockNumber` trace**
1. `rpc/http.go:373 ServeHTTP` -> `handler.handleMsg`.
2. The registry finds the callback registered by `internal/ethapi`: `internal/ethapi/backend.go:108 GetAPIs`
   creates `BlockChainAPI` under namespace `eth`.
3. `internal/ethapi/api.go:329 BlockChainAPI.BlockNumber()` calls `api.b.HeaderByNumber(ctx, rpc.LatestBlockNumber)`.
4. `api.b` is the `ethapi.Backend` interface; the implementation is `eth/api_backend.go` (`EthAPIBackend`,
   `HeaderByNumber` 90, `CurrentBlock` 63), which reads `bc.CurrentBlock()` from `core/blockchain.go`.
5. The result is a `hexutil.Uint64` and is JSON-encoded by the codec.

**Java analogy:** a hand-rolled JAX-RS container: `@Path` is the namespace, method discovery is reflection, the
`Backend` interface is the service layer behind the controller. Struct fields `*TraceConfig` that are pointers become
optional parameters.

**Where the lab touches it:** `internal/labapi` defines the `lab_` service; its methods become `lab_nodeInfo`,
`lab_txpoolStats`, `lab_blockFill`, `lab_gasProfile`, `lab_fork`, `lab_shutdown`. Scenario C6 probes the limits above.

---

## 4. The transaction pool (`core/txpool`)

**Question:** where does a transaction wait between `eth_sendRawTransaction` and a block, and what is it allowed to
do while waiting?

**Entry points**
- `core/txpool/txpool.go`: `TxPool` is a thin coordinator over **sub-pools** (`SubPool` interface in `subpool.go`):
  the legacy pool (normal txs) and the blob pool. `New` (98), `loop` (162, follows chain heads), `Add` (326, routes
  each tx to the sub-pool that accepts its type), `Pending` (374, what the miner asks for), `Content` (436).
- `core/txpool/legacypool/legacypool.go`: the real logic.
  - `Add` (907) -> `addTxsLocked` (954) -> `add` (660): validates (`validateTx` 566, which uses
    `core/txpool/validation.go`), decides replace-or-insert, enforces limits.
  - Two sets per account: **pending** (executable now) and **queued** (nonce gap). `enqueueTx` (821), `promoteTx` (845),
    `promoteExecutables` (1399) moves queued to pending when the gap closes, `demoteUnexecutables` (1535) goes back.
  - Limits: `truncatePending` (1431) and `truncateQueue` (1511) discard when over `GlobalSlots` / `GlobalQueue`;
    per-account `AccountSlots` / `AccountQueue`; price-based eviction.
  - `runReorg` (1203) and `reset` (1299) react to a new head (remove mined txs, re-inject txs of orphaned blocks).
  - Errors: `core/txpool/errors.go` (`ErrUnderpriced`, `ErrReplaceUnderpriced`, `ErrAlreadyKnown`...) and
    `legacypool.go:62 ErrTxPoolOverflow` ("txpool is full").
- `core/txpool/locals/tx_tracker.go`: **local transactions** (those submitted through this node's RPC). It keeps a
  journal file (`transactions.rlp`) and, after a restart, re-submits journaled txs: the first check happens
  **10 seconds after start** (line 213, then every minute, `recheckInterval` line 36). This is why a restarted node
  looks empty for 10 s and then refills (see postmortem C1).

**Key types:** `txpool.PendingFilter`, `txpool.LazyTransaction`, `legacypool.Config`
(flags `--txpool.*`).

**Flow:** `eth_sendRawTransaction -> ethapi SubmitTransaction -> EthAPIBackend.SendTx -> TxPool.Add -> LegacyPool.add`;
the miner later calls `TxPool.Pending` (`miner/worker.go:584 fillTransactions`).

**Java analogy:** a bounded priority queue per account (`TreeMap<nonce, tx>`) plus a global eviction policy; the
`SubPool` interface is a strategy pattern. The "accepted but later evicted" behaviour is like a cache with an
eviction listener that nobody tells the client about.

**Where the lab touches it:** `lab_txpoolStats`, loadgen's `gap` / `replace` scenarios and chaos C3.

---

## 5. Block production in dev mode (`eth/catalyst` and `miner`)

**Question:** who decides to build a block when there is no consensus client?

**Entry points**
- `eth/catalyst/simulated_beacon.go`: `SimulatedBeacon` (91) plays the consensus client.
  `NewSimulatedBeacon` (118) sends the first `forkchoiceUpdated` to trigger the "merge", `Start` (156) starts the
  `loop` (322) when `--dev.period N > 0`, `sealBlock` (175) does one round, `Commit` (363) seals on demand,
  `Fork` (377) sets the head to an ancestor (used by `lab_fork`).
- `sealBlock` is the Engine API conversation done in-process: `forkchoiceUpdated` **with payload attributes**
  (start building) -> `getPayload` (get the built block) -> `newPayload` (import/validate it) -> `forkchoiceUpdated`
  **without** attributes (make it the head). The same four calls a Lighthouse node would make over HTTP.
- `eth/catalyst/simulated_beacon_api.go`: `newSimulatedBeaconAPI` (35) starts `loop` (47) when the period is 0:
  it subscribes to the txpool and seals a block when a transaction arrives. That is the "mine on demand" dev mode.
  It is created inside `RegisterSimulatedBeaconAPIs` (`simulated_beacon.go:407`), which also registers the `dev_` namespace.
- `eth/catalyst/api.go`: the real Engine API (`ConsensusAPI`): `ForkchoiceUpdatedV3` (203) -> `forkchoiceUpdated` (246),
  `GetPayloadV3` (473) -> `getPayload` (530), `NewPayloadV3` (818) -> `newPayload` (898), and `delayPayloadImport` (1064).
- `miner/`: `Miner.BuildPayload` (`miner.go:139`) -> `buildPayload` (`payload_building.go:265`) -> `generateWork`
  (`worker.go:149`) -> `fillTransactions` (`worker.go:584`) which pulls from `TxPool.Pending`, applies each tx with
  `core.ApplyTransaction...` and stops at the block gas limit.

**Flow of one dev block:** txpool event (or timer) -> `sealBlock` -> miner picks txs by effective tip -> EVM executes
them -> block is imported with `BlockChain.InsertChain` path (`newPayload`) -> head moves -> txpool `reset`.

**Java analogy:** `SimulatedBeacon` is a test double (a stub of the consensus-client interface) wired in place of the
real collaborator; the Engine API is the interface.

**Where the lab touches it:** labnode creates the beacon itself; `lab_fork` calls `Fork`; chaos C1 and C5 hinge on how
this module behaves after a rewind or a restart (the wedge in C1 is `delayPayloadImport`, line 1095, saying "Ignoring
payload while snap syncing").

---

## 6. EVM entry (`core/state_transition.go`, `core/vm`)

**Question:** what exactly happens to a transaction when a block is executed?

**Entry points**
- `core/state_processor.go`: `StateProcessor.Process` (68) loops over the block's txs;
  `ApplyTransactionWithEVM` (256) -> `applyTransactionWithEVM` (263) runs one tx and builds the receipt.
- `core/state_transition.go`: `ApplyMessage` (396) creates a `stateTransition`; `preCheck` (573) checks nonce, funds and
  fee caps; `buyGas` (464) debits `gas*price` up front; `execute` (716) charges intrinsic gas, runs the call or contract
  creation, refunds, pays the coinbase tip. `initRuntimeGasBudget` (531) splits the budget into the new
  **execution + state gas** dimensions on recent forks.
- `core/vm/evm.go`: `NewEVM` (148), `EVM.Call` (283), `EVM.create` (550) handle value transfer, precompiles, new
  frames and depth; `core/vm/interpreter.go:96 EVM.Run` is the bytecode loop (decode opcode, charge gas, execute through
  `jump_table.go` / `instructions.go`, call tracing hooks).
- `core/tracing/hooks.go`: `Hooks` struct (286): `OnTxStart`, `OnEnter`/`OnExit` (and the `V2` variants with two-dimensional gas),
  `OnOpcode`/`OnOpcodeV2`, `OnTxEnd`, state-change hooks. The interpreter calls them only when `vm.Config.Tracer != nil`.

**Flow:** `Process -> ApplyMessage -> stateTransition.execute -> EVM.Call/create -> EVM.Run -> opcodes -> StateDB writes`.

**Java analogy:** `Run` is the main loop of a JVM interpreter; `Hooks` is a listener/agent interface (think JVMTI or a
`StepListener`) with nullable callbacks so the hot path pays nothing when no tracer is attached.

**Where the lab touches it:** `internal/gasprofile` is a tracer built on `Hooks`. Observed on the dev chain: a first-time
SSTORE costs 10,100 execution gas **plus 97,920 state gas** (the V2 hooks expose both). That second dimension does not
exist on mainnet-era rules; it is why the counter call costs 131,130 gas instead of about 45,000.

---

## 7. State and trie databases

**Question:** where do balances, nonces, code and storage live, and how do they get from memory to disk safely?

**Layers, top to bottom**
1. `core/state/statedb.go` `StateDB` (`New` 177): per-block working copy, with journaled changes so a failed call can
   revert; `IntermediateRoot` (929) computes the state root; `Commit` (1464) writes the dirty trie nodes to the trie database.
2. `trie/trie.go` (`New` 90): the Merkle-Patricia trie that hashes state; `trie/` also has the proof and iterator code.
3. `triedb/database.go` + a **scheme**: `triedb/hashdb` (nodes keyed by hash, old behaviour) or `triedb/pathdb`
   (nodes keyed by path, default for new databases in this version; log line "State scheme set to already existing scheme=path").
   `triedb/pathdb/database.go`: `New` (147), `Update` (311) adds a *diff layer* in memory per block, `Commit` (340) flushes
   layers to disk, `Journal` (`journal.go:321`) writes the in-memory layers at shutdown ("Persisting dirty state"),
   `Recover` (512) / `Recoverable` (574) roll back using stored state history.
4. `core/rawdb`: schema of keys in the key-value store (headers, bodies, receipts, tx lookup), and the **freezer**
   (`freezer.go:82 NewFreezer`) that moves old blocks to append-only "ancient" files.
5. `ethdb/pebble/pebble.go` (`New` 181): the LSM key-value store (CockroachDB's Pebble). `ethdb/memorydb` is used by
   `--dev` without a datadir.

**The crash story in one paragraph** (postmortem C1): pathdb keeps the latest ~128 block states only in memory
and writes them with `Journal` at a clean shutdown. After `kill -9` the journal is missing, the disk layer is the old
state, so `core/blockchain.go:497/508` logs "Head state missing, repairing" and rewinds the head to the last block whose
state exists (here: genesis). `eth/downloader/syncmode.go:49` then sees "block head = 0 but header/snap head > 0" and
switches to snap sync. With `--gcmode archive --state.scheme hash` every block's state is committed as it is inserted
(`core/blockchain.go:1782`: in archive mode `triedb.Commit(root)` runs inside `writeBlockWithState`, line 1723), so a crash
loses at most the block in flight.

**Java analogy:** `StateDB` is a Hibernate session (first-level cache with dirty tracking); `triedb`/`pathdb` is the
write-behind cache in front of the database; the journal is a shutdown hook that serialises the cache. Skip the hook
(kill -9) and the cache content is gone.

**Where the lab touches it:** chaos C1, C2 and C4 (flag changes against an existing datadir), `docs/postmortems/`.

---

## 8. Sync (`eth/downloader`)

**Question:** how does a node catch up with the network, and why does it matter for a dev node?

**Entry points**
- `eth/downloader/downloader.go`: `New` (249). `eth/downloader/beaconsync.go`: `BeaconSync` (135) is called when the
  consensus client sends a head the node does not have; `skeleton.go` (`skeleton.Sync` 353) first downloads a sparse
  "skeleton" of headers backwards from that head, then the fillers fetch bodies and receipts.
- `eth/downloader/syncmode.go`: `newSyncModer` (38) picks **snap sync** (download the latest state directly, trust the
  chain of headers) or **full sync** (execute every block); `get` (78) re-evaluates; lines 49 to 54 contain the two
  "Switching from full-sync to snap-sync" rules that bite dev nodes after a crash.
- `eth/downloader/beacondevsync.go:36 BeaconDevSync`: a helper for dev setups to sync to a given head.
- `eth/handler.go`, `eth/sync.go`, `eth/protocols/eth`, `eth/protocols/snap`: peer handling and the wire protocols;
  `eth/fetcher` gossip of transactions.
- Ties back to section 5: `eth/catalyst/api.go:1064 delayPayloadImport` refuses payloads while in snap mode.

**Flow (mainnet):** CL says "new head H" -> `forkchoiceUpdated` -> unknown H -> `BeaconSync` -> skeleton -> snap sync of
state at a pivot -> switch to full sync -> import blocks as they come.

**Java analogy:** Kafka consumer catch-up with a compacted snapshot (snap) versus replaying the whole log (full).

**Where the lab touches it:** not exercised on purpose (single node), except through the crash wedge described above.

---

## 9. Metrics

**Question:** what does the node count and how do I get it out?

**Entry points**
- `metrics/`: the registry and instrument types (`Gauge`, `Counter`, `Meter`, `Timer`, `ResettingTimer`, `Histogram`...).
  Instruments are **disabled no-ops unless** metrics are enabled (`metrics.Enabled()`, `metrics/metrics.go:21`; enabling
  happens in `cmd/utils/flags.go:2371 SetupMetrics`).
- Registration happens next to the code that measures, with slash-separated names:
  `core/blockchain.go:63 headBlockGauge = metrics.NewRegisteredGauge("chain/head/block", nil)`.
- Export: `metrics/exp/exp.go:59 Setup(addr)` starts a server with `/debug/metrics` (JSON) and
  `/debug/metrics/prometheus` (`metrics/prometheus/prometheus.go:30 Handler`, `collector.go` converts the registry:
  slashes become underscores, meters and timers become `summary` series with quantile labels, plus `_count`).
- Optional push to InfluxDB (same `SetupMetrics`).

**Java analogy:** Micrometer with a `MeterRegistry` that is a no-op unless actuator is on; metric names `chain/head/block`
are the Micrometer dotted names, Prometheus export rewrites them.

**Where the lab touches it:** `docs/METRICS.md` lists real names from a scrape; `monitoring/` has the Prometheus,
Grafana and alert files; labnode starts `exp.Setup` itself.

---

## 10. Where the lab plugs in

| Lab piece | Geth extension point it uses | Source |
|---|---|---|
| `lab_` JSON-RPC namespace | `rpc.API` + `node.RegisterAPIs`; reflection-based method discovery | `internal/labapi`, `rpc/service.go` |
| `gasProfile` tracer | `tracers.DefaultDirectory.Register` + `tracing.Hooks` | `internal/gasprofile`, `eth/tracers/dir.go:69` |
| labnode | `node.New`, `eth.New`, `catalyst.NewSimulatedBeacon`, `filters.NewFilterAPI` | `cmd/labnode`, `cmd/geth/config.go:226` |
| `lab_fork` | `SimulatedBeacon.Fork` -> `BlockChain.SetCanonical` | `eth/catalyst/simulated_beacon.go:377`, `core/blockchain.go:2901` |
| watch | plain JSON-RPC (`eth_getBlockByNumber`, `eth_getBlockByHash`, `txpool_status`) | `internal/chainwatch`, `cmd/watch` |

Reading order if you have one evening: section 1 (30 min), section 3 (the reflection trick, 30 min), section 5
(`sealBlock`, 30 min), section 4 (`add` and `truncatePending`, 45 min), then the C1 postmortem with section 7 open.
