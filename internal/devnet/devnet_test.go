package devnet

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestDevAddressIsTheKnownFaucet(t *testing.T) {
	want := common.HexToAddress("0x71562b71999873DB5b286dF957af199Ec94617F7") // printed by `geth --dev`
	if DevAddress() != want {
		t.Fatalf("dev address = %s, want %s", DevAddress(), want)
	}
}

func TestSenderKeysAreDeterministicAndDistinct(t *testing.T) {
	a1, a2, b := SenderKey("s", 1), SenderKey("s", 1), SenderKey("s", 2)
	if a1.D.Cmp(a2.D) != 0 {
		t.Fatal("same seed and index must give the same key")
	}
	if a1.D.Cmp(b.D) == 0 {
		t.Fatal("different index must give a different key")
	}
	if SenderKey("other", 1).D.Cmp(a1.D) == 0 {
		t.Fatal("different seed must give a different key")
	}
}

func TestIntrinsicGas(t *testing.T) {
	if g := IntrinsicGas(nil); g != 21000 {
		t.Fatalf("empty data: %d", g)
	}
	// 2 zero bytes (4 each) + 3 non-zero bytes (16 each)
	if g := IntrinsicGas([]byte{0, 0, 1, 2, 3}); g != 21000+2*4+3*16 {
		t.Fatalf("mixed data: %d", g)
	}
}

func TestCounterInitCodeShape(t *testing.T) {
	runtime := common.FromHex("0x5f546001015f5500") // PUSH0 SLOAD PUSH1 1 ADD PUSH0 SSTORE STOP
	if !bytes.HasSuffix(CounterInitCode, runtime) {
		t.Fatal("init code must end with the runtime code")
	}
	// constructor copies len(runtime) bytes from offset len(constructor)
	ctor := CounterInitCode[:len(CounterInitCode)-len(runtime)]
	if int(ctor[1]) != len(runtime) || int(ctor[4]) != len(ctor) {
		t.Fatalf("constructor sizes inconsistent: size=%d offset=%d ctorLen=%d", ctor[1], ctor[4], len(ctor))
	}
}
