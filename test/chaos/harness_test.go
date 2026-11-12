//go:build chaos

// Package chaos holds the failure scenarios C1..C6. They start REAL node
// processes (geth, labnode), break them on purpose and write the observations
// to out/chaos/<scenario>.json. The postmortems in docs/postmortems/ quote
// those files.
//
// Run:  go test -tags chaos -count=1 -v -timeout 20m ./test/chaos -run 'TestC1'
//
// Requirements: a geth binary (GETH_BIN or PATH or GOPATH/bin) and the lab
// tools, which TestMain builds into out/bin. One node runs at a time.
package chaos

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/sergiiogg7/geth-lab/internal/nodeproc"
)

var (
	repoRoot string
	outDir   string
	gethBin  string
	labBin   string
	watchBin string
)

func TestMain(m *testing.M) {
	wd, _ := os.Getwd()
	repoRoot = filepath.Clean(filepath.Join(wd, "..", ".."))
	outDir = filepath.Join(repoRoot, "out", "chaos")
	_ = os.RemoveAll(filepath.Join(outDir, "logs"))
	_ = os.MkdirAll(filepath.Join(outDir, "logs"), 0o755)
	binDir := filepath.Join(repoRoot, "out", "bin")
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	build := exec.Command("go", "build", "-p", "2", "-o", binDir+string(os.PathSeparator), "./cmd/labnode", "./cmd/watch")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building lab tools failed: %v\n%s", err, out)
		os.Exit(1)
	}
	labBin, watchBin = filepath.Join(binDir, "labnode"+exe), filepath.Join(binDir, "watch"+exe)
	gethBin = os.Getenv("GETH_BIN")
	if gethBin == "" {
		if p, err := exec.LookPath("geth"); err == nil {
			gethBin = p
		} else if gp, err := exec.Command("go", "env", "GOPATH").Output(); err == nil {
			gethBin = filepath.Join(strings.TrimSpace(string(gp)), "bin", "geth"+exe)
		}
	}
	if _, err := os.Stat(gethBin); err != nil {
		fmt.Fprintf(os.Stderr, "geth binary not found (%q); set GETH_BIN\n", gethBin)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// ---- helpers ----------------------------------------------------------------

// node starts a node and registers cleanup that kills it.
func startNode(t *testing.T, binary, label string, opts nodeproc.DevOpts) *nodeproc.Proc {
	t.Helper()
	logPath := logPathFor(t, label)
	p, err := nodeproc.Start(binary, opts, logPath)
	if err != nil {
		t.Fatalf("start %s: %v", binary, err)
	}
	t.Cleanup(func() {
		select {
		case <-p.Done():
		default:
			_ = p.Kill()
		}
	})
	return p
}

// readyAfter returns how long the node needed until JSON-RPC answered.
func readyAfter(t *testing.T, p *nodeproc.Proc) time.Duration {
	t.Helper()
	d, err := p.WaitReady(90 * time.Second)
	if err != nil {
		t.Fatalf("node not ready: %v", err)
	}
	return d
}

func dial(t *testing.T, url string) *rpc.Client {
	t.Helper()
	c, err := rpc.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

type poolStatus struct{ Pending, Queued uint64 }

func txpoolStatus(c *rpc.Client) (poolStatus, error) {
	var m map[string]hexutil.Uint64
	if err := c.Call(&m, "txpool_status"); err != nil {
		return poolStatus{}, err
	}
	return poolStatus{uint64(m["pending"]), uint64(m["queued"])}, nil
}

func blockNumber(c *rpc.Client) (uint64, error) {
	var n hexutil.Uint64
	err := c.Call(&n, "eth_blockNumber")
	return uint64(n), err
}

// txLocation says where a transaction is according to the node.
type txLocation struct {
	BlockNumber *hexutil.Uint64 `json:"blockNumber"`
}

// locate returns "included", "pooled" or "missing". The node answers
// "transaction indexing is in progress" for a while after a start, so we retry.
func locate(c *rpc.Client, h common.Hash) (string, uint64, error) {
	var lastErr error
	for i := 0; i < 600; i++ {
		var tx *txLocation
		err := c.Call(&tx, "eth_getTransactionByHash", h)
		if err == nil {
			switch {
			case tx == nil:
				return "missing", 0, nil
			case tx.BlockNumber == nil:
				return "pooled", 0, nil
			default:
				return "included", uint64(*tx.BlockNumber), nil
			}
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return "", 0, lastErr
}

// result accumulates observations and saves them as JSON at the end.
type result struct {
	mu   sync.Mutex
	name string
	data map[string]any
}

func newResult(t *testing.T, name string) *result {
	r := &result{name: name, data: map[string]any{"scenario": name, "date": time.Now().Format(time.RFC3339), "os": runtime.GOOS}}
	t.Cleanup(func() { r.save(t) })
	return r
}

func (r *result) set(t *testing.T, k string, v any) {
	r.mu.Lock()
	r.data[k] = v
	r.mu.Unlock()
	t.Logf("%-34s %v", k, v)
}

func (r *result) save(t *testing.T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, _ := json.MarshalIndent(r.data, "", "  ")
	_ = os.WriteFile(filepath.Join(outDir, r.name+".json"), b, 0o644)
}

// logLines returns the lines of a node log that contain any of the needles.
func logLines(path string, needles ...string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		for _, n := range needles {
			if strings.Contains(ln, n) {
				if len(ln) > 220 {
					ln = ln[:220]
				}
				out = append(out, strings.TrimSpace(ln))
				break
			}
		}
	}
	return out
}

func ctx30() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// hexBig converts an int64 to *big.Int (short helper for fees and values).
func hexBig(v int64) *big.Int { return big.NewInt(v) }

// logPathFor names the node log of a test: out/chaos/logs/<test>-<label>.log
func logPathFor(t *testing.T, label string) string {
	return filepath.Join(outDir, "logs", fmt.Sprintf("%s-%s.log", strings.ReplaceAll(t.Name(), "/", "_"), label))
}

// scrapeMetrics reads the node's Prometheus endpoint and returns the samples
// that have no labels (name -> value). Good enough for gauges and counters.
func scrapeMetrics(port int) (map[string]float64, error) {
	// A timeout is essential: a node that crashed in the middle of its exit (C7) keeps its listening socket but never answers.
	hc := &http.Client{Timeout: 3 * time.Second}
	resp, err := hc.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/metrics/prometheus", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]float64{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		ln := sc.Text()
		if ln == "" || ln[0] == '#' || strings.ContainsAny(ln, "{") {
			continue
		}
		f := strings.Fields(ln)
		if len(f) == 2 {
			if v, err := strconv.ParseFloat(f[1], 64); err == nil {
				out[f[0]] = v
			}
		}
	}
	return out, sc.Err()
}

// metricsDelta returns after-before for the given names (missing = 0).
func metricsDelta(before, after map[string]float64, names ...string) map[string]float64 {
	d := map[string]float64{}
	for _, n := range names {
		d[n] = after[n] - before[n]
	}
	return d
}

func ctx30bg() context.Context { return context.Background() }
