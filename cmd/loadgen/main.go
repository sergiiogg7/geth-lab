// Command loadgen floods a dev node with transactions and prints a report.
//
//	loadgen -rpc http://127.0.0.1:8545 -senders 8 -txs 2000 -rate 400
//	loadgen -mode contract -data 512          call the counter contract with 512 bytes of calldata
//	loadgen -scenario gap -txs 600            create nonce gaps (queued, non-executable txs)
//	loadgen -scenario replace -txs 100        same-nonce replacements (underpriced / bumped)
//
// All the logic lives in internal/loadgen so the chaos tests can reuse it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/sergiiogg7/geth-lab/internal/loadgen"
)

func main() {
	var (
		cfg loadgen.Config
		js  = flag.Bool("json", false, "print the report as JSON")
		out = flag.String("out", "", "also write the JSON report to this file")
	)
	flag.StringVar(&cfg.RPC, "rpc", "http://127.0.0.1:8545", "JSON-RPC HTTP endpoint")
	flag.IntVar(&cfg.Senders, "senders", 8, "number of sender accounts")
	flag.IntVar(&cfg.Txs, "txs", 1000, "total transactions (0 = use -duration)")
	flag.DurationVar(&cfg.Duration, "duration", 0, "run time when -txs is 0")
	flag.Float64Var(&cfg.Rate, "rate", 0, "total target tx/s (0 = unthrottled)")
	flag.StringVar(&cfg.Mode, "mode", "transfer", "transfer | contract")
	flag.IntVar(&cfg.DataBytes, "data", 0, "calldata bytes per transaction")
	flag.StringVar(&cfg.Scenario, "scenario", "steady", "steady | gap | replace")
	flag.IntVar(&cfg.GapEvery, "gap-every", 5, "gap scenario: skip a nonce every N txs per sender")
	flag.Float64Var(&cfg.TipGwei, "tip", 1, "priority fee in gwei")
	flag.StringVar(&cfg.Seed, "seed", "loadgen", "sender key derivation seed")
	flag.DurationVar(&cfg.Drain, "drain", 20*time.Second, "wait this long for inclusion after sending")
	flag.Parse()
	cfg.Log = func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	rep, err := loadgen.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
	if *out != "" {
		b, _ := json.MarshalIndent(rep, "", "  ")
		_ = os.WriteFile(*out, b, 0o644)
	}
	if *js {
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Print(rep.Format())
}
