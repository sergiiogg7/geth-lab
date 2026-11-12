// Command labnode is Geth embedded as a LIBRARY, plus the lab's extensions.
//
// What cmd/geth does with ~300 lines of CLI plumbing, this file does in
// ~150: build a node.Node (the service container), attach an eth.Ethereum
// (the execution layer), attach a SimulatedBeacon (the fake consensus client
// of --dev mode), and mount extra things on top:
//
//   - a custom JSON-RPC namespace "lab_" (internal/labapi)
//   - a native EVM tracer "gasProfile" (internal/gasprofile, registered by its
//     init() through the blank import below)
//
// It understands the same flag names as `geth --dev` for the options the lab
// uses (see internal/nodeproc.DevOpts), so scripts and chaos tests can start
// either binary with the same arguments. It is NOT a replacement for geth:
// no p2p, no console, no account management, no mainnet.
//
// Java analogy: geth is a Spring Boot fat jar with a main(); this is a tiny
// main() that builds the same ApplicationContext by hand and registers one
// more @RestController.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/catalyst"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/eth/filters"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/metrics/exp"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/sergiiogg7/geth-lab/internal/devnet"
	_ "github.com/sergiiogg7/geth-lab/internal/gasprofile" // registers the "gasProfile" tracer
	"github.com/sergiiogg7/geth-lab/internal/labapi"
)

const version = "labnode/0.1.0 (go-ethereum v1.17.10)"

