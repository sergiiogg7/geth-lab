package nodeproc

import (
	"slices"
	"testing"
)

func TestArgsDefaultsAndOptions(t *testing.T) {
	o := DevOpts{HTTPPort: 18545}
	a := o.Args()
	for _, want := range []string{"--dev", "--http", "--ipcdisable"} {
		if !slices.Contains(a, want) {
			t.Fatalf("missing %s in %v", want, a)
		}
	}
	for _, unwanted := range []string{"--datadir", "--metrics", "--dev.period", "--cache", "--gcmode"} {
		if slices.Contains(a, unwanted) {
			t.Fatalf("%s must not be present by default: %v", unwanted, a)
		}
	}
	o = DevOpts{HTTPPort: 1, Datadir: "D", MetricsPort: 2, Period: 3, Cache: 64, GCMode: "archive", StateScheme: "hash",
		TxPool: map[string]int{"globalslots": 9}, Extra: []string{"--foo"}}
	a = o.Args()
	for _, want := range []string{"--datadir", "D", "--metrics", "--dev.period", "3", "--cache", "64", "--gcmode", "archive",
		"--state.scheme", "hash", "--txpool.globalslots", "9", "--foo"} {
		if !slices.Contains(a, want) {
			t.Fatalf("missing %s in %v", want, a)
		}
	}
	if o.URL() != "http://127.0.0.1:1" {
		t.Fatalf("url = %s", o.URL())
	}
}
