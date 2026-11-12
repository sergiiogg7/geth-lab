// Package labapi implements the custom "lab_" JSON-RPC namespace.
//
// How a custom namespace works in Geth (this is the whole trick):
//
//   - Any Go struct is an RPC "service". rpc.Server.RegisterName("lab", svc)
//     (rpc/server.go, rpc/service.go) walks the exported methods by reflection
//     and publishes each one as "<namespace>_<methodNameWithLowerFirstLetter>".
//     So the Go method TxpoolStats becomes lab_txpoolStats. Java analogy: it is
//     like Spring MVC / JAX-RS scanning a @RestController, except the "annotation"
//     is simply "exported method with a context-first signature and (result, error)
//     return".
//   - node.Node.RegisterAPIs([]rpc.API{{Namespace: "lab", Service: svc}}) hands
//     the service to the node, which mounts it on every enabled transport
//     (HTTP, WS, IPC) if the namespace is in the allowed module list.
//
// The service needs read access to the chain and the transaction pool, so it
// receives the *eth.Ethereum backend, exactly like Geth's own "txpool" or
// "debug" namespaces do (eth/backend.go APIs()).
package labapi

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/sergiiogg7/geth-lab/internal/gasprofile"
)

// Forker abstracts eth/catalyst.SimulatedBeacon.Fork so the API does not
// depend on the concrete beacon (and tests can fake it).
type Forker interface {
	Fork(parentHash common.Hash) error
}

// Static facts about the process, filled by cmd/labnode.
type Static struct {
	Version  string
	ChainID  uint64
	Datadir  string
	Period   uint64
	Started  time.Time
	PoolCaps map[string]uint64
}

// API is the service object registered under the "lab" namespace.
type API struct {
	eth      *eth.Ethereum
	traces   *tracers.API
	beacon   Forker
	static   Static
	shutdown func()
	stopping atomic.Bool
	readyAt  atomic.Int64 // unix nanos, set by MarkReady
}

// MarkReady records "the node finished starting" (called by labnode after
// stack.Start); lab_nodeInfo reports ready - started as readyInMillis.
func (a *API) MarkReady() { a.readyAt.Store(time.Now().UnixNano()) }

// New wires the service. shutdown must stop the node gracefully.
func New(e *eth.Ethereum, beacon Forker, static Static, shutdown func()) *API {
	return &API{
		eth:      e,
		traces:   tracers.NewAPI(e.APIBackend),
		beacon:   beacon,
		static:   static,
		shutdown: shutdown,
	}
}

// RPCAPI is what node.RegisterAPIs expects.
func (a *API) RPCAPI() rpc.API {
	return rpc.API{Namespace: "lab", Service: a}
}

// ---------------------------------------------------------------- lab_nodeInfo

// NodeInfo is the answer of lab_nodeInfo.
type NodeInfo struct {
	Version       string            `json:"version"`
	ChainID       uint64            `json:"chainId"`
	Datadir       string            `json:"datadir"`
	Persistent    bool              `json:"persistent"`
	DevPeriod     uint64            `json:"devPeriodSeconds"`
	StartedAt     time.Time         `json:"startedAt"`
	UptimeSeconds float64           `json:"uptimeSeconds"`
	ReadyInMillis int64             `json:"readyInMillis"`
	HeadNumber    uint64            `json:"headNumber"`
	HeadHash      common.Hash       `json:"headHash"`
	TxPoolCaps    map[string]uint64 `json:"txpoolCaps"`
}

