//go:build chaos

package chaos

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sergiiogg7/geth-lab/internal/loadgen"
	"github.com/sergiiogg7/geth-lab/internal/nodeproc"
)

// C4: restart the SAME datadir with different storage-related flags.
//
// Hypothesis: --cache is a pure runtime knob (any value works); changing the
// state scheme or the gc mode of an existing database is either refused with a
// clear error or silently accepted - we find out which, for each flag.
func TestC4_RestartWithChangedStorageFlags(t *testing.T) {
	res := newResult(t, "C4-restart-changed-flags")
	dir := t.TempDir()
	port := 18631

	// Phase 0: build some history with defaults, stop gracefully.
	base := nodeproc.DevOpts{Datadir: dir, HTTPPort: port, MetricsPort: 16631}
	p := startNode(t, gethBin, "seed", base)
	readyAfter(t, p)
	rep, err := loadgen.Run(ctx30bg(), loadgen.Config{RPC: base.URL(), Senders: 4, Txs: 400, Rate: 100, Seed: "c4", Drain: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	c := dial(t, base.URL())
	head, _ := blockNumber(c)
	if ok, err := p.Interrupt(60 * time.Second); err != nil || !ok {
		t.Fatalf("graceful stop: %v %v", ok, err)
	}
	res.set(t, "seed.txs", rep.Included)
	res.set(t, "seed.head", head)
	res.set(t, "seed.datadirBytes", dirSize(dir))
	res.set(t, "seed.defaultsLog", logLines(logPathFor(t, "seed"), "State scheme", "Allocated trie memory caches", "Sanitizing cache"))

	variants := []struct {
		name  string
		cache int
		gc    string
		sch   string
	}{
		{"cache-128MB", 128, "", ""},
		{"cache-2048MB", 2048, "", ""},
		{"gcmode-archive-on-path-db", 0, "archive", ""},
		{"scheme-hash-on-path-db", 0, "", "hash"},
		{"explicit-path-full-again", 0, "full", "path"},
	}
	for i, v := range variants {
		o := base
		o.HTTPPort = port + 1 + i
		o.MetricsPort = 16632 + i
		o.Cache, o.GCMode, o.StateScheme = v.cache, v.gc, v.sch
		pp := startNode(t, gethBin, v.name, o)
		ready, rerr := pp.WaitReady(30 * time.Second)
		if rerr != nil {
			res.set(t, v.name+".result", "NODE DID NOT START")
			res.set(t, v.name+".error", logLines(logPathFor(t, v.name), "Fatal", "fatal", "incompatible", "mismatch", "cannot", "invalid", "refus", "must"))
			<-pp.Done()
			res.set(t, v.name+".exitError", errString(pp.Err()))
			continue
		}
		cc := dial(t, o.URL())
		h, _ := blockNumber(cc)
		mined, merr := mineOne(cc, o.URL(), 15*time.Second)
		m, _ := scrapeMetrics(o.MetricsPort)
		res.set(t, v.name+".result", "started")
		res.set(t, v.name+".readySeconds", ready.Seconds())
		res.set(t, v.name+".headAfterRestart", h)
		res.set(t, v.name+".newTxMined", mined)
		res.set(t, v.name+".newTxError", errString(merr))
		res.set(t, v.name+".heapMB", m["system_memory_used"]/1e6)
		res.set(t, v.name+".log", logLines(logPathFor(t, v.name), "State scheme", "Allocated trie memory caches", "Sanitizing cache", "nclean", "archive", "Archive"))
		if ok, err := pp.Interrupt(60 * time.Second); err != nil || !ok {
			t.Logf("graceful stop of %s: %v %v", v.name, ok, err)
			_ = pp.Kill()
		}
	}
}

func dirSize(root string) int64 {
	var n int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}
