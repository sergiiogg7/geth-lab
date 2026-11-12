# Node operations: flags, startup sequence, smoke checks

All flags below were checked with `geth --help` of **v1.17.10** and exercised by `scripts/start-node.sh` and the chaos
scenarios. Defaults are the ones printed by that help text.

## Why a single `--dev` node

Geth no longer supports multi-node Clique / proof-of-authority private networks (deprecated in 1.14). A multi-node
network now needs a consensus client per execution client, normally orchestrated with Kurtosis, which needs Docker and
does not fit the 8 GB lab machine. So the lab runs **one** `geth --dev` node: a post-Merge chain with a built-in fake
consensus client (the *simulated beacon*) that seals blocks, a prefunded account and no networking. It is enough to
operate a real execution client, send real transactions, read real metrics and break it on purpose.

## The three ways to run a node

| Script | What runs | Chain database | When to use |
|---|---|---|---|
| `scripts/start-node.sh` | stock `geth --dev` | memory (lost on stop) | quick experiments |
| `scripts/start-node.sh --persist` | stock `geth --dev --datadir .lab/datadir` | disk, survives restarts | restart / crash scenarios |
| `scripts/start-node.sh --client labnode [--persist]` | `bin/labnode` (Geth embedded as a library + `lab_` namespace + `gasProfile` tracer) | same as above | the lab's own extensions |

Options: `--period N` (block every N seconds; default 0 = one block per incoming transaction), `-- <flags>` passes extra
flags to the node. `scripts/stop-node.sh` is graceful (Ctrl-C equivalent through `bin/nodectl`), `scripts/stop-node.sh --kill`
is `kill -9` / `TerminateProcess`. `scripts/reset.sh` kills the node and deletes `.lab/`. `scripts/smoke.sh` runs the
JSON-RPC checks with `cast`.

## What the flags mean

Flags used by `scripts/start-node.sh`:

| Flag | Meaning |
|---|---|
| `--dev` | Dev mode: chain id/network id 1337, a post-Merge genesis with every fork active from block 0 (the printed fork list ends with Osaka and Bogota), simulated beacon, prefunded developer account `0x71562b71999873DB5b286dF957af199Ec94617F7` (key printed in the startup banner; **test key**), miner tip floor 1 wei, no p2p (`MaxPeers=0`, no discovery, no listener). See `cmd/utils/flags.go` 2016 to 2097 and `cmd/geth/config.go` 299 to 311. |
| `--dev.period N` | Seconds between sealed blocks. `0` (default) = seal when a transaction or withdrawal arrives (`eth/catalyst/simulated_beacon_api.go:47`). Blocks are sealed even when empty if N > 0. |
| `--dev.gaslimit` | Gas limit of the dev genesis (default 11,500,000; the miner then moves it by 1/1024 per block toward `--miner.gaslimit`, default 60,000,000, which is why the dev gas limit slowly grows). |
| `--datadir D` | Persistent data directory. Without it, `--dev` uses in-memory databases. Contains `geth/chaindata` (Pebble database + `ancient/` freezer), `geth/triedb` (path-scheme journal), `geth/transactions.rlp` (local tx journal), `keystore/`. |
| `--http --http.addr 127.0.0.1 --http.port 8545 --http.api eth,net,web3,txpool,debug,admin` | JSON-RPC over HTTP, loopback only, with the listed namespaces. `debug` and `admin` are powerful (rewind the chain, add peers): fine for a lab, never exposed in production. Default `--http.vhosts localhost` rejects other Host headers. |
| `--ipcdisable` | No named pipe (Windows) / unix socket. |
| `--metrics --metrics.addr 127.0.0.1 --metrics.port 6060` | Enable metric collection and serve it on a separate port: `/debug/metrics` (JSON) and `/debug/metrics/prometheus`. Collection is off by default and costs nothing when off. |
| `--verbosity N` | 0 silent .. 5 trace; default 3. |

Flags the lab changes in specific scenarios (all exist in 1.17.10):