// NodeInfo reports what the process is and how long it took to come up.
func (a *API) NodeInfo() NodeInfo {
	head := a.eth.BlockChain().CurrentBlock()
	var ready time.Duration
	if r := a.readyAt.Load(); r != 0 {
		ready = time.Unix(0, r).Sub(a.static.Started)
	}
	return NodeInfo{
		Version: a.static.Version, ChainID: a.static.ChainID, Datadir: a.static.Datadir,
		Persistent: a.static.Datadir != "", DevPeriod: a.static.Period,
		StartedAt: a.static.Started, UptimeSeconds: time.Since(a.static.Started).Seconds(),
		ReadyInMillis: ready.Milliseconds(),
		HeadNumber:    head.Number.Uint64(), HeadHash: head.Hash(), TxPoolCaps: a.static.PoolCaps,
	}
}

// -------------------------------------------------------------- lab_txpoolStats

// PoolStats is a richer view than the stock txpool_status (which only returns
// two counters). It is derived from the pool's full content.
type PoolStats struct {
	Pending         int       `json:"pending"`               // executable txs
	Queued          int       `json:"queued"`                // txs waiting for a nonce gap to close
	PendingAccounts int       `json:"pendingAccounts"`       // distinct senders with executable txs
	QueuedAccounts  int       `json:"queuedAccounts"`        // distinct senders with a nonce gap
	PendingBytes    uint64    `json:"pendingBytes"`          // sum of encoded sizes of pending txs
	MinTipWei       string    `json:"minTipWei"`             // lowest gas tip cap among pending
	MaxTipWei       string    `json:"maxTipWei"`             // highest gas tip cap among pending
	LargestAccount  int       `json:"largestAccountPending"` // most pending txs of one sender
	ObservedAt      time.Time `json:"observedAt"`
}

// TxpoolStats summarises the pool.
func (a *API) TxpoolStats() PoolStats {
	pending, queued := a.eth.APIBackend.TxPoolContent()
	s := SummarizePool(pending, queued)
	s.ObservedAt = time.Now()
	return s
}

// ---------------------------------------------------------------- lab_blockFill

// BlockFill describes one block's usage.
type BlockFill struct {
	Number       uint64      `json:"number"`
	Hash         common.Hash `json:"hash"`
	Txs          int         `json:"txs"`
	GasUsed      uint64      `json:"gasUsed"`
	GasLimit     uint64      `json:"gasLimit"`
	FillPercent  float64     `json:"fillPercent"`
	IntervalSecs uint64      `json:"intervalSeconds"` // timestamp delta to the parent
	BaseFeeWei   string      `json:"baseFeeWei"`
}

// BlockFill returns the last `count` blocks (default 10, max 256), newest first.
func (a *API) BlockFill(count *hexutil.Uint64) ([]BlockFill, error) {
	n := uint64(10)
	if count != nil {
		n = uint64(*count)
	}
	if n == 0 || n > 256 {
		return nil, fmt.Errorf("count must be in 1..256, got %d", n)
	}
	chain := a.eth.BlockChain()
	head := chain.CurrentBlock().Number.Uint64()
	out := make([]BlockFill, 0, n)
	for i := uint64(0); i < n && i <= head; i++ {
		num := head - i
		blk := chain.GetBlockByNumber(num)
		if blk == nil {
			break
		}
		var parentTime uint64
		if num > 0 {
			if p := chain.GetHeaderByNumber(num - 1); p != nil {
				parentTime = p.Time
			}
		} else {
			parentTime = blk.Time()
		}
		out = append(out, NewBlockFill(blk, parentTime))
	}
	return out, nil
}

// ---------------------------------------------------------------- lab_gasProfile

// GasProfile re-executes a mined transaction with the native "gasProfile"
// tracer (internal/gasprofile) and returns the per-opcode gas profile. It is a
// convenience wrapper over debug_traceTransaction(hash, {tracer:"gasProfile"}).
func (a *API) GasProfile(ctx context.Context, hash common.Hash) (interface{}, error) {
	name := gasprofile.Name
	return a.traces.TraceTransaction(ctx, hash, &tracers.TraceConfig{Tracer: &name})
}

// ---------------------------------------------------------------- lab_fork

