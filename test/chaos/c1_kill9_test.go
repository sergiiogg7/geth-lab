//go:build chaos

package chaos

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/sergiiogg7/geth-lab/internal/devnet"
	"github.com/sergiiogg7/geth-lab/internal/loadgen"
	"github.com/sergiiogg7/geth-lab/internal/nodeproc"
)

// C1: kill -9 under load, then restart on the same datadir.
//
// Hypothesis (written BEFORE the first run):
//   - blocks that were already sealed survive (they are on disk);
//   - transactions that were only in the txpool are lost, except those the
//     txpool journal (local transactions) brings back;
//   - the node answers RPC again within a few seconds and keeps working.
//
// The test runs the scenario for two configurations: geth's defaults, and
// "--gcmode archive --state.scheme hash" (every state is written to disk).
func TestC1_KillNineUnderLoad(t *testing.T) {
	res := newResult(t, "C1-kill9-under-load")
	variants := []struct {
		name  string
		extra []string
		port  int
	}{
		{"default", nil, 18601},
		{"archive-hash", []string{"--gcmode", "archive", "--state.scheme", "hash"}, 18602},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) { runKill9(t, res, v.name, v.extra, v.port) })
	}
}

func runKill9(t *testing.T, res *result, variant string, extra []string, port int) {
	key := func(k string) string { return variant + "." + k }
	opts := nodeproc.DevOpts{Datadir: t.TempDir(), HTTPPort: port, Period: 2, Extra: extra}

	p := startNode(t, gethBin, variant+"-run1", opts)
	res.set(t, key("readyFreshSeconds"), readyAfter(t, p).Seconds())
	c := dial(t, opts.URL())

	// --- flood in the background, remember every accepted transaction ---
	var (
		mu     sync.Mutex
		hashes []common.Hash
	)
	floodCtx, stopFlood := context.WithCancel(context.Background())
	floodDone := make(chan struct{})
	go func() {
		defer close(floodDone)
		_, _ = loadgen.Run(floodCtx, loadgen.Config{
			RPC: opts.URL(), Senders: 4, Txs: 6000, Rate: 300, Drain: time.Second, Seed: "c1-" + variant,
			OnSent: func(s loadgen.SentInfo) { mu.Lock(); hashes = append(hashes, s.Hash); mu.Unlock() },
		})
	}()

	// --- wait for a realistic situation: blocks were sealed AND a backlog is pending ---
	deadline := time.Now().Add(60 * time.Second)
	for {
		head, _ := blockNumber(c)
		st, _ := txpoolStatus(c)
		mu.Lock()
		n := len(hashes)
		mu.Unlock()
		if head >= 5 && st.Pending >= 200 && n >= 1000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("flood did not reach the expected state (head=%d pending=%d accepted=%d)", head, st.Pending, n)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// --- last look (what is canonical right now), then pull the plug ---
	headBefore, _ := blockNumber(c)
	preMined, err := canonicalTxs(c, headBefore)
	if err != nil {
		t.Fatal(err)
	}
	stBefore, _ := txpoolStatus(c)
	killAt := time.Now()
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	stopFlood()
	<-floodDone
	mu.Lock()
	accepted := append([]common.Hash(nil), hashes...)
	mu.Unlock()
	res.set(t, key("headBeforeKill"), headBefore)
	res.set(t, key("txsMinedBeforeKill"), len(preMined))
	res.set(t, key("txpoolPendingBeforeKill"), stBefore.Pending)
	res.set(t, key("txsAcceptedByNode"), len(accepted))

	// --- restart on the same datadir ---
	p2 := startNode(t, gethBin, variant+"-run2", opts)
	ready := readyAfter(t, p2)
	res.set(t, key("restartReadySeconds"), ready.Seconds())
	res.set(t, key("downtimeSecondsKillToReady"), time.Since(killAt).Seconds())
	c2 := dial(t, opts.URL())
	headAfter, _ := blockNumber(c2)
	res.set(t, key("headAfterRestart"), headAfter)
	res.set(t, key("blocksLostByRollback"), int64(headBefore)-int64(headAfter))

	// Is the node FUNCTIONAL, not just answering? Read state, then mine one transaction.
	var bal hexutil.Big
	balErr := c2.Call(&bal, "eth_getBalance", devnet.DevAddress(), "latest")
	res.set(t, key("getBalanceAfterRestart"), errOrOK(balErr))
	mined, mineErr := mineOne(c2, opts.URL(), 20*time.Second)
	res.set(t, key("canMineNewTxAfterRestart"), mined)
	res.set(t, key("mineNewTxError"), errString(mineErr))
	// A second transaction a few seconds later: does block production CONTINUE?
	time.Sleep(5 * time.Second)
	mined2, mineErr2 := mineOne(c2, opts.URL(), 15*time.Second)
	res.set(t, key("canMineSecondTxAfterRestart"), mined2)
	res.set(t, key("mineSecondTxError"), errString(mineErr2))

	// --- settle, then account for every accepted transaction using the canonical chain ---
	// The local-transaction tracker re-injects journaled txs 10 s after start
	// (core/txpool/locals/tx_tracker.go), so wait for the pool to fill and drain again,
	// or for 25 s without any sign of it.
	settleStart := time.Now()
	sawPending := false
	var reinjectedAfter float64
	for time.Since(settleStart) < 90*time.Second {
		st, _ := txpoolStatus(c2)
		if st.Pending > 0 && !sawPending {
			sawPending = true
			reinjectedAfter = time.Since(settleStart).Seconds()
		}
		if sawPending && st.Pending == 0 {
			break
		}
		if !sawPending && time.Since(settleStart) > 25*time.Second {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	res.set(t, key("journalReinjectedSecondsAfterReady"), reinjectedAfter)
	time.Sleep(3 * time.Second)
	headFinal, _ := blockNumber(c2)
	postMined, _ := canonicalTxs(c2, headFinal)
	stFinal, _ := txpoolStatus(c2)
	var survivedMined, lostMined, recoveredPooled, lostPooled int
	for _, h := range accepted {
		_, wasMined := preMined[h]
		_, isMined := postMined[h]
		switch {
		case wasMined && isMined:
			survivedMined++
		case wasMined && !isMined:
			lostMined++
		case !wasMined && isMined:
			recoveredPooled++
		default:
			lostPooled++
		}
	}
	res.set(t, key("headFinal"), headFinal)
	res.set(t, key("txpoolPendingFinal"), stFinal.Pending)
	res.set(t, key("minedBefore_andStillCanonical"), survivedMined)
	res.set(t, key("minedBefore_butLost"), lostMined)
	res.set(t, key("poolOnlyBefore_recoveredAndMined"), recoveredPooled)
	res.set(t, key("poolOnlyBefore_lost"), lostPooled)
	res.set(t, key("restartLogHighlights"), logLines(logPathFor(t, variant+"-run2"),
		"Loaded local transaction journal", "Head state missing", "Rewind", "nclean", "Switching from", "Ignoring payload", "Genesis block reached", "Loaded most recent local block", "Ignoring payload", "State scheme"))
}

func errOrOK(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// canonicalTxs lists the transaction hashes of canonical blocks 1..head.
func canonicalTxs(c *rpc.Client, head uint64) (map[common.Hash]uint64, error) {
	out := map[common.Hash]uint64{}
	for n := uint64(1); n <= head; n++ {
		var b struct {
			Transactions []common.Hash `json:"transactions"`
		}
		if err := c.Call(&b, "eth_getBlockByNumber", hexutil.EncodeUint64(n), false); err != nil {
			return out, err
		}
		for _, h := range b.Transactions {
			out[h] = n
		}
	}
	return out, nil
}

// mineOne sends one dev-account transfer and waits for its receipt.
func mineOne(c *rpc.Client, url string, timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cl, err := devnet.Dial(url)
	if err != nil {
		return false, err
	}
	defer cl.Close()
	nonce, err := cl.PendingNonceAt(ctx, devnet.DevAddress())
	if err != nil {
		return false, fmt.Errorf("nonce: %w", err)
	}
	to := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	tip, fc, err := devnet.SuggestFees(ctx, cl, hexBig(2e9))
	if err != nil {
		return false, fmt.Errorf("fees: %w", err)
	}
	tx, err := devnet.SignDynamic(devnet.DevKey(), nonce, &to, hexBig(1), 21000, tip, fc, nil)
	if err != nil {
		return false, err
	}
	if err := cl.SendTransaction(ctx, tx); err != nil {
		return false, fmt.Errorf("send: %w", err)
	}
	if _, err := devnet.WaitReceipt(ctx, cl, tx.Hash()); err != nil {
		return false, fmt.Errorf("receipt: %w", err)
	}
	return true, nil
}
