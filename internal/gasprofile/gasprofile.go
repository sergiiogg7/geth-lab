// Package gasprofile is a native Geth tracer built on core/tracing.Hooks.
//
// It answers the question "where did the gas of this transaction go?": per
// opcode (count and gas), per category (storage, memory, calls, ...), the
// call tree shape (frames, max depth, reverts) and the total.
//
// How a tracer plugs into Geth (v1.17.10):
//
//  1. A tracer is a struct holding a *tracing.Hooks (callbacks the EVM calls
//     while it executes: OnTxStart, OnEnter, OnOpcode, OnExit, OnTxEnd, ...).
//     Java analogy: a listener interface with default no-op methods; the EVM
//     is the event source and every callback is a nullable function field.
//  2. It registers a constructor under a name in tracers.DefaultDirectory
//     (eth/tracers/dir.go). That is the same mechanism Geth uses for its own
//     "callTracer", "prestateTracer", "4byteTracer", ... (see eth/tracers/native).
//  3. After that, `debug_traceTransaction(hash, {"tracer":"gasProfile"})`
//     (and debug_traceCall, debug_traceBlockByNumber, ...) work with it. The
//     tracers API re-executes the transaction on top of the parent state with
//     the hooks installed. Nothing in consensus code is touched.
//
// The tracer is only compiled into binaries that import this package (blank
// import in cmd/labnode). The stock geth binary does not know it.
package gasprofile

import (
	"encoding/json"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/params"
)

// Name is the tracer name used in the "tracer" field of the trace config.
const Name = "gasProfile"

func init() {
	tracers.DefaultDirectory.Register(Name, func(_ *tracers.Context, _ json.RawMessage, _ *params.ChainConfig) (*tracers.Tracer, error) {
		return New(), nil
	}, false)
}

// OpStat aggregates one opcode.
type OpStat struct {
	Op       string `json:"op"`
	Count    uint64 `json:"count"`
	Gas      uint64 `json:"gas"`      // execution-dimension gas
	StateGas uint64 `json:"stateGas"` // state-dimension gas (EIP-8037); 0 on pre-Amsterdam rules
}

// Result is the JSON document returned to the RPC caller.
type Result struct {
	GasUsed       uint64            `json:"gasUsed"`       // from the receipt (includes intrinsic gas and refunds)
	IntrinsicGas  uint64            `json:"intrinsicGas"`  // approx: GasUsed minus summed opcode costs (21000 + calldata, minus refunds), clamped at 0
	Failed        bool              `json:"failed"`        // top-level frame reverted/failed
	Steps         uint64            `json:"steps"`         // opcodes executed
	Frames        uint64            `json:"frames"`        // call frames entered (top level counts as 1)
	MaxDepth      int               `json:"maxDepth"`      // deepest call depth reached (top level = 0)
	Reverts       uint64            `json:"reverts"`       // frames that ended reverted
	StorageWrites uint64            `json:"storageWrites"` // SSTORE executions
	ExecutionGas  uint64            `json:"executionGas"`  // sum of per-opcode execution gas
	StateGas      uint64            `json:"stateGas"`      // sum of per-opcode state gas
	ByCategory    map[string]uint64 `json:"gasByCategory"` // execution + state, per category
	Ops           []OpStat          `json:"ops"`           // sorted by execution+state gas, then count
}

// profiler is the tracer state. Hooks run single-threaded for one transaction,
// so plain fields are fine (no mutex).
type profiler struct {
	counts [256]uint64
	gas    [256]uint64 // execution-dimension gas per opcode
	state  [256]uint64 // state-dimension gas per opcode (EIP-8037, Amsterdam+; 0 before)

	steps, frames, reverts, sstores uint64
	maxDepth                        int
	gasUsed                         uint64
	failed                          bool
}

// New builds a fresh profiler wrapped as a tracers.Tracer. Exported so tests
// and other programs can attach the hooks to a vm.Config directly.
func New() *tracers.Tracer {
	p := &profiler{}
	return &tracers.Tracer{
		Hooks: &tracing.Hooks{
			OnOpcodeV2: p.onOpcode,
			OnEnterV2:  p.onEnter,
			OnExitV2:   p.onExit,
			OnTxEnd:    p.onTxEnd,
		},
		GetResult: p.result,
		Stop:      func(error) {}, // runs are short; nothing to interrupt
	}
}

