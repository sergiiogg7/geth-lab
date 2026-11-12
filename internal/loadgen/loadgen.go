// Package loadgen floods a dev node with signed transactions and measures what
// happens: how many are accepted, why the others are rejected, how long
// inclusion takes, how the txpool behaves.
//
// Concurrency model (Java analogy in brackets):
//
//   - one goroutine per sender [a worker thread per account] signs and sends
//     transactions with a locally tracked nonce, so a sender never needs a
//     round trip to learn its next nonce;
//   - one collector goroutine [a scheduled poller] watches new blocks and
//     matches their transactions with the send timestamps;
//   - one sampler goroutine polls txpool_status;
//   - shared maps are protected by a sync.Mutex [synchronized], counters by
//     atomics [AtomicLong].
package loadgen

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/sergiiogg7/geth-lab/internal/devnet"
)

// Config describes one run.
type Config struct {
	RPC       string
	Senders   int            // number of distinct sender accounts
	Txs       int            // total transactions to send (0 = run for Duration)
	Duration  time.Duration  // used when Txs == 0
	Rate      float64        // total tx/s across all senders; 0 = as fast as possible
	Mode      string         // "transfer" or "contract" (calls the counter contract: 1 SSTORE per tx)
	DataBytes int            // calldata bytes per tx
	Scenario  string         // "steady", "gap" (never-filled nonce gaps) or "replace" (same-nonce replacements)
	GapEvery  int            // gap scenario: skip a nonce every N-th tx of a sender
	TipGwei   float64        // priority fee
	Seed      string         // sender key derivation seed
	Drain     time.Duration  // how long to wait for inclusion after the last send
	OnSent    func(SentInfo) // optional: called for every transaction the node accepted
	Log       func(string, ...any)
}

// Defaults fills zero fields.
func (c *Config) Defaults() {
	if c.RPC == "" {
		c.RPC = "http://127.0.0.1:8545"
	}
	if c.Senders <= 0 {
		c.Senders = 8
	}
	if c.Txs == 0 && c.Duration == 0 {
		c.Txs = 1000
	}
	if c.Mode == "" {
		c.Mode = "transfer"
	}
	if c.Scenario == "" {
		c.Scenario = "steady"
	}
	if c.GapEvery <= 0 {
		c.GapEvery = 5
	}
	if c.TipGwei == 0 {
		c.TipGwei = 1
	}
	if c.Seed == "" {
		c.Seed = "loadgen"
	}
	if c.Drain == 0 {
		c.Drain = 20 * time.Second
	}
	if c.Log == nil {
		c.Log = func(string, ...any) {}
	}
}

// Report is the outcome of a run (JSON friendly).
type Report struct {
	Config struct {
		Senders   int     `json:"senders"`
		Mode      string  `json:"mode"`
		Scenario  string  `json:"scenario"`
		DataBytes int     `json:"dataBytes"`
		Rate      float64 `json:"targetRate"`
		Gas       uint64  `json:"gasPerTx"`
	} `json:"config"`

	Attempted int            `json:"attempted"`
	Accepted  int            `json:"accepted"`
	Included  int            `json:"included"`
	Errors    map[string]int `json:"errors"`

	SendSeconds    float64     `json:"sendSeconds"`
	TotalSeconds   float64     `json:"totalSeconds"`
	AcceptedPerSec float64     `json:"acceptedPerSec"`
	IncludedPerSec float64     `json:"includedPerSec"`
	LatencyMillis  Percentiles `json:"inclusionLatencyMillis"`
	Blocks         int         `json:"blocksObserved"`
	AvgGasFillPct  float64     `json:"avgGasFillPercent"`
	MaxTxsPerBlock int         `json:"maxTxsPerBlock"`
	MaxPending     uint64      `json:"txpoolMaxPending"`
	MaxQueued      uint64      `json:"txpoolMaxQueued"`
	FinalPending   uint64      `json:"txpoolFinalPending"`
	FinalQueued    uint64      `json:"txpoolFinalQueued"`
	NotIncluded    int         `json:"acceptedButNotIncluded"`
}

