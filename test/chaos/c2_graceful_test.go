//go:build chaos

package chaos

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/sergiiogg7/geth-lab/internal/loadgen"
	"github.com/sergiiogg7/geth-lab/internal/nodeproc"
)

// C2: graceful stop (Ctrl-C) versus hard kill, on the same workload, and what
// each does to the datadir.
//
// Hypothesis: the graceful stop takes a moment (it flushes the state to disk)
// and the node comes back at the same head with everything; the hard kill is
// instant and leaves the datadir in a state that needs repair on the next start.
func TestC2_GracefulVsHardStop(t *testing.T) {
	res := newResult(t, "C2-graceful-vs-kill")
	for i, mode := range []string{"graceful", "kill"} {
		port := 18611 + i
		t.Run(mode, func(t *testing.T) { runStopMode(t, res, mode, port) })
	}
}

func runStopMode(t *testing.T, res *result, mode string, port int) {
	key := func(k string) string { return mode + "." + k }
	opts := nodeproc.DevOpts{Datadir: t.TempDir(), HTTPPort: port, Period: 1}
	p := startNode(t, gethBin, mode+"-run1", opts)
	readyAfter(t, p)
	c := dial(t, opts.URL())

	// Load for a fixed time: ~8 s at 150 tx/s.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	rep, err := loadgen.Run(ctx, loadgen.Config{RPC: opts.URL(), Senders: 4, Duration: 8 * time.Second, Rate: 150, Drain: 100 * time.Millisecond, Seed: "c2-" + mode})
	if err != nil {
		t.Fatal(err)
	}
	headBefore, _ := blockNumber(c)
	res.set(t, key("txsAccepted"), rep.Accepted)
	res.set(t, key("headBeforeStop"), headBefore)

	// --- stop ---
	stopStart := time.Now()
	switch mode {
	case "graceful":
		exited, err := p.Interrupt(60 * time.Second)
		if err != nil || !exited {
			t.Fatalf("graceful stop failed: exited=%v err=%v", exited, err)
		}
	default:
		if err := p.Kill(); err != nil {
			t.Fatal(err)
		}
	}
	res.set(t, key("stopTakesSeconds"), time.Since(stopStart).Seconds())
	res.set(t, key("stopLogHighlights"), logLines(logPathFor(t, mode+"-run1"), "Got interrupt", "Persisting dirty state", "Persisted dirty state", "Blockchain stopped", "Writing clean trie cache", "Writing snapshot", "HTTP server stopped"))

	// --- restart ---
	p2 := startNode(t, gethBin, mode+"-run2", opts)
	res.set(t, key("restartReadySeconds"), readyAfter(t, p2).Seconds())
	c2 := dial(t, opts.URL())
	headAfter, _ := blockNumber(c2)
	res.set(t, key("headAfterRestart"), headAfter)
	res.set(t, key("blocksLost"), int64(headBefore)-int64(headAfter))

	blocks, problems := integrity(c2, headAfter)
	res.set(t, key("integrity.blocksChecked"), blocks)
	res.set(t, key("integrity.problems"), problems)

	m1, e1 := mineOne(c2, opts.URL(), 15*time.Second)
	time.Sleep(4 * time.Second)
	m2, e2 := mineOne(c2, opts.URL(), 15*time.Second)
	res.set(t, key("functional.firstTxMined"), m1)
	res.set(t, key("functional.secondTxMined"), m2)
	res.set(t, key("functional.errors"), []string{errString(e1), errString(e2)})
	res.set(t, key("startLogHighlights"), logLines(logPathFor(t, mode+"-run2"), "nclean", "Head state missing", "Switching from", "Loaded most recent local block", "Ignoring payload", "Loaded local transaction journal", "Persisted"))
}

// integrity walks blocks 0..head and checks parent linkage and that the dev
// chain still has consistent block numbers. It returns how many blocks it read.
func integrity(c *rpc.Client, head uint64) (int, []string) {
	var problems []string
	var prev common.Hash
	checked := 0
	for n := uint64(0); n <= head; n++ {
		var b struct {
			Hash       common.Hash    `json:"hash"`
			ParentHash common.Hash    `json:"parentHash"`
			Number     hexutil.Uint64 `json:"number"`
		}
		if err := c.CallContext(context.Background(), &b, "eth_getBlockByNumber", hexutil.EncodeUint64(n), false); err != nil {
			problems = append(problems, fmt.Sprintf("block %d: %v", n, err))
			continue
		}
		checked++
		if uint64(b.Number) != n {
			problems = append(problems, fmt.Sprintf("block %d reports number %d", n, uint64(b.Number)))
		}
		if n > 0 && b.ParentHash != prev {
			problems = append(problems, fmt.Sprintf("block %d parent %s != previous hash %s", n, b.ParentHash, prev))
		}
		prev = b.Hash
	}
	return checked, problems
}
