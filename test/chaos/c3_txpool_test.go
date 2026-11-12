//go:build chaos

package chaos

import (
	"testing"
	"time"

	"github.com/sergiiogg7/geth-lab/internal/loadgen"
	"github.com/sergiiogg7/geth-lab/internal/nodeproc"
)

// C3: flood far beyond the txpool limits.
//
// The node runs with deliberately tiny limits and slow blocks (one every 4 s)
// so the pool is the bottleneck, not the block gas limit:
//
//	--txpool.globalslots 64 --txpool.globalqueue 64 --txpool.accountslots 16 --txpool.accountqueue 16
//
// Three phases on the same node: (1) steady flood from 16 senders,
// (2) nonce gaps (queued transactions), (3) same-nonce replacements.
//
// Hypothesis: the pool never grows beyond its limits; extra transactions are
// rejected with "txpool is full" / "underpriced" style errors; a gap leaves
// transactions queued (not mined); a replacement needs a >=10% price bump.
func TestC3_TxPoolOverflow(t *testing.T) {
	res := newResult(t, "C3-txpool-overflow")
	opts := nodeproc.DevOpts{HTTPPort: 18621, MetricsPort: 16621, Period: 4,
		TxPool: map[string]int{"globalslots": 64, "globalqueue": 64, "accountslots": 16, "accountqueue": 16}}
	p := startNode(t, gethBin, "run", opts)
	readyAfter(t, p)
	res.set(t, "limits", "globalslots=64 globalqueue=64 accountslots=16 accountqueue=16 pricelimit=1 pricebump=10 period=4s")

	names := []string{"txpool_valid", "txpool_invalid", "txpool_underpriced", "txpool_overflowed", "txpool_known", "txpool_pending_discard", "txpool_pending_nofunds", "txpool_pending_ratelimit", "txpool_queued_discard", "txpool_queued_nofunds", "txpool_queued_ratelimit", "txpool_queued_eviction", "txpool_dropbetweenreorg"}
	phase := func(name string, cfg loadgen.Config) {
		before, _ := scrapeMetrics(opts.MetricsPort)
		cfg.RPC = opts.URL()
		rep, err := loadgen.Run(ctx30bg(), cfg)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		after, _ := scrapeMetrics(opts.MetricsPort)
		res.set(t, name+".attempted", rep.Attempted)
		res.set(t, name+".accepted", rep.Accepted)
		res.set(t, name+".included", rep.Included)
		res.set(t, name+".errors", rep.Errors)
		res.set(t, name+".maxPending", rep.MaxPending)
		res.set(t, name+".maxQueued", rep.MaxQueued)
		res.set(t, name+".finalPending", rep.FinalPending)
		res.set(t, name+".finalQueued", rep.FinalQueued)
		res.set(t, name+".metricsDelta", metricsDelta(before, after, names...))
	}

	phase("P1-steady-flood", loadgen.Config{Senders: 16, Txs: 1600, Seed: "c3a", Drain: 12 * time.Second})
	phase("P2-nonce-gaps", loadgen.Config{Senders: 8, Txs: 320, Scenario: "gap", GapEvery: 3, Seed: "c3b", Drain: 2 * time.Second})
	phase("P3-replacements", loadgen.Config{Senders: 4, Txs: 40, Scenario: "replace", Seed: "c3c", Drain: 10 * time.Second})

	// Sanity: the pool never exceeded its configured capacity by an absurd margin.
	if mp, ok := res.data["P1-steady-flood.maxPending"].(uint64); ok && mp > 64*4 {
		t.Errorf("pending grew to %d with globalslots=64", mp)
	}
}