// onOpcode is called before every opcode executes. `cost` is the gas the
// opcode is about to be charged (static + dynamic, and for CALL-like ops it
// includes the gas forwarded to the callee). That is why call opcodes can look
// expensive: the callee's own opcodes are ALSO counted in their own rows.
func (p *profiler) onOpcode(_ uint64, op byte, _, cost tracing.Gas, _ tracing.OpContext, _ []byte, depth int, _ error) {
	p.counts[op]++
	p.gas[op] += cost.Execution
	p.state[op] += cost.State
	p.steps++
	if op == byte(vm.SSTORE) {
		p.sstores++
	}
	if depth > p.maxDepth {
		p.maxDepth = depth
	}
}

func (p *profiler) onEnter(depth int, _ byte, _, _ common.Address, _ []byte, _ tracing.Gas, _ *big.Int) {
	p.frames++
	if depth > p.maxDepth {
		p.maxDepth = depth
	}
}

func (p *profiler) onExit(depth int, _ []byte, _ tracing.Gas, err error, reverted bool) {
	if reverted || err != nil {
		p.reverts++
		if depth == 0 {
			p.failed = true
		}
	}
}

func (p *profiler) onTxEnd(receipt *types.Receipt, _ error) {
	if receipt != nil {
		p.gasUsed = receipt.GasUsed
	}
}

// category maps an opcode to a coarse bucket, so the output answers
// "storage vs compute vs memory" at a glance.
func category(op vm.OpCode) string {
	switch {
	case op == vm.SLOAD || op == vm.SSTORE || op == vm.TLOAD || op == vm.TSTORE:
		return "storage"
	case op == vm.CALL || op == vm.CALLCODE || op == vm.DELEGATECALL || op == vm.STATICCALL ||
		op == vm.CREATE || op == vm.CREATE2:
		return "calls"
	case op >= vm.LOG0 && op <= vm.LOG4:
		return "logs"
	case op == vm.KECCAK256:
		return "hashing"
	case op == vm.MLOAD || op == vm.MSTORE || op == vm.MSTORE8 || op == vm.MCOPY || op == vm.CALLDATACOPY ||
		op == vm.CODECOPY || op == vm.RETURNDATACOPY || op == vm.EXTCODECOPY:
		return "memory"
	case op == vm.BALANCE || op == vm.EXTCODESIZE || op == vm.EXTCODEHASH || op == vm.SELFBALANCE:
		return "account-access"
	default:
		return "compute"
	}
}

func (p *profiler) result() (json.RawMessage, error) {
	res := Result{
		GasUsed: p.gasUsed, Failed: p.failed, Steps: p.steps, Frames: p.frames,
		MaxDepth: p.maxDepth, Reverts: p.reverts, StorageWrites: p.sstores,
		ByCategory: map[string]uint64{},
	}
	for op := 0; op < 256; op++ {
		if p.counts[op] == 0 {
			continue
		}
		o := vm.OpCode(op)
		res.Ops = append(res.Ops, OpStat{Op: o.String(), Count: p.counts[op], Gas: p.gas[op], StateGas: p.state[op]})
		res.ByCategory[category(o)] += p.gas[op] + p.state[op]
		res.ExecutionGas += p.gas[op]
		res.StateGas += p.state[op]
	}
	if sum := res.ExecutionGas + res.StateGas; p.gasUsed > sum {
		res.IntrinsicGas = p.gasUsed - sum
	}
	sort.Slice(res.Ops, func(i, j int) bool {
		gi, gj := res.Ops[i].Gas+res.Ops[i].StateGas, res.Ops[j].Gas+res.Ops[j].StateGas
		if gi != gj {
			return gi > gj
		}
		return res.Ops[i].Count > res.Ops[j].Count
	})
	return json.Marshal(res)
}
