// Package chainwatch turns a stream of "current head" observations into
// events: new block, skipped blocks, rewind, REORG, stall.
//
// It is pure logic (no network): the only thing it needs from the outside is
// a Source that can fetch a header by hash, so unit tests can feed it a fake
// chain. cmd/watch plugs the real JSON-RPC client into it.
//
// Vocabulary:
//
//	reorg   the canonical chain switched to a branch that does not contain our
//	        previous head. Depth = how many blocks we had to drop.
//	rewind  the head moved back to a block we already knew to be canonical
//	        (what debug_setHead or lab_fork do right away). If the chain then
//	        grows with DIFFERENT blocks at heights we had seen, that is a reorg
//	        too, and it is reported as such once the first replacement arrives.
package chainwatch

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Header is the subset of a block header the watcher needs.
type Header struct {
	Number     uint64
	Hash       common.Hash
	ParentHash common.Hash
	Time       uint64 // unix seconds, from the header
	GasUsed    uint64
	GasLimit   uint64
	TxCount    int
}

// Source fetches headers the tracker has not seen (ancestors of a new head).
type Source interface {
	HeaderByHash(ctx context.Context, h common.Hash) (Header, error)
}

// Kind classifies an event.
type Kind string

const (
	KindStart  Kind = "start"  // first observation
	KindBlock  Kind = "block"  // head moved to a child of the previous head
	KindSkip   Kind = "skip"   // head jumped over N blocks on the same branch
	KindRewind Kind = "rewind" // head moved back onto a known canonical block
	KindReorg  Kind = "reorg"  // canonical chain switched branch
)

// Event is one thing worth printing or counting.
type Event struct {
	Kind      Kind
	Head      Header
	Prev      Header // previous head (zero on start)
	BlockTime uint64 // seconds between header timestamps (KindBlock)
	Depth     uint64 // blocks dropped (rewind/reorg)
	Added     uint64 // blocks on the new branch (reorg) or skipped (skip)
	Ancestor  uint64 // number of the common ancestor (rewind/reorg)
	At        time.Time
}

func (e Event) String() string {
	switch e.Kind {
	case KindStart:
		return fmt.Sprintf("start  head=#%d %s", e.Head.Number, short(e.Head.Hash))
	case KindBlock:
		return fmt.Sprintf("block  #%d %s txs=%d gas=%d/%d (%.1f%%) blocktime=%ds", e.Head.Number, short(e.Head.Hash),
			e.Head.TxCount, e.Head.GasUsed, e.Head.GasLimit, pct(e.Head.GasUsed, e.Head.GasLimit), e.BlockTime)
	case KindSkip:
		return fmt.Sprintf("skip   head jumped #%d -> #%d (%d blocks not seen, same branch)", e.Prev.Number, e.Head.Number, e.Added)
	case KindRewind:
		return fmt.Sprintf("REWIND head #%d -> #%d (dropped %d blocks, now at known canonical %s)", e.Prev.Number, e.Head.Number, e.Depth, short(e.Head.Hash))
	case KindReorg:
		return fmt.Sprintf("REORG  depth=%d added=%d ancestor=#%d old=#%d %s new=#%d %s", e.Depth, e.Added, e.Ancestor,
			e.Prev.Number, short(e.Prev.Hash), e.Head.Number, short(e.Head.Hash))
	}
	return string(e.Kind)
}

func short(h common.Hash) string { return h.Hex()[:10] }

func pct(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}

// MaxWalkBack bounds how far we walk back looking for a common ancestor.
const MaxWalkBack = 256

// Tracker keeps the canonical chain as the watcher has seen it.
type Tracker struct {
	src   Source
	canon map[uint64]common.Hash // number -> hash on our current canonical view
	seen  map[uint64]common.Hash // number -> hash of the most recent block we ever saw at that height (before the current one)
	head  Header
	have  bool

	// A rewind whose replacement has not arrived yet.
	rewindFrom *Header

	// Counters for the final report.
	Blocks, Reorgs, Rewinds, Skips uint64
	MaxReorgDepth                  uint64
}

// NewTracker builds a tracker that resolves unknown ancestors through src.
func NewTracker(src Source) *Tracker {
	return &Tracker{src: src, canon: map[uint64]common.Hash{}, seen: map[uint64]common.Hash{}}
}

// Head returns the current head (ok=false before the first observation).
func (t *Tracker) Head() (Header, bool) { return t.head, t.have }

