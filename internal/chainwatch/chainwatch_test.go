package chainwatch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// fakeChain is an in-memory block store acting as the Source.
type fakeChain struct{ byHash map[common.Hash]Header }

func newFake() *fakeChain { return &fakeChain{byHash: map[common.Hash]Header{}} }

func (f *fakeChain) HeaderByHash(_ context.Context, h common.Hash) (Header, error) {
	hd, ok := f.byHash[h]
	if !ok {
		return Header{}, fmt.Errorf("unknown header %s", h)
	}
	return hd, nil
}

// block creates a header on top of parent; salt makes sibling blocks differ.
func (f *fakeChain) block(parent Header, salt string) Header {
	h := Header{Number: parent.Number + 1, ParentHash: parent.Hash, Time: parent.Time + 2, GasLimit: 1000, GasUsed: 100}
	h.Hash = common.BytesToHash(crypto.Keccak256([]byte(fmt.Sprintf("%d/%s/%s", h.Number, parent.Hash, salt))))
	f.byHash[h.Hash] = h
	return h
}

func (f *fakeChain) genesis() Header {
	g := Header{Number: 0, Hash: common.HexToHash("0xabc")}
	f.byHash[g.Hash] = g
	return g
}

func observe(t *testing.T, tr *Tracker, h Header) []Event {
	t.Helper()
	evs, err := tr.Observe(context.Background(), h, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func kinds(evs []Event) []Kind {
	var k []Kind
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return k
}

func TestLinearChain(t *testing.T) {
	f := newFake()
	tr := NewTracker(f)
	g := f.genesis()
	observe(t, tr, g)
	b1 := f.block(g, "a")
	evs := observe(t, tr, b1)
	if len(evs) != 1 || evs[0].Kind != KindBlock || evs[0].BlockTime != 2 {
		t.Fatalf("want one block event with blocktime 2, got %+v", evs)
	}
	if got := observe(t, tr, b1); got != nil {
		t.Fatalf("same head twice must produce no event, got %v", got)
	}
}

func TestSkippedBlocksAreNotAReorg(t *testing.T) {
	f := newFake()
	tr := NewTracker(f)
	g := f.genesis()
	observe(t, tr, g)
	b1 := f.block(g, "a")
	b2 := f.block(b1, "a")
	b3 := f.block(b2, "a")
	evs := observe(t, tr, b3) // poll interval was too long: saw g then b3
	if len(evs) != 1 || evs[0].Kind != KindSkip || evs[0].Added != 2 {
		t.Fatalf("want skip of 2 blocks, got %+v", evs)
	}
	if tr.Reorgs != 0 {
		t.Fatal("skip must not count as a reorg")
	}
}

func TestRewindThenReplacementIsReorg(t *testing.T) {
	f := newFake()
	tr := NewTracker(f)
	g := f.genesis()
	observe(t, tr, g)
	a1 := f.block(g, "a")
	a2 := f.block(a1, "a")
	a3 := f.block(a2, "a")
	for _, b := range []Header{a1, a2, a3} {
		observe(t, tr, b)
	}
	// head goes back to a1 (debug_setHead / lab_fork depth 2)
	evs := observe(t, tr, a1)
	if len(evs) != 1 || evs[0].Kind != KindRewind || evs[0].Depth != 2 {
		t.Fatalf("want rewind depth 2, got %+v", evs)
	}
	// a replacement block at height 2 with a different hash
	b2 := f.block(a1, "b")
	evs = observe(t, tr, b2)
	if len(evs) != 1 || evs[0].Kind != KindReorg || evs[0].Depth != 2 {
		t.Fatalf("want reorg depth 2 (a2,a3 dropped), got %+v", evs)
	}
	// next block is business as usual
	b3 := f.block(b2, "b")
	if evs = observe(t, tr, b3); kinds(evs)[0] != KindBlock {
		t.Fatalf("after the reorg a child must be a plain block, got %v", kinds(evs))
	}
	if tr.Reorgs != 1 || tr.MaxReorgDepth != 2 {
		t.Fatalf("counters: reorgs=%d maxDepth=%d", tr.Reorgs, tr.MaxReorgDepth)
	}
}

func TestDirectBranchSwitch(t *testing.T) {
	// The poller never saw the intermediate state: head a3 -> head b4 (parent chain b1..b3 forks at g).
	f := newFake()
	tr := NewTracker(f)
	g := f.genesis()
	observe(t, tr, g)
	a1 := f.block(g, "a")
	a2 := f.block(a1, "a")
	a3 := f.block(a2, "a")
	for _, b := range []Header{a1, a2, a3} {
		observe(t, tr, b)
	}
	b1 := f.block(g, "b")
	b2 := f.block(b1, "b")
	b3 := f.block(b2, "b")
	b4 := f.block(b3, "b")
	evs := observe(t, tr, b4)
	if len(evs) != 1 || evs[0].Kind != KindReorg {
		t.Fatalf("want a reorg, got %+v", evs)
	}
	e := evs[0]
	if e.Depth != 3 || e.Added != 4 || e.Ancestor != 0 {
		t.Fatalf("depth/added/ancestor = %d/%d/%d, want 3/4/0", e.Depth, e.Added, e.Ancestor)
	}
}

func TestUnknownAncestorIsAnError(t *testing.T) {
	f := newFake()
	tr := NewTracker(f)
	g := f.genesis()
	observe(t, tr, g)
	orphan := Header{Number: 5, Hash: common.HexToHash("0x5"), ParentHash: common.HexToHash("0x4")}
	if _, err := tr.Observe(context.Background(), orphan, time.Now()); err == nil {
		t.Fatal("expected an error when the ancestor cannot be fetched")
	}
}