// SentInfo describes one accepted transaction (see Config.OnSent).
type SentInfo struct {
	Hash  common.Hash
	From  common.Address
	Nonce uint64
}

// Percentiles of a latency sample in milliseconds.
type Percentiles struct {
	N                       int                `json:"n"`
	Min, P50, P90, P99, Max float64            `json:"-"`
	Values                  map[string]float64 `json:"values"`
}

// Compute fills the percentiles from raw samples (nearest-rank method).
func Compute(samples []time.Duration) Percentiles {
	p := Percentiles{N: len(samples), Values: map[string]float64{}}
	if len(samples) == 0 {
		return p
	}
	s := append([]time.Duration(nil), samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := func(q float64) float64 {
		idx := int(q*float64(len(s))+0.999999) - 1 // ceil(q*n)-1
		idx = max(0, min(len(s)-1, idx))
		return float64(s[idx].Microseconds()) / 1000
	}
	p.Min, p.P50, p.P90, p.P99 = rank(0), rank(0.50), rank(0.90), rank(0.99)
	p.Max = float64(s[len(s)-1].Microseconds()) / 1000
	p.Values = map[string]float64{"min": p.Min, "p50": p.P50, "p90": p.P90, "p99": p.P99, "max": p.Max}
	return p
}

// ClassifyError maps a node error message to a short, stable bucket name.
// The strings come from core/txpool/errors.go, core/error.go and
// core/txpool/legacypool/legacypool.go (v1.17.10).
func ClassifyError(err error) string {
	m := strings.ToLower(err.Error())
	switch {
	case strings.Contains(m, "replacement transaction underpriced"):
		return "replacement-underpriced"
	case strings.Contains(m, "transaction underpriced"):
		return "underpriced"
	case strings.Contains(m, "txpool is full"):
		return "txpool-full"
	case strings.Contains(m, "out of capacity"):
		return "out-of-capacity"
	case strings.Contains(m, "account limit exceeded"):
		return "account-limit"
	case strings.Contains(m, "future transaction tries to replace pending"):
		return "future-replaces-pending"
	case strings.Contains(m, "nonce too low"):
		return "nonce-too-low"
	case strings.Contains(m, "already known"):
		return "already-known"
	case strings.Contains(m, "insufficient funds"):
		return "insufficient-funds"
	case strings.Contains(m, "exceeds block gas limit"):
		return "exceeds-block-gas-limit"
	case strings.Contains(m, "connection refused"), strings.Contains(m, "eof"), strings.Contains(m, "forcibly closed"),
		strings.Contains(m, "connectex"), strings.Contains(m, "context deadline"), strings.Contains(m, "timeout"):
		return "connection"
	}
	return "other: " + truncate(err.Error(), 80)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// sentTx remembers when a transaction was accepted by the node.
type sentTx struct {
	at       time.Time
	included time.Time
}

type runner struct {
	cfg    Config
	client *ethclient.Client
	rpc    *rpc.Client

	keys   []*ecdsa.PrivateKey
	target common.Address
	gas    uint64
	feeCap atomic.Pointer[big.Int] // refreshed from the latest base fee

	mu   sync.Mutex
	sent map[common.Hash]*sentTx

	attempted, accepted atomic.Int64
	errMu               sync.Mutex
	errs                map[string]int

	maxPending, maxQueued atomic.Uint64
}

// Run executes the whole flow: fund, send, observe, report.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	cfg.Defaults()
	rc, err := rpc.DialContext(ctx, cfg.RPC)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	r := &runner{cfg: cfg, rpc: rc, client: ethclient.NewClient(rc), sent: map[common.Hash]*sentTx{}, errs: map[string]int{}}
	return r.run(ctx)
}

func (r *runner) run(ctx context.Context) (*Report, error) {
	cfg := r.cfg
	id, err := r.client.ChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot reach node: %w", err)
	}
	if id.Uint64() != devnet.ChainID {
		return nil, fmt.Errorf("chain id %s, the lab only works on the dev chain (%d)", id, devnet.ChainID)
	}
	for i := 0; i < cfg.Senders; i++ {
		r.keys = append(r.keys, devnet.SenderKey(cfg.Seed, i))
	}
	if err := r.setup(ctx); err != nil {
		return nil, err
	}

	// --- observation goroutines ---
	obsCtx, stopObs := context.WithCancel(ctx)
	defer stopObs()
	var obsWG sync.WaitGroup
	col := &collector{r: r}
	obsWG.Add(2)
	go func() { defer obsWG.Done(); col.run(obsCtx) }()
	go func() { defer obsWG.Done(); r.samplePool(obsCtx) }()
	go r.refreshFees(obsCtx)

	// --- send phase ---
	start := time.Now()
	r.sendAll(ctx)
	sendDur := time.Since(start)

	// --- drain: wait until everything accepted is included, or give up ---
	deadline := time.Now().Add(cfg.Drain)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if col.pendingCount() == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	total := time.Since(start)
	stopObs()
	obsWG.Wait()

	return r.report(col, sendDur, total), nil
}