// ForkResult is the answer of lab_fork.
type ForkResult struct {
	OldHead common.Hash `json:"oldHead"`
	OldNum  uint64      `json:"oldNumber"`
	NewHead common.Hash `json:"newHead"`
	NewNum  uint64      `json:"newNumber"`
}

// Fork makes a REAL reorg on the dev chain: it moves the canonical head back
// `depth` blocks (the old blocks stay in the database as a side chain). The
// next blocks the simulated beacon seals are siblings of the old ones (new
// timestamp and prevrandao -> different hashes), so any follower sees
// "head goes down, then the same heights come back with different hashes".
// The transactions of the orphaned blocks go back to the pool and are mined
// again. It refuses when the pool has executable transactions.
func (a *API) Fork(depth hexutil.Uint64) (*ForkResult, error) {
	if a.beacon == nil {
		return nil, errors.New("no simulated beacon: lab_fork only works in dev mode")
	}
	chain := a.eth.BlockChain()
	old := chain.CurrentBlock()
	if uint64(depth) == 0 || uint64(depth) > old.Number.Uint64() {
		return nil, fmt.Errorf("depth must be in 1..%d", old.Number.Uint64())
	}
	target := chain.GetBlockByNumber(old.Number.Uint64() - uint64(depth))
	if target == nil {
		return nil, errors.New("target block not found")
	}
	if err := a.beacon.Fork(target.Hash()); err != nil {
		return nil, err
	}
	cur := chain.CurrentBlock()
	return &ForkResult{OldHead: old.Hash(), OldNum: old.Number.Uint64(), NewHead: cur.Hash(), NewNum: cur.Number.Uint64()}, nil
}

// ---------------------------------------------------------------- lab_shutdown

// Shutdown stops the node gracefully (flushes the database, closes the RPC
// server). It is the portable replacement for Ctrl-C on Windows, where a
// background process cannot receive a console signal.
func (a *API) Shutdown() bool {
	if !a.stopping.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		time.Sleep(100 * time.Millisecond) // let the RPC reply go out first
		a.shutdown()
	}()
	return true
}

// ------------------------------------------------------------------ pure helpers

// SummarizePool is split out from TxpoolStats so it can be unit tested without
// a node.
func SummarizePool(pending, queued map[common.Address][]*types.Transaction) PoolStats {
	var s PoolStats
	s.PendingAccounts, s.QueuedAccounts = len(pending), len(queued)
	var minTip, maxTip *big.Int
	for _, txs := range pending {
		s.Pending += len(txs)
		if len(txs) > s.LargestAccount {
			s.LargestAccount = len(txs)
		}
		for _, tx := range txs {
			s.PendingBytes += tx.Size()
			tip := tx.GasTipCap()
			if minTip == nil || tip.Cmp(minTip) < 0 {
				minTip = tip
			}
			if maxTip == nil || tip.Cmp(maxTip) > 0 {
				maxTip = tip
			}
		}
	}
	for _, txs := range queued {
		s.Queued += len(txs)
	}
	s.MinTipWei, s.MaxTipWei = "0", "0"
	if minTip != nil {
		s.MinTipWei, s.MaxTipWei = minTip.String(), maxTip.String()
	}
	return s
}

// NewBlockFill converts a block to its fill summary.
func NewBlockFill(b *types.Block, parentTime uint64) BlockFill {
	f := BlockFill{
		Number: b.NumberU64(), Hash: b.Hash(), Txs: len(b.Transactions()),
		GasUsed: b.GasUsed(), GasLimit: b.GasLimit(), BaseFeeWei: "0",
	}
	if f.GasLimit > 0 {
		f.FillPercent = float64(f.GasUsed) * 100 / float64(f.GasLimit)
	}
	if b.Time() >= parentTime {
		f.IntervalSecs = b.Time() - parentTime
	}
	if bf := b.BaseFee(); bf != nil {
		f.BaseFeeWei = bf.String()
	}
	return f
}
