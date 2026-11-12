//go:build chaos

package chaos

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sergiiogg7/geth-lab/internal/nodeproc"
)

// C6: abuse the JSON-RPC server and see which limits hold.
//
// Hypothesis (from `geth --help` defaults): batches above 1000 items are
// refused, bodies above 5 MB are refused (HTTP 413), a client that never
// finishes its request is cut off by the HTTP read timeout (30 s), and none of
// this disturbs other clients.
func TestC6_RPCAbuse(t *testing.T) {
	res := newResult(t, "C6-rpc-abuse")
	opts := nodeproc.DevOpts{HTTPPort: 18651, MetricsPort: 16651}
	p := startNode(t, gethBin, "defaults", opts)
	readyAfter(t, p)
	url := opts.URL()
	before, _ := scrapeMetrics(opts.MetricsPort)

	post := func(body []byte) (int, []byte, time.Duration, error) {
		start := time.Now()
		resp, err := (&http.Client{Timeout: 60 * time.Second}).Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return 0, nil, time.Since(start), err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b, time.Since(start), nil
	}
	batch := func(n int, method, params string) []byte {
		var sb strings.Builder
		sb.WriteString("[")
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, `{"jsonrpc":"2.0","id":%d,"method":"%s","params":%s}`, i, method, params)
		}
		sb.WriteString("]")
		return []byte(sb.String())
	}
	short := func(b []byte) string {
		s := string(b)
		if len(s) > 160 {
			s = s[:160] + "..."
		}
		return s
	}

	// 1. batch size limit (default --rpc.batch-request-limit 1000)
	for _, n := range []int{1000, 1001} {
		code, body, d, err := post(batch(n, "eth_blockNumber", "[]"))
		res.set(t, fmt.Sprintf("batch%d.httpStatus", n), code)
		res.set(t, fmt.Sprintf("batch%d.seconds", n), d.Seconds())
		res.set(t, fmt.Sprintf("batch%d.bytesBack", n), len(body))
		res.set(t, fmt.Sprintf("batch%d.bodyStart", n), short(body))
		res.set(t, fmt.Sprintf("batch%d.clientError", n), errString(err))
	}

	// 2. request body limit (default --rpc.http-body-limit 5 MB)
	for _, mb := range []int{4, 6} {
		pad := strings.Repeat("a", mb*1024*1024)
		body := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"web3_sha3","params":["0x%s"]}`, pad))
		code, resp, d, err := post(body)
		res.set(t, fmt.Sprintf("body%dMB.httpStatus", mb), code)
		res.set(t, fmt.Sprintf("body%dMB.seconds", mb), d.Seconds())
		res.set(t, fmt.Sprintf("body%dMB.responseStart", mb), short(resp))
		res.set(t, fmt.Sprintf("body%dMB.clientError", mb), errString(err))
	}

	// 3. CPU-bound call: eth_call with an infinite loop (JUMPDEST PUSH0 JUMP) via state override.
	loop := `{"jsonrpc":"2.0","id":1,"method":"eth_call","params":[{"to":"0x00000000000000000000000000000000000000aa"},"latest",{"0x00000000000000000000000000000000000000aa":{"code":"0x5b5f56"}}]}`
	code, resp, d, err := post([]byte(loop))
	res.set(t, "infiniteLoopCall.httpStatus", code)
	res.set(t, "infiniteLoopCall.seconds", d.Seconds())
	res.set(t, "infiniteLoopCall.responseStart", short(resp))
	res.set(t, "infiniteLoopCall.clientError", errString(err))

	// 4. 400 idle TCP connections, then a normal request.
	var conns []net.Conn
	for i := 0; i < 400; i++ {
		cn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", opts.HTTPPort), 2*time.Second)
		if err != nil {
			res.set(t, "idleConns.dialErrorAt", i)
			break
		}
		conns = append(conns, cn)
	}
	res.set(t, "idleConns.opened", len(conns))
	code, _, d, err = post([]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`))
	res.set(t, "idleConns.normalRequestStatus", code)
	res.set(t, "idleConns.normalRequestMillis", d.Milliseconds())
	res.set(t, "idleConns.normalRequestError", errString(err))
	for _, cn := range conns {
		cn.Close()
	}

	// 5. slow client: send half a request and wait. Meanwhile 20 normal requests must work.
	cn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", opts.HTTPPort))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = cn.Write([]byte("POST / HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"jsonrpc\":"))
	slowStart := time.Now()
	var wg sync.WaitGroup
	var okReq, badReq int
	var mu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			c, _, _, err := post([]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`))
			mu.Lock()
			if err == nil && c == 200 {
				okReq++
			} else {
				badReq++
			}
			mu.Unlock()
			time.Sleep(time.Second)
		}
	}()
	_ = cn.SetReadDeadline(time.Now().Add(45 * time.Second))
	buf := make([]byte, 512)
	n, rerr := cn.Read(buf)
	closedAfter := time.Since(slowStart)
	res.set(t, "slowClient.serverClosedAfterSeconds", closedAfter.Seconds())
	res.set(t, "slowClient.serverReply", short(buf[:n]))
	res.set(t, "slowClient.readError", errString(rerr))
	cn.Close()
	wg.Wait()
	res.set(t, "slowClient.otherRequestsOK", okReq)
	res.set(t, "slowClient.otherRequestsFailed", badReq)

	// 6. is the node still healthy, and what did the server count?
	code, _, _, _ = post([]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`))
	after, _ := scrapeMetrics(opts.MetricsPort)
	res.set(t, "finalHealthCheckStatus", code)
	res.set(t, "metricsDelta", metricsDelta(before, after, "rpc_requests", "rpc_success", "rpc_failure"))
	if code != 200 {
		t.Errorf("node unhealthy after abuse: %d", code)
	}

	// 7. second node with a small batch RESPONSE limit: the server truncates instead of exhausting memory.
	if ok, _ := p.Interrupt(30 * time.Second); !ok {
		_ = p.Kill()
	}
	opts2 := nodeproc.DevOpts{HTTPPort: 18652, Extra: []string{"--rpc.batch-response-max-size", "3000"}}
	p2 := startNode(t, gethBin, "small-response-limit", opts2)
	readyAfter(t, p2)
	url = opts2.URL()
	code, body, _, _ := post(batch(100, "eth_getBlockByNumber", `["0x0",true]`))
	res.set(t, "smallResponseLimit.httpStatus", code)
	res.set(t, "smallResponseLimit.bytesBack", len(body))
	res.set(t, "smallResponseLimit.bodyEnd", short(body[max(0, len(body)-170):]))
	res.set(t, "smallResponseLimit.itemsContainingError", strings.Count(string(body), `"error"`))
}