// setup funds the senders and prepares the transaction template.
func (r *runner) setup(ctx context.Context) error {
	cfg := r.cfg
	dev := devnet.DevKey()
	devAddr := devnet.DevAddress()
	tip := gwei(cfg.TipGwei)
	// Fund senders that are below 0.1 ETH.
	nonce, err := r.client.PendingNonceAt(ctx, devAddr)
	if err != nil {
		return err
	}
	var fundHashes []common.Hash
	oneETH := new(big.Int).Mul(big.NewInt(1e9), big.NewInt(1e9))
	tipCap, feeCap, err := devnet.SuggestFees(ctx, r.client, new(big.Int).Mul(tip, big.NewInt(2)))
	if err != nil {
		return err
	}
	for _, k := range r.keys {
		addr := crypto.PubkeyToAddress(k.PublicKey)
		bal, err := r.client.BalanceAt(ctx, addr, nil)
		if err != nil {
			return err
		}
		if bal.Cmp(new(big.Int).Div(oneETH, big.NewInt(10))) >= 0 {
			continue
		}
		tx, err := devnet.SignDynamic(dev, nonce, &addr, oneETH, 21000, tipCap, feeCap, nil)
		if err != nil {
			return err
		}
		if err := r.client.SendTransaction(ctx, tx); err != nil {
			return fmt.Errorf("funding %s: %w", addr, err)
		}
		fundHashes = append(fundHashes, tx.Hash())
		nonce++
	}
	// Contract mode: deploy the counter.
	var deployHash common.Hash
	if cfg.Mode == "contract" {
		tx, err := devnet.SignDynamic(dev, nonce, nil, new(big.Int), 200_000, tipCap, feeCap, devnet.CounterInitCode)
		if err != nil {
			return err
		}
		if err := r.client.SendTransaction(ctx, tx); err != nil {
			return fmt.Errorf("deploy: %w", err)
		}
		deployHash = tx.Hash()
		r.target = crypto.CreateAddress(devAddr, nonce)
	} else {
		r.target = common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	}
	cfg.Log("funding %d senders (waiting for receipts)...", len(fundHashes))
	wctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for _, h := range append(fundHashes, deployHash) {
		if h == (common.Hash{}) {
			continue
		}
		if _, err := devnet.WaitReceipt(wctx, r.client, h); err != nil {
			return fmt.Errorf("waiting for setup tx %s: %w", h, err)
		}
	}
	// Gas per tx: ask the node once, then add 10% margin.
	msg := ethereum.CallMsg{From: crypto.PubkeyToAddress(r.keys[0].PublicKey), To: &r.target, Data: r.calldata(0)}
	gas, err := r.client.EstimateGas(ctx, msg)
	if err != nil {
		return fmt.Errorf("estimate gas: %w", err)
	}
	r.gas = gas + gas/10
	r.feeCap.Store(feeCap)
	return nil
}