| Flag | Default | Scenario |
|---|---|---|
| `--txpool.globalslots / globalqueue / accountslots / accountqueue` | 5120 / 1024 / 16 / 64 | C3 shrinks them to 64 / 64 / 16 / 16 |
| `--txpool.pricelimit`, `--txpool.pricebump` | 1, 10 (percent) | C3 observes the 10% replacement rule |
| `--txpool.journal`, `--txpool.rejournal`, `--txpool.nolocals`, `--txpool.lifetime` | `transactions.rlp`, 1h, false, 3h | C1 (the journal brings txs back) |
| `--cache`, `--cache.database/trie/gc/snapshot` | 4096 MB; 50 / 15 / 25 / 10 % | C4 (`--cache 128`, `2048`) |
| `--gcmode` | `full` | C1, C4: `archive` keeps every state |
| `--state.scheme` | none (new DB: `path`; existing DB: whatever it was created with) | C1, C4: `hash` |
| `--rpc.batch-request-limit`, `--rpc.batch-response-max-size`, `--rpc.http-body-limit` | 1000, 25,000,000 bytes, 5 MB | C6 |
| `--rpc.gascap`, `--rpc.evmtimeout` | 50,000,000, 5s | C6 (an infinite loop in `eth_call` ends in "out of gas" after about 60 ms, the gas cap bites first) |
| `--ipcpath`, `--nodiscover`, `--maxpeers`, `--syncmode`, `--networkid` | | irrelevant or forced in `--dev` |

Note: with `--cache 4096` on this 8 GB machine Geth sanitises the value ("Sanitizing cache to Go's GC limits
provided=4096 updated=2691") and then allocates trie caches of 403 MB clean + 672 MB dirty (log line "Allocated trie memory
caches"). The process stays small until the caches fill (process memory was 18 to 30 MB in the C4 runs), but on a tight
machine pass `--cache 256`.

## What the node does on startup (real log of v1.17.10, `--dev --datadir ...`)

In order, as printed by the node:

1. `Starting Geth on Ethereum mainnet...` (the banner text is misleading in dev mode; the chain is 1337).
2. `Maximum peer count`, `Sanitizing cache to Go's GC limits`, `Set global gas cap`, `Engine API maximum reorg depth depth=32`.
3. `Using developer account address=0x7156...` (imports the dev key into `datadir/keystore` on first start).
4. `Using pebble as the backing database`, `Allocated cache and file handles`, `Opened ancient database` (the freezer), `Opened Era store`.
5. `Initializing the KZG library backend=gokzg` (blob transactions).
6. `Allocated trie memory caches`, `State scheme set to already existing scheme=path` (or `set by user` when you pass the flag).
7. `Initialising Ethereum protocol network=1337 dbversion=9`, then pathdb: `Load database journal from disk`, `Initialized path database`.
8. The chain config banner (every pre-merge fork at #0, then Shanghai, Cancun, Prague, Osaka, Bogota at timestamp 0).
9. `Loaded most recent local block number=N`. **After an unclean shutdown this is where `Head state missing, repairing`
   appears** (postmortem C1). Also `Initialized transaction indexer`, `Unclean shutdown detected` (a marker from the last run).
10. `Registered sync override service`, the `--dev` warning banner, `Starting peer-to-peer node`, `HTTP server started`.
11. `Loaded local transaction journal transactions=N`. The transactions are only re-submitted to the pool ~10 s later
    (`core/txpool/locals/tx_tracker.go:213`).
12. `Started log indexer`: the node answers `eth_getTransactionReceipt` with `-32000 transaction indexing is in progress`
    until the transaction indexer has caught up after a restart. `scripts/smoke.sh` polls the receipt for this reason.

Time to a first answer from `eth_blockNumber` was 0.25 to 0.6 s on this machine for every start in the chaos runs.

## JSON-RPC smoke checks

`scripts/smoke.sh` (needs a running node) uses `cast`:

```
web3_clientVersion, eth_chainId (= 1337), eth_blockNumber, eth_gasPrice, net_version, txpool_status,
eth_getBalance(dev account), a 1 wei transfer with `cast send --async` + receipt polling,
balance of the recipient grows; on labnode also lab_nodeInfo and lab_blockFill.
```

Manual equivalents: `cast block-number`, `cast rpc txpool_status`, `cast send <to> --value 1 --private-key $DEV_KEY`.
