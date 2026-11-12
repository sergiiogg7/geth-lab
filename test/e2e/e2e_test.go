//go:build e2e

// Package e2e starts the real labnode binary and exercises the lab_ namespace,
// the gasProfile tracer and loadgen against it.
//
// Run: go test -tags e2e -count=1 -v ./test/e2e
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/sergiiogg7/geth-lab/internal/devnet"
	"github.com/sergiiogg7/geth-lab/internal/loadgen"
	"github.com/sergiiogg7/geth-lab/internal/nodeproc"
)

func TestLabnodeEndToEnd(t *testing.T) {
	wd, _ := os.Getwd()
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	bin := filepath.Join(root, "out", "bin", "labnode"+exe)
	b := exec.Command("go", "build", "-p", "2", "-o", bin, "./cmd/labnode")
	b.Dir = root
	if out, err := b.CombinedOutput(); err != nil {
		t.Fatalf("build labnode: %v\n%s", err, out)
	}

	opts := nodeproc.DevOpts{HTTPPort: 18701}
	p, err := nodeproc.Start(bin, opts, filepath.Join(t.TempDir(), "labnode.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })
	if _, err := p.WaitReady(60 * time.Second); err != nil {
		t.Fatal(err)
	}
	c, err := rpc.Dial(opts.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// lab_nodeInfo
	var info map[string]any
	if err := c.Call(&info, "lab_nodeInfo"); err != nil {
		t.Fatalf("lab_nodeInfo: %v", err)
	}
	if info["chainId"].(float64) != devnet.ChainID {
		t.Fatalf("chainId = %v", info["chainId"])
	}

	// Deploy the counter, call it twice.
	cl, _ := devnet.Dial(opts.URL())
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dev := devnet.DevKey()
	tip, fc, _ := devnet.SuggestFees(ctx, cl, common.Big1)
	deploy, _ := devnet.SignDynamic(dev, 0, nil, common.Big0, 300_000, tip, fc, devnet.CounterInitCode)
	if err := cl.SendTransaction(ctx, deploy); err != nil {
		t.Fatal(err)
	}
	if _, err := devnet.WaitReceipt(ctx, cl, deploy.Hash()); err != nil {
		t.Fatal(err)
	}
	counter := crypto.CreateAddress(devnet.DevAddress(), 0)
	call, _ := devnet.SignDynamic(dev, 1, &counter, common.Big0, 300_000, tip, fc, nil)
	if err := cl.SendTransaction(ctx, call); err != nil {
		t.Fatal(err)
	}
	if _, err := devnet.WaitReceipt(ctx, cl, call.Hash()); err != nil {
		t.Fatal(err)
	}

	// lab_gasProfile and debug_traceTransaction with the custom tracer must agree.
	var viaLab, viaDebug map[string]any
	if err := c.Call(&viaLab, "lab_gasProfile", call.Hash()); err != nil {
		t.Fatalf("lab_gasProfile: %v", err)
	}
	if err := c.Call(&viaDebug, "debug_traceTransaction", call.Hash(), map[string]string{"tracer": "gasProfile"}); err != nil {
		t.Fatalf("debug_traceTransaction: %v", err)
	}
	a, _ := json.Marshal(viaLab)
	bb, _ := json.Marshal(viaDebug)
	if string(a) != string(bb) {
		t.Fatalf("lab_gasProfile and debug_traceTransaction differ:\n%s\n%s", a, bb)
	}
	if viaLab["storageWrites"].(float64) != 1 || viaLab["steps"].(float64) != 7 {
		t.Fatalf("unexpected profile: %s", a)
	}
	t.Logf("gasProfile: %s", a)

	// lab_blockFill and lab_txpoolStats
	var fill []map[string]any
	if err := c.Call(&fill, "lab_blockFill", hexutil.Uint64(3)); err != nil || len(fill) != 3 {
		t.Fatalf("lab_blockFill: err=%v len=%d", err, len(fill))
	}
	var ps map[string]any
	if err := c.Call(&ps, "lab_txpoolStats"); err != nil {
		t.Fatal(err)
	}

	// loadgen against labnode: contract mode, small run.
	rep, err := loadgen.Run(ctx, loadgen.Config{RPC: opts.URL(), Senders: 3, Txs: 60, Mode: "contract", DataBytes: 32, Seed: "e2e", Drain: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + rep.Format())
	if rep.Accepted != 60 || rep.Included != 60 {
		t.Fatalf("expected 60/60 accepted/included, got %d/%d (%v)", rep.Accepted, rep.Included, rep.Errors)
	}

	// Graceful shutdown over RPC.
	var ok bool
	if err := c.Call(&ok, "lab_shutdown"); err != nil || !ok {
		t.Fatalf("lab_shutdown: %v %v", ok, err)
	}
	select {
	case <-p.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("labnode did not exit after lab_shutdown")
	}
	_ = fmt.Sprint()
}
