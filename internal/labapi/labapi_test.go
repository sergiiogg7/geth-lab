package labapi

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func tx(nonce uint64, tip int64) *types.Transaction {
	return types.NewTx(&types.DynamicFeeTx{Nonce: nonce, GasTipCap: big.NewInt(tip), GasFeeCap: big.NewInt(tip + 100), Gas: 21000, Data: make([]byte, 10)})
}

func TestSummarizePool(t *testing.T) {
	a, b := common.HexToAddress("0xa"), common.HexToAddress("0xb")
	pending := map[common.Address][]*types.Transaction{a: {tx(0, 5), tx(1, 9)}, b: {tx(0, 2)}}
	queued := map[common.Address][]*types.Transaction{b: {tx(5, 7), tx(6, 7)}}
	s := SummarizePool(pending, queued)
	if s.Pending != 3 || s.Queued != 2 || s.PendingAccounts != 2 || s.QueuedAccounts != 1 {
		t.Fatalf("counts wrong: %+v", s)
	}
	if s.MinTipWei != "2" || s.MaxTipWei != "9" || s.LargestAccount != 2 {
		t.Fatalf("tips/largest wrong: %+v", s)
	}
	if s.PendingBytes == 0 {
		t.Fatal("pending bytes should be > 0")
	}
}

func TestSummarizeEmptyPool(t *testing.T) {
	s := SummarizePool(nil, nil)
	if s.Pending != 0 || s.MinTipWei != "0" || s.MaxTipWei != "0" {
		t.Fatalf("empty pool summary wrong: %+v", s)
	}
}

func TestNewBlockFill(t *testing.T) {
	h := &types.Header{Number: big.NewInt(7), GasLimit: 1000, GasUsed: 250, Time: 110, BaseFee: big.NewInt(1000)}
	b := types.NewBlockWithHeader(h)
	f := NewBlockFill(b, 100)
	if f.Number != 7 || f.FillPercent != 25 || f.IntervalSecs != 10 || f.BaseFeeWei != "1000" {
		t.Fatalf("unexpected fill: %+v", f)
	}
}
