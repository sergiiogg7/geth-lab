//go:build chaos

package chaos

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/sergiiogg7/geth-lab/internal/loadgen"
	"github.com/sergiiogg7/geth-lab/internal/nodeproc"
)

// C5: make the chain go backwards / reorganise, and see what `watch` says.
//
// A dev node has one block producer, so there is no natural fork. Two ways to
// force one:
//   - stock geth:  debug_setHead(n) rewinds the canonical chain to block n;
//   - labnode:     lab_fork(depth) moves the head back AND the beacon then
//     seals different blocks at the same heights (a real reorg).
//
// Hypothesis: setHead is seen by `watch` as REWIND (the old blocks are gone,
// not replaced); lab_fork is seen as REORG with the right depth.
func TestC5_RewindAndReorg(t *testing.T) {
	res := newResult(t, "C5-rewind-and-reorg")
	t.Run("geth-debug_setHead", func(t *testing.T) {
		runReorg(t, res, "geth-setHead", gethBin, 18641, func(c *rpc.Client, head uint64) error {
			return c.Call(nil, "debug_setHead", hexutil.EncodeUint64(head-3))
		})
	})
	t.Run("labnode-lab_fork", func(t *testing.T) {
		runReorg(t, res, "labnode-fork", labBin, 18642, func(c *rpc.Client, head uint64) error {
			var out map[string]any
			return c.Call(&out, "lab_fork", "0x3")
		})
	})
}

func runReorg(t *testing.T, res *result, label, binary string, port int, trigger func(*rpc.Client, uint64) error) {
	key := func(k string) string { return label + "." + k }
	opts := nodeproc.DevOpts{HTTPPort: port, Period: 1}
	p := startNode(t, binary, label, opts)
	readyAfter(t, p)
	c := dial(t, opts.URL())

	// Follow the chain with the real `watch` binary.
	var out bytes.Buffer
	var mu sync.Mutex
	w := exec.Command(watchBin, "-rpc", opts.URL(), "-interval", "100ms", "-duration", "40s")
	w.Stdout = writerFunc(func(b []byte) { mu.Lock(); out.Write(b); mu.Unlock() })
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	watchDone := make(chan struct{})
	go func() { _ = w.Wait(); close(watchDone) }()
	t.Cleanup(func() { _ = w.Process.Kill() })

	// A little traffic so the soon-to-be-dropped blocks contain transactions.
	var hashes []common.Hash
	_, err := loadgen.Run(context.Background(), loadgen.Config{RPC: opts.URL(), Senders: 2, Txs: 20, Rate: 4, Seed: "c5" + label, Drain: 3 * time.Second,
		OnSent: func(s loadgen.SentInfo) { hashes = append(hashes, s.Hash) }})
	if err != nil {
		t.Fatal(err)
	}
	// Let the pool empty and a few empty blocks pile up.
	for i := 0; i < 100; i++ {
		if st, _ := txpoolStatus(c); st.Pending == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(4 * time.Second)

	head, _ := blockNumber(c)
	minedBefore, _ := canonicalTxs(c, head)
	// Remember the hash of the block that is about to be orphaned.
	var oldBlk struct {
		Hash common.Hash `json:"hash"`
	}
	_ = c.Call(&oldBlk, "eth_getBlockByNumber", hexutil.EncodeUint64(head), false)
	res.set(t, key("headBeforeTrigger"), head)
	if err := trigger(c, head); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	headRight, _ := blockNumber(c)
	res.set(t, key("headRightAfterTrigger"), headRight)

	time.Sleep(8 * time.Second) // let the chain grow again
	headLater, _ := blockNumber(c)
	var newBlk struct {
		Hash common.Hash `json:"hash"`
	}
	_ = c.Call(&newBlk, "eth_getBlockByNumber", hexutil.EncodeUint64(head), false)
	res.set(t, key("headLater"), headLater)
	res.set(t, key("oldBlockAtOldHeadHash"), oldBlk.Hash.Hex())
	res.set(t, key("blockAtSameHeightNow"), newBlk.Hash.Hex())
	res.set(t, key("sameHeightDifferentHash"), oldBlk.Hash != newBlk.Hash)

	// Where did the transactions of the dropped blocks go?
	minedAfter, _ := canonicalTxs(c, headLater)
	var stillMined, gone int
	for h := range minedBefore {
		if _, ok := minedAfter[h]; ok {
			stillMined++
		} else {
			gone++
		}
	}
	res.set(t, key("txsMinedBefore"), len(minedBefore))
	res.set(t, key("txsStillCanonicalAfter"), stillMined)
	res.set(t, key("txsNotCanonicalAfter"), gone)

	<-watchDone
	mu.Lock()
	var events []string
	for _, ln := range strings.Split(out.String(), "\n") {
		if strings.Contains(ln, "REWIND") || strings.Contains(ln, "REORG") || strings.Contains(ln, "summary") {
			events = append(events, strings.TrimSpace(ln))
		}
	}
	mu.Unlock()
	res.set(t, key("watchEvents"), events)
	joined := strings.Join(events, "\n")
	if label == "labnode-fork" && !strings.Contains(joined, "REORG") {
		t.Errorf("watch did not report a REORG after lab_fork:\n%s", joined)
	}
	if label == "geth-setHead" && !strings.Contains(joined, "REWIND") {
		t.Errorf("watch did not report a REWIND after debug_setHead:\n%s", joined)
	}
}

// writerFunc turns a func into an io.Writer.
type writerFunc func([]byte)

func (f writerFunc) Write(b []byte) (int, error) { f(b); return len(b), nil }
