package gasprofile

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
)

// runCounter executes the lab's counter contract runtime code
// (PUSH0 SLOAD PUSH1 1 ADD PUSH0 SSTORE STOP) under the tracer, without any
// node: the EVM runtime package builds an in-memory state for us.
func runCounter(t *testing.T) Result {
	t.Helper()
	tr := New()
	code := common.FromHex("0x5f546001015f5500")
	cfg := &runtime.Config{EVMConfig: vm.Config{Tracer: tr.Hooks}}
	if _, _, err := runtime.Execute(code, nil, cfg); err != nil {
		t.Fatalf("execute: %v", err)
	}
	raw, err := tr.GetResult()
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return res
}

func TestCounterProfile(t *testing.T) {
	res := runCounter(t)
	if res.Steps != 7 { // PUSH0 SLOAD PUSH1 ADD PUSH0 SSTORE STOP
		t.Fatalf("steps = %d, want 7", res.Steps)
	}
	if res.StorageWrites != 1 {
		t.Fatalf("storageWrites = %d, want 1", res.StorageWrites)
	}
	if len(res.Ops) == 0 || res.Ops[0].Op != "SSTORE" {
		t.Fatalf("the most expensive opcode should be SSTORE, got %+v", res.Ops)
	}
	// SSTORE of a fresh slot (0 -> 1) dominates: storage must be the biggest bucket.
	for cat, gas := range res.ByCategory {
		if cat != "storage" && gas >= res.ByCategory["storage"] {
			t.Fatalf("category %s (%d gas) should not outweigh storage (%d)", cat, gas, res.ByCategory["storage"])
		}
	}
}

func TestCategoryTable(t *testing.T) {
	cases := map[vm.OpCode]string{
		vm.SSTORE: "storage", vm.CALL: "calls", vm.LOG2: "logs",
		vm.KECCAK256: "hashing", vm.MSTORE: "memory", vm.ADD: "compute",
	}
	for op, want := range cases {
		if got := category(op); got != want {
			t.Errorf("category(%s) = %s, want %s", op, got, want)
		}
	}
}