// calldata builds DataBytes of non-zero bytes (worst case: 16 gas per byte).
func (r *runner) calldata(seq int) []byte {
	if r.cfg.DataBytes <= 0 {
		return nil
	}
	d := make([]byte, r.cfg.DataBytes)
	for i := range d {
		d[i] = byte((i+seq)%255 + 1)
	}
	return d
}

// refreshFees keeps feeCap = 2*baseFee + 2*tip fresh as the base fee moves.
func (r *runner) refreshFees(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, fc, err := devnet.SuggestFees(ctx, r.client, new(big.Int).Mul(gwei(r.cfg.TipGwei), big.NewInt(2))); err == nil {
				r.feeCap.Store(fc)
			}
		}
	}
}

// samplePool tracks the maximum txpool occupancy seen during the run.
func (r *runner) samplePool(ctx context.Context) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var st map[string]hexutil.Uint64
			cctx, cancel := context.WithTimeout(ctx, time.Second)
			err := r.rpc.CallContext(cctx, &st, "txpool_status")
			cancel()
			if err != nil {
				continue
			}
			storeMax(&r.maxPending, uint64(st["pending"]))
			storeMax(&r.maxQueued, uint64(st["queued"]))
		}
	}
}

func storeMax(a *atomic.Uint64, v uint64) {
	for {
		old := a.Load()
		if v <= old || a.CompareAndSwap(old, v) {
			return
		}
	}
}

func gwei(f float64) *big.Int {
	v, _ := new(big.Float).Mul(big.NewFloat(f), big.NewFloat(1e9)).Int(nil)
	return v
}

// sendAll starts one goroutine per sender and waits for them.
func (r *runner) sendAll(ctx context.Context) {
	cfg := r.cfg
	var tokens <-chan struct{}
	if cfg.Rate > 0 {
		ch := make(chan struct{}, 1024)
		tokens = ch
		go func() {
			iv := time.Duration(float64(time.Second) / cfg.Rate)
			t := time.NewTicker(iv)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			}
		}()
	}
	var budget atomic.Int64 // remaining tx budget; -1 = time based
	budget.Store(int64(cfg.Txs))
	var stopAt time.Time
	if cfg.Txs == 0 {
		budget.Store(-1)
		stopAt = time.Now().Add(cfg.Duration)
	}
	var wg sync.WaitGroup
	for i, k := range r.keys {
		wg.Add(1)
		go func(i int, k *ecdsa.PrivateKey) {
			defer wg.Done()
			r.sender(ctx, k, tokens, &budget, stopAt)
		}(i, k)
	}
	wg.Wait()
}

