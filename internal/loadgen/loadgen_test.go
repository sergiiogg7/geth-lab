package loadgen

import (
	"errors"
	"testing"
	"time"
)

func TestPercentilesNearestRank(t *testing.T) {
	var s []time.Duration
	for i := 1; i <= 100; i++ {
		s = append(s, time.Duration(i)*time.Millisecond)
	}
	p := Compute(s)
	if p.N != 100 || p.Min != 1 || p.P50 != 50 || p.P90 != 90 || p.P99 != 99 || p.Max != 100 {
		t.Fatalf("unexpected percentiles: %+v", p)
	}
	if e := Compute(nil); e.N != 0 {
		t.Fatalf("empty sample must yield N=0, got %+v", e)
	}
}

func TestClassifyError(t *testing.T) {
	cases := map[string]string{
		"replacement transaction underpriced":                             "replacement-underpriced",
		"transaction underpriced":                                         "underpriced",
		"txpool is full":                                                  "txpool-full",
		"nonce too low: next nonce 5, tx nonce 4":                         "nonce-too-low",
		"already known":                                                   "already-known",
		"account limit exceeded":                                          "account-limit",
		"future transaction tries to replace pending":                     "future-replaces-pending",
		"insufficient funds for gas * price + value":                      "insufficient-funds",
		"dial tcp 127.0.0.1:8545: connectex: No connection could be made": "connection",
	}
	for msg, want := range cases {
		if got := ClassifyError(errors.New(msg)); got != want {
			t.Errorf("ClassifyError(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestGwei(t *testing.T) {
	if gwei(1.5).String() != "1500000000" {
		t.Fatalf("gwei(1.5) = %s", gwei(1.5))
	}
}
