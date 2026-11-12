// Package nodeproc starts, stops and kills a geth (or labnode) child process.
// It is the harness behind the chaos scenarios: a test needs to be able to
// "pull the plug" (hard kill) and to "stop politely" (interrupt), and to know
// how long the node needed to come back.
//
// Java analogy: a tiny ProcessBuilder wrapper with a waitFor() in a goroutine.
package nodeproc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/sergiiogg7/geth-lab/internal/devnet"
)

// DevOpts are the node options the lab cares about. The same flag names are
// understood by the real geth binary and by labnode (cmd/labnode).
type DevOpts struct {
	Datadir     string // "" = in-memory chain (geth --dev default)
	HTTPPort    int
	MetricsPort int // 0 = metrics off
	Period      int // --dev.period, seconds; 0 = seal a block when a tx arrives
	GasLimit    uint64
	Cache       int    // --cache (MB), 0 = default
	GCMode      string // "full" or "archive", "" = default
	StateScheme string // "path" or "hash", "" = default
	TxPool      map[string]int
	Verbosity   int
	Extra       []string

	// MemLimitMB, when > 0, puts the process in a Windows Job Object that caps its committed memory (see MemLimit).
	// It is not a node flag: the node does not know it is capped. Start refuses to run the node if the cap cannot be applied.
	MemLimitMB int
}

// Args renders the flags. TxPool keys are the suffix after "--txpool.".
func (o DevOpts) Args() []string {
	a := []string{"--dev", "--http", "--http.addr", "127.0.0.1", "--http.port", strconv.Itoa(o.HTTPPort),
		"--http.api", "eth,net,web3,txpool,debug,admin", "--ipcdisable"}
	if o.Datadir != "" {
		a = append(a, "--datadir", o.Datadir)
	}
	if o.MetricsPort != 0 {
		a = append(a, "--metrics", "--metrics.addr", "127.0.0.1", "--metrics.port", strconv.Itoa(o.MetricsPort))
	}
	if o.Period != 0 {
		a = append(a, "--dev.period", strconv.Itoa(o.Period))
	}
	if o.GasLimit != 0 {
		a = append(a, "--dev.gaslimit", strconv.FormatUint(o.GasLimit, 10))
	}
	if o.Cache != 0 {
		a = append(a, "--cache", strconv.Itoa(o.Cache))
	}
	if o.GCMode != "" {
		a = append(a, "--gcmode", o.GCMode)
	}
	if o.StateScheme != "" {
		a = append(a, "--state.scheme", o.StateScheme)
	}
	for k, v := range o.TxPool {
		a = append(a, "--txpool."+k, strconv.Itoa(v))
	}
	if o.Verbosity != 0 {
		a = append(a, "--verbosity", strconv.Itoa(o.Verbosity))
	}
	return append(a, o.Extra...)
}

// URL is the HTTP JSON-RPC endpoint the node will listen on.
func (o DevOpts) URL() string { return fmt.Sprintf("http://127.0.0.1:%d", o.HTTPPort) }

// Proc is a running child process.
type Proc struct {
	Opts DevOpts
	cmd  *exec.Cmd
	log  *os.File

	done  chan struct{} // closed when the process exits
	limit *MemLimit     // nil unless Opts.MemLimitMB > 0
	mu    sync.Mutex
	err   error // result of cmd.Wait
}

// Start launches binary with the options and appends stdout+stderr to logPath.
func Start(binary string, opts DevOpts, logPath string) (*Proc, error) {
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, opts.Args()...)
	cmd.Stdout, cmd.Stderr = lf, lf
	prepare(cmd) // OS specific: own process group on Windows
	if err := cmd.Start(); err != nil {
		lf.Close()
		return nil, err
	}
	p := &Proc{Opts: opts, cmd: cmd, log: lf, done: make(chan struct{})}
	if opts.MemLimitMB > 0 {
		// Safety first: if the cap cannot be applied the node must not run at all (a capped test must never run uncapped).
		lim, err := LimitMemory(cmd.Process.Pid, opts.MemLimitMB, true)
		if err != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			lf.Close()
			return nil, fmt.Errorf("memory limit: %w", err)
		}
		p.limit = lim
	}
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		lf.Close()
		close(p.done)
	}()
	return p, nil
}

// Pid is the OS process id.
func (p *Proc) Pid() int { return p.cmd.Process.Pid }

// WaitReady blocks until the JSON-RPC endpoint answers and returns the delay.
func (p *Proc) WaitReady(timeout time.Duration) (time.Duration, error) {
	type res struct {
		d   time.Duration
		err error
	}
	ch := make(chan res, 1)
	go func() { d, err := devnet.WaitReady(p.Opts.URL(), timeout); ch <- res{d, err} }()
	select {
	case r := <-ch:
		return r.d, r.err
	case <-p.done:
		return 0, fmt.Errorf("process exited before becoming ready: %v", p.Err())
	}
}

// Kill is the hard stop: TerminateProcess on Windows, SIGKILL elsewhere.
// The process gets no chance to flush anything. This is "kill -9".
func (p *Proc) Kill() error {
	err := p.cmd.Process.Kill()
	<-p.done
	return err
}

// Interrupt is the polite stop (Ctrl-Break on Windows, SIGINT elsewhere). It
// returns once the process has exited or the timeout expired (then false).
func (p *Proc) Interrupt(timeout time.Duration) (bool, error) {
	if err := interrupt(p.cmd); err != nil {
		return false, err
	}
	select {
	case <-p.done:
		return true, nil
	case <-time.After(timeout):
		return false, nil
	}
}

// Done is closed when the process has exited.
func (p *Proc) Done() <-chan struct{} { return p.done }

// Err returns cmd.Wait's error (nil = exit code 0). Only valid after Done.
func (p *Proc) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// MemPeakBytes is the peak committed memory of the node, as measured by its Job Object (0 and false when no limit was set).
// It stays readable after the process has exited.
func (p *Proc) MemPeakBytes() (uint64, bool) {
	if p.limit == nil {
		return 0, false
	}
	b, err := p.limit.PeakBytes()
	return b, err == nil
}

// Exited reports whether the process has ended, and its exit code (only meaningful when true).
func (p *Proc) Exited() (bool, int) {
	select {
	case <-p.done:
		if p.cmd.ProcessState != nil {
			return true, p.cmd.ProcessState.ExitCode()
		}
		return true, -1
	default:
		return false, 0
	}
}