func (r *runner) sender(ctx context.Context, key *ecdsa.PrivateKey, tokens <-chan struct{}, budget *atomic.Int64, stopAt time.Time) {
	addr := crypto.PubkeyToAddress(key.PublicKey)
	nonce, err := r.client.PendingNonceAt(ctx, addr)
	if err != nil {
		r.recordErr(err)
		return
	}
	tip := gwei(r.cfg.TipGwei)
	for seq := 0; ctx.Err() == nil; seq++ {
		// Budget / deadline.
		if b := budget.Load(); b >= 0 {
			if budget.Add(-1) < 0 {
				return
			}
		} else if time.Now().After(stopAt) {
			return
		}
		if tokens != nil {
			select {
			case <-tokens:
			case <-ctx.Done():
				return
			}
		}
		switch r.cfg.Scenario {
		case "gap":
			// Every GapEvery-th tx skips one nonce; the skipped nonce is never sent,
			// so everything after it is stuck in the queued (non-executable) set.
			if seq > 0 && seq%r.cfg.GapEvery == 0 {
				nonce++
			}
			r.sendOne(ctx, key, nonce, tip, r.feeCap.Load(), seq)
			nonce++
		case "replace":
			fc := r.feeCap.Load()
			r.sendOne(ctx, key, nonce, tip, fc, seq)     // original
			r.sendOne(ctx, key, nonce, tip, fc, seq)     // identical -> already known
			same := new(big.Int).Add(tip, big.NewInt(1)) // +1 wei: below the 10% bump
			r.sendOne(ctx, key, nonce, same, new(big.Int).Add(fc, big.NewInt(1)), seq)
			bumpT, bumpF := pct(tip, 120), pct(fc, 120) // +20%: accepted as replacement
			r.sendOne(ctx, key, nonce, bumpT, bumpF, seq)
			nonce++
		default:
			if ok := r.sendOne(ctx, key, nonce, tip, r.feeCap.Load(), seq); ok {
				nonce++
			} else if n, err := r.client.PendingNonceAt(ctx, addr); err == nil {
				nonce = n // resync after a rejection (e.g. nonce too low after a restart)
			}
		}
	}
}

func pct(v *big.Int, p int64) *big.Int {
	return new(big.Int).Div(new(big.Int).Mul(v, big.NewInt(p)), big.NewInt(100))
}

func (r *runner) sendOne(ctx context.Context, key *ecdsa.PrivateKey, nonce uint64, tip, feeCap *big.Int, seq int) bool {
	tx, err := devnet.SignDynamic(key, nonce, &r.target, new(big.Int), r.gas, tip, feeCap, r.calldata(seq))
	if err != nil {
		r.recordErr(err)
		return false
	}
	r.attempted.Add(1)
	at := time.Now()
	if err := r.client.SendTransaction(ctx, tx); err != nil {
		if ctx.Err() == nil {
			r.recordErr(err)
		}
		return false
	}
	r.accepted.Add(1)
	if r.cfg.OnSent != nil {
		r.cfg.OnSent(SentInfo{Hash: tx.Hash(), From: crypto.PubkeyToAddress(key.PublicKey), Nonce: nonce})
	}
	r.mu.Lock()
	r.sent[tx.Hash()] = &sentTx{at: at}
	r.mu.Unlock()
	return true
}

func (r *runner) recordErr(err error) {
	k := ClassifyError(err)
	r.errMu.Lock()
	r.errs[k]++
	r.errMu.Unlock()
}

// collector matches mined transactions with the send timestamps.
type collector struct {
	r        *runner
	next     uint64 // next block number to look at
	blocks   int
	fillSum  float64
	maxTxs   int
	included int
	lat      []time.Duration
	lastInc  time.Time
	mu       sync.Mutex
}

type blockInfo struct {
	Number       hexutil.Uint64 `json:"number"`
	GasUsed      hexutil.Uint64 `json:"gasUsed"`
	GasLimit     hexutil.Uint64 `json:"gasLimit"`
	Transactions []common.Hash  `json:"transactions"`
}

func (c *collector) run(ctx context.Context) {
	head, _ := c.r.client.BlockNumber(ctx)
	c.next = head + 1
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cur, err := c.r.client.BlockNumber(ctx)
		if err != nil {
			continue
		}
		for ; c.next <= cur; c.next++ {
			var b *blockInfo
			if err := c.r.rpc.CallContext(ctx, &b, "eth_getBlockByNumber", hexutil.EncodeUint64(c.next), false); err != nil || b == nil {
				break
			}
			now := time.Now()
			c.mu.Lock()
			if len(b.Transactions) > 0 {
				c.blocks++
				c.fillSum += float64(b.GasUsed) * 100 / float64(max(uint64(b.GasLimit), 1))
				c.maxTxs = max(c.maxTxs, len(b.Transactions))
			}
			c.r.mu.Lock()
			for _, h := range b.Transactions {
				if s, ok := c.r.sent[h]; ok && s.included.IsZero() {
					s.included = now
					c.included++
					c.lat = append(c.lat, now.Sub(s.at))
					c.lastInc = now
				}
			}
			c.r.mu.Unlock()
			c.mu.Unlock()
		}
	}
}

