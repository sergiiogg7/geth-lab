// Command watch follows the head of a node over JSON-RPC and reports blocks,
// block times, gas usage, reorgs, rewinds, stalls and RPC outages. With
// -serve it also exposes a tiny status page, so the lab has an observability
// view that needs no Prometheus or Grafana.
//
//	watch -rpc http://127.0.0.1:8545                 follow forever
//	watch -rpc ... -once                             print one status snapshot and exit
//	watch -rpc ... -serve 127.0.0.1:8081             also serve /  and /status.json
//
// The reorg logic lives in internal/chainwatch (pure, unit tested); this file
// is the I/O shell around it. That split ("functional core, imperative shell")
// is the same idea as keeping a Spring service free of the controller.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/sergiiogg7/geth-lab/internal/chainwatch"
)

// rpcHeader is the JSON shape of eth_getBlockByNumber(…, false).
type rpcHeader struct {
	Number       hexutil.Uint64 `json:"number"`
	Hash         common.Hash    `json:"hash"`
	ParentHash   common.Hash    `json:"parentHash"`
	Timestamp    hexutil.Uint64 `json:"timestamp"`
	GasUsed      hexutil.Uint64 `json:"gasUsed"`
	GasLimit     hexutil.Uint64 `json:"gasLimit"`
	Transactions []common.Hash  `json:"transactions"`
}

func (r *rpcHeader) toHeader() chainwatch.Header {
	return chainwatch.Header{Number: uint64(r.Number), Hash: r.Hash, ParentHash: r.ParentHash, Time: uint64(r.Timestamp),
		GasUsed: uint64(r.GasUsed), GasLimit: uint64(r.GasLimit), TxCount: len(r.Transactions)}
}

// rpcSource adapts the JSON-RPC client to chainwatch.Source.
type rpcSource struct{ c *rpc.Client }

func (s rpcSource) HeaderByHash(ctx context.Context, h common.Hash) (chainwatch.Header, error) {
	var out *rpcHeader
	if err := s.c.CallContext(ctx, &out, "eth_getBlockByHash", h, false); err != nil {
		return chainwatch.Header{}, err
	}
	if out == nil {
		return chainwatch.Header{}, fmt.Errorf("block %s not found", h)
	}
	return out.toHeader(), nil
}

// Status is what the snapshot / status page shows.
type Status struct {
	mu sync.Mutex

	RPC           string    `json:"rpc"`
	Client        string    `json:"client"`
	Up            bool      `json:"up"`
	Head          uint64    `json:"head"`
	HeadHash      string    `json:"headHash"`
	LastBlockAt   time.Time `json:"lastBlockAt"`
	SecondsSince  float64   `json:"secondsSinceLastBlock"`
	Blocks        uint64    `json:"blocksSeen"`
	Reorgs        uint64    `json:"reorgs"`
	Rewinds       uint64    `json:"rewinds"`
	MaxReorgDepth uint64    `json:"maxReorgDepth"`
	Pending       uint64    `json:"txpoolPending"`
	Queued        uint64    `json:"txpoolQueued"`
	Peers         uint64    `json:"peers"`
	AvgBlockTime  float64   `json:"avgBlockTimeSeconds"`
	AvgFillPct    float64   `json:"avgGasFillPercent"`
	Outages       int       `json:"rpcOutages"`
	Recent        []string  `json:"recentEvents"`

	btSum, btN uint64
	fillSum    float64
	fillN      uint64
}

func (s *Status) addEvent(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Recent = append(s.Recent, line)
	if len(s.Recent) > 30 {
		s.Recent = s.Recent[len(s.Recent)-30:]
	}
}

func (s *Status) snapshot() *Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &Status{RPC: s.RPC, Client: s.Client, Up: s.Up, Head: s.Head, HeadHash: s.HeadHash, LastBlockAt: s.LastBlockAt,
		Blocks: s.Blocks, Reorgs: s.Reorgs, Rewinds: s.Rewinds, MaxReorgDepth: s.MaxReorgDepth, Pending: s.Pending,
		Queued: s.Queued, Peers: s.Peers, AvgBlockTime: s.AvgBlockTime, AvgFillPct: s.AvgFillPct, Outages: s.Outages,
		Recent: append([]string(nil), s.Recent...)}
	if !s.LastBlockAt.IsZero() {
		c.SecondsSince = time.Since(s.LastBlockAt).Seconds()
	}
	return c
}

