//go:build chaos

package chaos

import (
	"context"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rpc"

	"github.com/sergiiogg7/geth-lab/internal/loadgen"
	"github.com/sergiiogg7/geth-lab/internal/nodeproc"
)

// C7: resource pressure, memory only (disk-full is analysed in the postmortem, never reproduced).
//
// The node runs inside a Windows Job Object that caps its COMMITTED memory (internal/nodeproc/memlimit_windows.go).
// The cap is 700 MB, well under the 1 GB safety line of this lab, and the OS enforces it: the node cannot take more.
// A watchdog additionally kills the node if the job's measured peak ever exceeds 900 MB, and the job is closed with
// kill-on-close so a crashed test cannot leave a node behind.
//
// Hypotheses (written before the first run):
//
//	A  --cache 256 under a 700 MB cap and a 1,500-tx flood: survives, peak far below the cap (the C4 runs used 18 to 63 MB).
//	B  geth defaults (--cache 4096, sanitised to about 2.7 GB on this machine) under the same cap: the node does not
//	   survive, because geth allocates its caches up front; it fails at startup rather than under load.
//	C  an expensive RPC (debug_traceCall of an infinite loop with the default struct logger, which keeps every step in memory)
//	   can push a capped node past its cap; the process then aborts (Go: "fatal error: out of memory"), which is an UNCLEAN
//	   shutdown, so a restart on the same datadir hits the C1 behaviour (rewind) on top of the memory problem.
func TestC7_MemoryCap(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("memory cap uses a Windows Job Object (internal/nodeproc/memlimit_windows.go); no Linux implementation")
	}
	res := newResult(t, "C7-memory-cap")
	const capMB = 700
	const watchdogMB = 900
	res.set(t, "capMB", capMB)

	watch := func(p *nodeproc.Proc) {
		stop := make(chan struct{})
		t.Cleanup(func() { close(stop) })
		go func() {
			for {
				select {
				case <-stop:
					return
				case <-p.Done():
					return
				case <-time.After(250 * time.Millisecond):
					if b, ok := p.MemPeakBytes(); ok && b > uint64(watchdogMB)<<20 {
						t.Errorf("SAFETY: node peak %d MB exceeded %d MB, killing", b>>20, watchdogMB)
						_ = p.Kill()
						return
					}
				}
			}
		}()
	}
	mb := func(p *nodeproc.Proc) float64 {
		b, _ := p.MemPeakBytes()
		return float64(b) / (1 << 20)
	}

	// ---------------------------------------------------------------- A: control
	{
		opts := nodeproc.DevOpts{HTTPPort: 18701, MetricsPort: 16701, Period: 1, Cache: 256, MemLimitMB: capMB}
		p := startNode(t, gethBin, "A-control", opts)
		watch(p)
		res.set(t, "A.readySeconds", readyAfter(t, p).Seconds())
		rep, err := loadgen.Run(context.Background(), loadgen.Config{RPC: opts.URL(), Senders: 8, Txs: 1500, Rate: 150, Mode: "contract", DataBytes: 64, Seed: "c7a", Drain: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		m, _ := scrapeMetrics(opts.MetricsPort)
		res.set(t, "A.accepted", rep.Accepted)
		res.set(t, "A.included", rep.Included)
		res.set(t, "A.peakCommittedMB", mb(p))
		res.set(t, "A.goHeapInUseMB(system_memory_used)", m["system_memory_used"]/(1<<20))
		exited, _ := p.Exited()
		res.set(t, "A.aliveAtEnd", !exited)
		_ = p.Kill()
	}

	// ---------------------------------------------------------------- B: geth defaults under the cap
	{
		opts := nodeproc.DevOpts{HTTPPort: 18702, MetricsPort: 16702, Period: 1, MemLimitMB: capMB} // no --cache
		p := startNode(t, gethBin, "B-default-cache", opts)
		watch(p)
		d, err := p.WaitReady(40 * time.Second)
		exited, code := p.Exited()
		res.set(t, "B.readyError", errString(err))
		res.set(t, "B.readySeconds", d.Seconds())
		res.set(t, "B.exitedBeforeReady", exited)
		res.set(t, "B.exitCode", code)
		res.set(t, "B.peakCommittedMB", mb(p))
		res.set(t, "B.logHighlights", logLines(logPathFor(t, "B-default-cache"), "out of memory", "fatal error", "Sanitizing cache", "Allocated trie memory caches", "runtime:"))
		_ = p.Kill()
	}

	// ---------------------------------------------------------------- C: expensive RPC against a capped, persistent node
	datadir := t.TempDir()
	optsC := nodeproc.DevOpts{Datadir: datadir, HTTPPort: 18703, MetricsPort: 16703, Period: 2, Cache: 256, MemLimitMB: capMB}
	var headBefore uint64
	{
		p := startNode(t, gethBin, "C-trace-bomb-run1", optsC)
		watch(p)
		readyAfter(t, p)
		c := dial(t, optsC.URL())
		if _, err := loadgen.Run(context.Background(), loadgen.Config{RPC: optsC.URL(), Senders: 4, Txs: 300, Rate: 100, Seed: "c7c", Drain: 6 * time.Second}); err != nil {
			t.Fatal(err)
		}
		headBefore, _ = blockNumber(c)
		res.set(t, "C.headBeforeBomb", headBefore)

		// 3 concurrent traces of `JUMPDEST; PUSH1 0; JUMP` (an endless loop) with a 50M gas allowance and a 30 s tracer timeout.
		const victim = "0x00000000000000000000000000000000000000aa"
		call := map[string]any{"to": victim, "gas": "0x2faf080"}
		cfg := map[string]any{"timeout": "30s", "stateOverrides": map[string]any{victim: map[string]any{"code": "0x5b600056"}}}
		var (
			mu       sync.Mutex
			errsSeen []string
			wg       sync.WaitGroup
		)
		bombAt := time.Now()
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cc, err := rpc.Dial(optsC.URL())
				if err != nil {
					return
				}
				defer cc.Close()
				var out any
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				err = cc.CallContext(ctx, &out, "debug_traceCall", call, "latest", cfg)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					s := err.Error()
					if len(s) > 160 {
						s = s[:160]
					}
					errsSeen = append(errsSeen, s)
				} else {
					errsSeen = append(errsSeen, "ok")
				}
			}()
		}
		// Probe the node every 5 s for up to 75 s: is the process still there, does RPC answer, does the metrics port answer?
		type probe struct {
			Sec      int   `json:"sec"`
			Alive    bool  `json:"processAlive"`
			RPC      bool  `json:"rpcAnswers"`
			Metrics  bool  `json:"metricsAnswers"`
			PeakMB   int   `json:"peakMB"`
			LogBytes int64 `json:"logBytes"`
		}
		var probes []probe
		var exitedAt any = "process still existed after 75 s"
		for s := 0; s <= 75; s += 5 {
			if exited, _ := p.Exited(); exited {
				exitedAt = time.Since(bombAt).Seconds()
				break
			}
			var pr probe
			pr.Sec = s
			pr.Alive = true
			pr.RPC = func() bool {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				var n any
				return c.CallContext(ctx, &n, "eth_blockNumber") == nil
			}()
			_, merr := scrapeMetrics(optsC.MetricsPort)
			pr.Metrics = merr == nil
			pk, _ := p.MemPeakBytes()
			pr.PeakMB = int(pk >> 20)
			if st, err := os.Stat(logPathFor(t, "C-trace-bomb-run1")); err == nil {
				pr.LogBytes = st.Size()
			}
			probes = append(probes, pr)
			time.Sleep(5 * time.Second)
		}
		res.set(t, "C.secondsUntilNodeExited", exitedAt)
		res.set(t, "C.probesAfterBomb", probes)
		wg.Wait()
		exited, code := p.Exited()
		res.set(t, "C.nodeExited", exited)
		if exited {
			res.set(t, "C.exitCode", code)
		}
		res.set(t, "C.peakCommittedMB", mb(p))
		res.set(t, "C.clientResults", errsSeen)
		res.set(t, "C.logHighlights", logLines(logPathFor(t, "C-trace-bomb-run1"), "out of memory", "fatal error", "runtime:", "goroutine "))
		_ = p.Kill()
	}

	// ---------------------------------------------------------------- recovery: restart on the same datadir, same cap
	{
		p := startNode(t, gethBin, "C-trace-bomb-run2", optsC)
		watch(p)
		res.set(t, "C.restart.readySeconds", readyAfter(t, p).Seconds())
		c := dial(t, optsC.URL())
		head, _ := blockNumber(c)
		res.set(t, "C.restart.headAfter", head)
		res.set(t, "C.restart.blocksLost", int64(headBefore)-int64(head))
		mined, mErr := mineOne(c, optsC.URL(), 25*time.Second)
		res.set(t, "C.restart.canMineTx", mined)
		res.set(t, "C.restart.mineError", errString(mErr))
		time.Sleep(6 * time.Second)
		mined2, mErr2 := mineOne(c, optsC.URL(), 15*time.Second)
		res.set(t, "C.restart.canMineSecondTx", mined2)
		res.set(t, "C.restart.mineSecondError", errString(mErr2))
		res.set(t, "C.restart.peakCommittedMB", mb(p))
		res.set(t, "C.restart.logHighlights", logLines(logPathFor(t, "C-trace-bomb-run2"), "Head state missing", "Switching from", "Ignoring payload", "Unclean shutdown", "Enabled full-sync"))
		_ = p.Kill()
	}
}