// pendingCount is the number of accepted transactions not yet seen in a block.
func (c *collector) pendingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int(c.r.accepted.Load()) - c.included
}

func (r *runner) report(c *collector, sendDur, total time.Duration) *Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	rep := &Report{Errors: map[string]int{}}
	rep.Config.Senders, rep.Config.Mode, rep.Config.Scenario = r.cfg.Senders, r.cfg.Mode, r.cfg.Scenario
	rep.Config.DataBytes, rep.Config.Rate, rep.Config.Gas = r.cfg.DataBytes, r.cfg.Rate, r.gas
	rep.Attempted, rep.Accepted, rep.Included = int(r.attempted.Load()), int(r.accepted.Load()), c.included
	r.errMu.Lock()
	for k, v := range r.errs {
		rep.Errors[k] = v
	}
	r.errMu.Unlock()
	rep.SendSeconds, rep.TotalSeconds = sendDur.Seconds(), total.Seconds()
	if sendDur > 0 {
		rep.AcceptedPerSec = float64(rep.Accepted) / sendDur.Seconds()
	}
	if span := c.lastInc; !span.IsZero() {
		start := time.Now().Add(-total)
		if d := span.Sub(start).Seconds(); d > 0 {
			rep.IncludedPerSec = float64(c.included) / d
		}
	}
	rep.LatencyMillis = Compute(c.lat)
	rep.Blocks, rep.MaxTxsPerBlock = c.blocks, c.maxTxs
	if c.blocks > 0 {
		rep.AvgGasFillPct = c.fillSum / float64(c.blocks)
	}
	rep.MaxPending, rep.MaxQueued = r.maxPending.Load(), r.maxQueued.Load()
	var st map[string]hexutil.Uint64
	if err := r.rpc.Call(&st, "txpool_status"); err == nil {
		rep.FinalPending, rep.FinalQueued = uint64(st["pending"]), uint64(st["queued"])
	}
	rep.NotIncluded = rep.Accepted - rep.Included
	return rep
}

// Format renders a human readable report.
func (rep *Report) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "scenario=%s mode=%s senders=%d dataBytes=%d gas/tx=%d\n", rep.Config.Scenario, rep.Config.Mode, rep.Config.Senders, rep.Config.DataBytes, rep.Config.Gas)
	fmt.Fprintf(&b, "attempted=%d accepted=%d included=%d acceptedButNotIncluded=%d\n", rep.Attempted, rep.Accepted, rep.Included, rep.NotIncluded)
	fmt.Fprintf(&b, "send phase %.2fs (%.0f accepted tx/s), total %.2fs, included %.0f tx/s\n", rep.SendSeconds, rep.AcceptedPerSec, rep.TotalSeconds, rep.IncludedPerSec)
	l := rep.LatencyMillis
	fmt.Fprintf(&b, "inclusion latency ms (n=%d): min %.0f p50 %.0f p90 %.0f p99 %.0f max %.0f\n", l.N, l.Min, l.P50, l.P90, l.P99, l.Max)
	fmt.Fprintf(&b, "blocks with txs=%d avgGasFill=%.1f%% maxTxsPerBlock=%d\n", rep.Blocks, rep.AvgGasFillPct, rep.MaxTxsPerBlock)
	fmt.Fprintf(&b, "txpool: max pending=%d max queued=%d | final pending=%d queued=%d\n", rep.MaxPending, rep.MaxQueued, rep.FinalPending, rep.FinalQueued)
	if len(rep.Errors) > 0 {
		keys := make([]string, 0, len(rep.Errors))
		for k := range rep.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("errors:\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "  %-28s %d\n", k, rep.Errors[k])
		}
	}
	return b.String()
}