func main() {
	var (
		url      = flag.String("rpc", "http://127.0.0.1:8545", "JSON-RPC HTTP endpoint")
		interval = flag.Duration("interval", 250*time.Millisecond, "poll interval")
		stall    = flag.Duration("stall", 15*time.Second, "report STALL when no new block for this long")
		duration = flag.Duration("duration", 0, "stop after this long (0 = until Ctrl-C)")
		once     = flag.Bool("once", false, "print one status snapshot as JSON and exit")
		serve    = flag.String("serve", "", "serve a status page on this address, e.g. 127.0.0.1:8081")
		quiet    = flag.Bool("quiet", false, "do not print plain block events (still prints reorgs, stalls, outages)")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	st := &Status{RPC: *url}
	if *once {
		if err := pollOnce(ctx, *url, st); err != nil {
			fmt.Fprintln(os.Stderr, "watch:", err)
			os.Exit(1)
		}
		out, _ := json.MarshalIndent(st.snapshot(), "", "  ")
		fmt.Println(string(out))
		return
	}
	if *serve != "" {
		go serveStatus(*serve, st)
		fmt.Printf("status page: http://%s/  (json: /status.json)\n", *serve)
	}
	run(ctx, *url, *interval, *stall, *quiet, st)

	// final report
	s := st.snapshot()
	fmt.Printf("\nsummary: blocks=%d reorgs=%d (max depth %d) rewinds=%d outages=%d avgBlockTime=%.2fs avgFill=%.1f%%\n",
		s.Blocks, s.Reorgs, s.MaxReorgDepth, s.Rewinds, s.Outages, s.AvgBlockTime, s.AvgFillPct)
}

// pollOnce fills a Status from a single round of RPC calls.
func pollOnce(ctx context.Context, url string, st *Status) error {
	c, err := rpc.DialContext(ctx, url)
	if err != nil {
		return err
	}
	defer c.Close()
	var h *rpcHeader
	if err := c.CallContext(ctx, &h, "eth_getBlockByNumber", "latest", false); err != nil {
		return err
	}
	refreshExtras(ctx, c, st)
	st.mu.Lock()
	st.Up, st.Head, st.HeadHash = true, uint64(h.Number), h.Hash.Hex()
	st.LastBlockAt = time.Unix(int64(h.Timestamp), 0)
	st.mu.Unlock()
	return nil
}

// refreshExtras reads the cheap secondary numbers; failures are ignored (some
// nodes do not expose the txpool namespace).
func refreshExtras(ctx context.Context, c *rpc.Client, st *Status) {
	var ver string
	var tp map[string]hexutil.Uint64
	var peers hexutil.Uint64
	_ = c.CallContext(ctx, &ver, "web3_clientVersion")
	_ = c.CallContext(ctx, &tp, "txpool_status")
	_ = c.CallContext(ctx, &peers, "net_peerCount")
	st.mu.Lock()
	st.Client, st.Pending, st.Queued, st.Peers = ver, uint64(tp["pending"]), uint64(tp["queued"]), uint64(peers)
	st.mu.Unlock()
}

// run is the polling loop. It survives RPC outages (the chaos scenarios kill
// the node on purpose) and reports how long they lasted.
func run(ctx context.Context, url string, interval, stall time.Duration, quiet bool, st *Status) {
	var (
		client     *rpc.Client
		tracker    *chainwatch.Tracker
		downSince  time.Time
		stalled    bool
		lastNew    = time.Now()
		lastExtras time.Time
		printf     = func(f string, a ...any) { fmt.Printf(time.Now().Format("15:04:05.000")+" "+f+"\n", a...) }
		ticker     = time.NewTicker(interval)
		hdr        *rpcHeader
	)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if client == nil {
			c, err := rpc.DialContext(ctx, url)
			if err != nil {
				markDown(st, &downSince, printf, err)
				continue
			}
			client = c
			tracker = chainwatch.NewTracker(rpcSource{c})
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		hdr = nil
		err := client.CallContext(cctx, &hdr, "eth_getBlockByNumber", "latest", false)
		cancel()
		if err != nil || hdr == nil {
			if ctx.Err() != nil {
				return
			}
			markDown(st, &downSince, printf, err)
			client.Close()
			client = nil
			continue
		}
		if !downSince.IsZero() {
			printf("RPC UP again after %s", time.Since(downSince).Round(time.Millisecond))
			st.addEvent("rpc up after " + time.Since(downSince).Round(time.Millisecond).String())
			downSince = time.Time{}
			// The node restarted: forget what we knew, a new head is not a "reorg".
			tracker = chainwatch.NewTracker(rpcSource{client})
		}
		st.mu.Lock()
		st.Up = true
		st.mu.Unlock()

		evs, err := tracker.Observe(ctx, hdr.toHeader(), time.Now())
		if err != nil {
			printf("watch error: %v", err)
		}
		for _, e := range evs {
			lastNew, stalled = time.Now(), false
			line := e.String()
			if e.Kind != chainwatch.KindBlock || !quiet {
				printf("%s", line)
			}
			st.addEvent(time.Now().Format("15:04:05") + " " + line)
			st.mu.Lock()
			st.Head, st.HeadHash, st.LastBlockAt = e.Head.Number, e.Head.Hash.Hex(), time.Now()
			if e.Kind == chainwatch.KindBlock {
				st.btSum += e.BlockTime
				st.btN++
				st.fillSum += float64(e.Head.GasUsed) * 100 / float64(max(e.Head.GasLimit, 1))
				st.fillN++
				st.AvgBlockTime = float64(st.btSum) / float64(st.btN)
				st.AvgFillPct = st.fillSum / float64(st.fillN)
			}
			st.Blocks, st.Reorgs, st.Rewinds, st.MaxReorgDepth = tracker.Blocks, tracker.Reorgs, tracker.Rewinds, tracker.MaxReorgDepth
			st.mu.Unlock()
		}
		if !stalled && time.Since(lastNew) > stall {
			stalled = true
			printf("STALL no new block for %s (head #%d)", stall, hdr.Number)
			st.addEvent("STALL")
		}
		if time.Since(lastExtras) > 2*time.Second {
			lastExtras = time.Now()
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			refreshExtras(cctx, client, st)
			cancel()
		}
	}
}

func markDown(st *Status, since *time.Time, printf func(string, ...any), err error) {
	if since.IsZero() {
		*since = time.Now()
		printf("RPC DOWN: %v", err)
		st.mu.Lock()
		st.Up = false
		st.Outages++
		st.mu.Unlock()
		st.addEvent("rpc down")
	}
}

var page = template.Must(template.New("p").Parse(`<!doctype html><meta charset=utf-8><meta http-equiv=refresh content=2>
<title>geth-lab status</title>
<style>body{font:14px/1.4 system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem}
table{border-collapse:collapse}td{padding:.15rem .8rem .15rem 0}.bad{color:#b00020;font-weight:600}.ok{color:#0a7d33;font-weight:600}
pre{background:#f4f4f4;padding:.6rem;overflow:auto}@media(prefers-color-scheme:dark){body{background:#111;color:#ddd}pre{background:#222}}</style>
<h1>geth-lab status</h1>
<p>{{.RPC}} - {{.Client}} - <span class="{{if .Up}}ok{{else}}bad{{end}}">{{if .Up}}UP{{else}}DOWN{{end}}</span></p>
<table>
<tr><td>head</td><td>#{{.Head}} {{.HeadHash}}</td></tr>
<tr><td>seconds since last block</td><td>{{printf "%.1f" .SecondsSince}}</td></tr>
<tr><td>blocks seen / avg block time / avg gas fill</td><td>{{.Blocks}} / {{printf "%.2f" .AvgBlockTime}}s / {{printf "%.1f" .AvgFillPct}}%</td></tr>
<tr><td>reorgs / max depth / rewinds</td><td>{{.Reorgs}} / {{.MaxReorgDepth}} / {{.Rewinds}}</td></tr>
<tr><td>txpool pending / queued</td><td>{{.Pending}} / {{.Queued}}</td></tr>
<tr><td>peers / rpc outages</td><td>{{.Peers}} / {{.Outages}}</td></tr>
</table>
<h2>recent events</h2><pre>{{range .Recent}}{{.}}
{{end}}</pre>`))

func serveStatus(addr string, st *Status) {
	mux := http.NewServeMux()
	mux.HandleFunc("/status.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st.snapshot())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _ = page.Execute(w, st.snapshot()) })
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, "status server:", err)
	}
}
