//go:build chaos

package chaos

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/sergiiogg7/geth-lab/internal/loadgen"
	"github.com/sergiiogg7/geth-lab/internal/nodeproc"
)

// C3b: why did C3 see 1,600 accepted submissions and only 192 mined?
//
// Hypothesis (from reading go-ethereum v1.17.10, written before the run):
// eth/api_backend.go SendTx swallows "temporary" pool rejections (ErrUnderpriced, ErrTxPoolOverflow,
// ErrFutureReplacePending; core/txpool/locals/errors.go IsTemporaryReject) and hands the transaction to the
// local-transaction tracker (core/txpool/locals/tx_tracker.go), which re-submits what the pool forgot once a minute
// (recheckInterval). So the client gets a hash, the pool says no, and the node promises to retry.
//
// Predictions:
//
//	default flags : errors = none, txpool_local gauge about the number of submissions, mined count rises in steps
//	                of roughly one pool-full per minute (the recheck), not all at once;
//	--txpool.nolocals : the tracker is not created (eth/backend.go:341), the client gets "underpriced" errors,
//	                accepted ~ mined, txpool_local = 0.
func TestC3b_LocalTracker(t *testing.T) {
	res := newResult(t, "C3b-local-tracker")
	limits := map[string]int{"globalslots": 64, "globalqueue": 64, "accountslots": 16, "accountqueue": 16}
	flood := func(opts nodeproc.DevOpts, label string, watch time.Duration) {
		p := startNode(t, gethBin, label, opts)
		readyAfter(t, p)
		c := dial(t, opts.URL())
		key := func(k string) string { return label + "." + k }

		var (
			mu     sync.Mutex
			hashes []common.Hash
		)
		before, _ := scrapeMetrics(opts.MetricsPort)
		rep, err := loadgen.Run(context.Background(), loadgen.Config{
			RPC: opts.URL(), Senders: 16, Txs: 1600, Seed: "c3b-" + label, Drain: 12 * time.Second,
			OnSent: func(s loadgen.SentInfo) { mu.Lock(); hashes = append(hashes, s.Hash); mu.Unlock() },
		})
		if err != nil {
			t.Fatal(err)
		}
		after, _ := scrapeMetrics(opts.MetricsPort)
		res.set(t, key("attempted"), rep.Attempted)
		res.set(t, key("accepted"), rep.Accepted)
		res.set(t, key("includedAtDrainEnd"), rep.Included)
		res.set(t, key("errors"), rep.Errors)
		res.set(t, key("metricsDelta"), metricsDelta(before, after, "txpool_valid", "txpool_underpriced", "txpool_overflowed", "txpool_pending_ratelimit", "txpool_queued_ratelimit"))
		res.set(t, key("txpool_local_gauge"), after["txpool_local"])

		// Does the node deliver the "lost" transactions later? Count canonical txs among the accepted hashes.
		mu.Lock()
		accepted := append([]common.Hash(nil), hashes...)
		mu.Unlock()
		want := map[common.Hash]bool{}
		for _, h := range accepted {
			want[h] = true
		}
		type point struct {
			Seconds  int     `json:"seconds"`
			Included int     `json:"included"`
			Pending  uint64  `json:"pending"`
			Local    float64 `json:"txpoolLocal"`
		}
		var series []point
		start := time.Now()
		for time.Since(start) < watch {
			head, _ := blockNumber(c)
			mined, _ := canonicalTxs(c, head)
			n := 0
			for h := range mined {
				if want[h] {
					n++
				}
			}
			st, _ := txpoolStatus(c)
			m, _ := scrapeMetrics(opts.MetricsPort)
			series = append(series, point{int(time.Since(start).Seconds()), n, st.Pending, m["txpool_local"]})
			if n >= len(accepted) {
				break
			}
			time.Sleep(10 * time.Second)
		}
		res.set(t, key("timeline"), series)
		if len(series) > 0 {
			last := series[len(series)-1]
			res.set(t, key("includedAtEnd"), last.Included)
			res.set(t, key("watchedSeconds"), last.Seconds)
		}
	}

	def := nodeproc.DevOpts{HTTPPort: 18622, MetricsPort: 16622, Period: 4, TxPool: limits}
	flood(def, "default", 4*time.Minute)

	nl := nodeproc.DevOpts{HTTPPort: 18623, MetricsPort: 16623, Period: 4, TxPool: limits, Extra: []string{"--txpool.nolocals"}}
	flood(nl, "nolocals", 30*time.Second)
}