func main() {
	var (
		datadir     = flag.String("datadir", "", "data directory; empty = in-memory chain")
		_           = flag.Bool("http", true, "accepted for compatibility; HTTP-RPC is always on")
		httpAddr    = flag.String("http.addr", "127.0.0.1", "HTTP-RPC listen address")
		httpPort    = flag.Int("http.port", 8545, "HTTP-RPC port")
		_           = flag.String("http.api", "", "accepted for compatibility with geth; labnode always serves eth,net,web3,txpool,debug,admin,lab,dev")
		_           = flag.Bool("ipcdisable", true, "accepted for compatibility; IPC is always off")
		_           = flag.Bool("dev", true, "accepted for compatibility; labnode is always in dev mode")
		devPeriod   = flag.Uint64("dev.period", 0, "seal a block every N seconds; 0 = seal when a transaction arrives")
		devGasLimit = flag.Uint64("dev.gaslimit", 11_500_000, "block gas limit of the dev genesis")
		metricsOn   = flag.Bool("metrics", false, "serve Prometheus metrics")
		metricsAddr = flag.String("metrics.addr", "127.0.0.1", "metrics listen address")
		metricsPort = flag.Int("metrics.port", 6060, "metrics port")
		cacheMB     = flag.Int("cache", 0, "total cache in MB (split 50/15/25/10 like geth); 0 = geth defaults")
		gcmode      = flag.String("gcmode", "", "full|archive; empty = default (full)")
		scheme      = flag.String("state.scheme", "", "path|hash; empty = default")
		verbosity   = flag.Int("verbosity", 3, "log level 1(error)..5(trace)")
		poolGlobalS = flag.Uint64("txpool.globalslots", 0, "max executable txs in the pool (0 = geth default 5120)")
		poolGlobalQ = flag.Uint64("txpool.globalqueue", 0, "max non-executable txs (0 = default 1024)")
		poolAccS    = flag.Uint64("txpool.accountslots", 0, "guaranteed executable slots per account (0 = default 16)")
		poolAccQ    = flag.Uint64("txpool.accountqueue", 0, "max non-executable txs per account (0 = default 64)")
		poolPrice   = flag.Uint64("txpool.pricelimit", 0, "minimum gas tip to accept a tx (0 = default 1)")
		poolBump    = flag.Uint64("txpool.pricebump", 0, "percent bump needed to replace a tx (0 = default 10)")
	)
	flag.Parse()

	// Logging: same terminal format as geth.
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, slog.Level(4-*verbosity), false)))
	t0 := time.Now()

	// --- 1. The service container -------------------------------------------------
	// node.Node owns the datadir lock, the RPC servers, the lifecycle list
	// (Start/Stop of every registered service) and the account manager.
	nodeCfg := node.DefaultConfig
	nodeCfg.Name = "labnode"
	nodeCfg.Version = version
	nodeCfg.DataDir = *datadir
	nodeCfg.IPCPath = "" // no named pipe / unix socket
	nodeCfg.HTTPHost, nodeCfg.HTTPPort = *httpAddr, *httpPort
	nodeCfg.HTTPModules = []string{"eth", "net", "web3", "txpool", "debug", "admin", "lab", "dev", "rpc"}
	nodeCfg.HTTPVirtualHosts = []string{"localhost", "127.0.0.1"}
	nodeCfg.UseLightweightKDF = true
	// Same p2p switches that `geth --dev` forces (cmd/utils/flags.go SetP2PConfig).
	nodeCfg.P2P.MaxPeers = 0
	nodeCfg.P2P.ListenAddr = ""
	nodeCfg.P2P.NoDial = true
	nodeCfg.P2P.NoDiscovery = true
	nodeCfg.P2P.DiscoveryV5 = false

	if *metricsOn {
		metrics.Enable()
		exp.Setup(fmt.Sprintf("%s:%d", *metricsAddr, *metricsPort))
	}

	stack, err := node.New(&nodeCfg)
	if err != nil {
		fatal("create node", err)
	}

	// --- 2. The execution layer ---------------------------------------------------
	dev := devnet.DevAddress()
	ecfg := ethconfig.Defaults
	ecfg.NetworkId = devnet.ChainID
	ecfg.SyncMode = ethconfig.FullSync
	ecfg.EnablePreimageRecording = true
	ecfg.Genesis = core.DeveloperGenesisBlock(*devGasLimit, &dev)
	ecfg.Miner.PendingFeeRecipient = dev
	ecfg.Miner.GasPrice = big.NewInt(1)
	if *cacheMB > 0 {
		ecfg.DatabaseCache = *cacheMB * 50 / 100
		ecfg.TrieCleanCache = *cacheMB * 15 / 100
		ecfg.TrieDirtyCache = *cacheMB * 25 / 100
		ecfg.SnapshotCache = *cacheMB * 10 / 100
	}
	switch *gcmode {
	case "":
	case "full":
	case "archive":
		ecfg.NoPruning = true
	default:
		fatal("gcmode", fmt.Errorf("must be full or archive, got %q", *gcmode))
	}
	if *scheme != "" {
		ecfg.StateScheme = *scheme
	}
	set := func(dst *uint64, v uint64) {
		if v != 0 {
			*dst = v
		}
	}
	set(&ecfg.TxPool.GlobalSlots, *poolGlobalS)
	set(&ecfg.TxPool.GlobalQueue, *poolGlobalQ)
	set(&ecfg.TxPool.AccountSlots, *poolAccS)
	set(&ecfg.TxPool.AccountQueue, *poolAccQ)
	set(&ecfg.TxPool.PriceLimit, *poolPrice)
	set(&ecfg.TxPool.PriceBump, *poolBump)

	backend, err := eth.New(stack, &ecfg)
	if err != nil {
		fatal("create ethereum backend", err)
	}
	// debug_* tracing namespace: the stock registration (cmd/utils RegisterEthService).
	stack.RegisterAPIs(tracers.APIs(backend.APIBackend))
	// Log-filter API (eth_getLogs, eth_newFilter, eth_subscribe): same as cmd/utils RegisterFilterAPI.
	filterSystem := filters.NewFilterSystem(backend.APIBackend, filters.Config{
		LogCacheSize: ecfg.FilterLogCacheSize, LogQueryLimit: ecfg.LogQueryLimit, RangeLimit: ecfg.RangeLimit,
	})
	stack.RegisterAPIs([]rpc.API{{Namespace: "eth", Service: filters.NewFilterAPI(filterSystem)}})

	// --- 3. The fake consensus client ---------------------------------------------
	beacon, err := catalyst.NewSimulatedBeacon(*devPeriod, dev, backend)
	if err != nil {
		fatal("create simulated beacon", err)
	}
	catalyst.RegisterSimulatedBeaconAPIs(stack, beacon)
	stack.RegisterLifecycle(beacon)

	// --- 4. The lab extension -----------------------------------------------------
	api := labapi.New(backend, beacon, labapi.Static{
		Version: version, ChainID: devnet.ChainID, Datadir: *datadir, Period: *devPeriod,
		Started: t0,
		PoolCaps: map[string]uint64{
			"globalSlots": ecfg.TxPool.GlobalSlots, "globalQueue": ecfg.TxPool.GlobalQueue,
			"accountSlots": ecfg.TxPool.AccountSlots, "accountQueue": ecfg.TxPool.AccountQueue,
		},
	}, func() { stack.Close() })
	stack.RegisterAPIs([]rpc.API{api.RPCAPI()})

	// --- 5. Run -------------------------------------------------------------------
	if err := stack.Start(); err != nil {
		fatal("start node", err)
	}
	api.MarkReady()
	log.Info("labnode ready", "http", fmt.Sprintf("http://%s:%d", *httpAddr, *httpPort),
		"datadir", *datadir, "startup", time.Since(t0).Round(time.Millisecond), "devAccount", dev)

	// Graceful stop on Ctrl-C / SIGTERM, or when lab_shutdown closes the stack.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		log.Info("Got interrupt, shutting down...")
		stack.Close()
	}()
	stack.Wait() // blocks until stack.Close() has finished
	log.Info("labnode stopped")
}

func fatal(what string, err error) {
	fmt.Fprintf(os.Stderr, "labnode: %s: %v\n", what, err)
	os.Exit(1)
}