// Observe feeds the tracker the node's current head and returns what changed.
func (t *Tracker) Observe(ctx context.Context, h Header, now time.Time) ([]Event, error) {
	if !t.have {
		t.set(h)
		t.have = true
		return []Event{{Kind: KindStart, Head: h, At: now}}, nil
	}
	if h.Hash == t.head.Hash {
		return nil, nil
	}
	prev := t.head

	// Case 1: plain child of the previous head.
	if h.ParentHash == prev.Hash && h.Number == prev.Number+1 {
		var evs []Event
		// If we are right after a rewind and this height was seen with another
		// hash, the chain was really replaced: this is a reorg.
		if old, ok := t.seen[h.Number]; ok && old != h.Hash && t.rewindFrom != nil {
			depth := t.rewindFrom.Number - prev.Number
			evs = append(evs, Event{Kind: KindReorg, Head: h, Prev: *t.rewindFrom, Depth: depth, Added: 1, Ancestor: prev.Number, At: now})
			t.Reorgs++
			t.MaxReorgDepth = max(t.MaxReorgDepth, depth)
			t.rewindFrom = nil
		} else {
			bt := uint64(0)
			if h.Time >= prev.Time {
				bt = h.Time - prev.Time
			}
			evs = append(evs, Event{Kind: KindBlock, Head: h, Prev: prev, BlockTime: bt, At: now})
		}
		t.Blocks++
		t.set(h)
		return evs, nil
	}

	// Case 2: head went back (or stayed level) onto a block we know is canonical.
	if h.Number <= prev.Number && t.canon[h.Number] == h.Hash {
		depth := prev.Number - h.Number
		t.Rewinds++
		if t.rewindFrom == nil {
			p := prev
			t.rewindFrom = &p
		}
		t.dropAbove(h.Number)
		t.head = h
		return []Event{{Kind: KindRewind, Head: h, Prev: prev, Depth: depth, Ancestor: h.Number, At: now}}, nil
	}

	// Case 3: something else. Walk back from the new head until we meet our canonical view.
	path := []Header{h}
	cur := h
	for i := 0; i < MaxWalkBack; i++ {
		if cur.Number == 0 {
			break
		}
		if known, ok := t.canon[cur.Number-1]; ok && known == cur.ParentHash {
			break // parent is on our canonical chain: found the ancestor
		}
		p, err := t.src.HeaderByHash(ctx, cur.ParentHash)
		if err != nil {
			return nil, fmt.Errorf("walking back from #%d: %w", cur.Number, err)
		}
		path = append(path, p)
		cur = p
	}
	ancestor := cur.Number - 1
	if cur.Number == 0 {
		ancestor = 0
	}
	added := uint64(len(path))
	// Same branch, just more blocks than we saw: skip, not a reorg.
	if ancestor == prev.Number && t.canon[ancestor] == prev.Hash {
		t.Skips++
		t.Blocks += added
		t.set(h)
		return []Event{{Kind: KindSkip, Head: h, Prev: prev, Added: added - 1, At: now}}, nil
	}
	from := prev
	if t.rewindFrom != nil && t.rewindFrom.Number > from.Number {
		from = *t.rewindFrom
	}
	depth := uint64(0)
	if from.Number > ancestor {
		depth = from.Number - ancestor
	}
	t.Reorgs++
	t.MaxReorgDepth = max(t.MaxReorgDepth, depth)
	t.rewindFrom = nil
	t.dropAbove(ancestor)
	for i := len(path) - 1; i >= 0; i-- {
		t.setCanon(path[i])
	}
	t.head = h
	t.Blocks += added
	return []Event{{Kind: KindReorg, Head: h, Prev: from, Depth: depth, Added: added, Ancestor: ancestor, At: now}}, nil
}

func (t *Tracker) set(h Header) {
	t.setCanon(h)
	t.head = h
}

func (t *Tracker) setCanon(h Header) {
	t.canon[h.Number] = h.Hash
	t.seen[h.Number] = h.Hash
	// keep the maps bounded
	if h.Number > 4*MaxWalkBack {
		delete(t.canon, h.Number-4*MaxWalkBack)
		delete(t.seen, h.Number-4*MaxWalkBack)
	}
}

// dropAbove removes our canonical view above n. The dropped hashes stay in
// `seen` so a later replacement at the same height is recognised as a reorg.
func (t *Tracker) dropAbove(n uint64) {
	for num := range t.canon {
		if num > n {
			delete(t.canon, num)
		}
	}
}
