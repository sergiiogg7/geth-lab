#!/usr/bin/env bash
# End-to-end demo of the lab (about 2 minutes). Everything is real: a Geth dev node
# (labnode: geth embedded as a library + the lab_ namespace), a transaction flood,
# the watcher, a gas profile of one transaction, a forced reorg, and one chaos
# scenario (kill -9 + restart) with assertions.
#
#   scripts/demo.sh            run it
#   scripts/demo.sh | tee out.txt
#
# Requirements: Go, Foundry (cast), geth 1.17.10 on PATH or in GOPATH/bin.
# Exit code is non-zero if an assertion fails.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$LAB_ROOT"

FAILED=0
step()   { printf '\n\033[1;35m### %s\033[0m\n' "$*"; }
note()   { printf '    \033[2m%s\033[0m\n' "$*"; }
assert() { # description, then a command that must succeed
  local d="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "ASSERT $d"; else fail "ASSERT $d"; FAILED=$((FAILED+1)); fi
}
cleanup() { [ -n "${WATCH_PID:-}" ] && hard_kill "$(winpid "$WATCH_PID")" 2>/dev/null; scripts/stop-node.sh --kill >/dev/null 2>&1; }
trap cleanup EXIT

# ------------------------------------------------------------------------------------
step "0. Clean slate and build the lab tools"
scripts/stop-node.sh --kill >/dev/null 2>&1 || true
rm -rf "$LAB_STATE"
scripts/build.sh | tail -1
note "geth binary: $(resolve_geth)  ->  $("$(resolve_geth)" version | sed -n 2p)"

# ------------------------------------------------------------------------------------
step "1. Start labnode: Geth embedded as a Go library + the lab_ JSON-RPC namespace"
note "dev mode = one node, simulated consensus client, a block every second, prefunded dev account"
scripts/start-node.sh --client labnode --period 1 | sed 's/^.*==>//' | tail -2
scripts/smoke.sh 2>&1 | tail -4

# ------------------------------------------------------------------------------------
step "2. Watcher: follows heads, prints block time/gas, detects reorgs (also serves a status page)"
bin/watch -rpc "$RPC_URL" -interval 200ms -duration 300s -serve 127.0.0.1:8081 > "$LAB_STATE/watch.log" 2>&1 &
WATCH_PID=$!
sleep 3
tail -3 "$LAB_STATE/watch.log"

# ------------------------------------------------------------------------------------
step "3. Load: 800 contract calls (1 SSTORE each) with 64 bytes of calldata from 8 senders at 250 tx/s"
bin/loadgen -rpc "$RPC_URL" -senders 8 -txs 800 -rate 250 -mode contract -data 64 -seed demo
echo "--- txpool as the lab_ namespace sees it, and the last blocks:"
cast rpc lab_txpoolStats --rpc-url "$RPC_URL"
cast rpc lab_blockFill 0x4 --rpc-url "$RPC_URL" | tr -d ' ' | sed 's/},{/},\n{/g' | cut -c1-210
assert "all 800 txs were mined (txpool empty)" test "$(cast rpc txpool_status --rpc-url "$RPC_URL" | grep -o '"pending":"0x0"')" = '"pending":"0x0"'

# ------------------------------------------------------------------------------------
step "4. Custom EVM tracer: where did the gas of one transaction go? (lab_gasProfile)"
CTR=$(cast send --private-key "$DEV_KEY" --rpc-url "$RPC_URL" --json --create 0x60088060095f395ff35f546001015f5500 | grep -o '"contractAddress":"0x[0-9a-fA-F]*"' | cut -d'"' -f4)
TX=$(cast send --private-key "$DEV_KEY" --rpc-url "$RPC_URL" --json "$CTR" | grep -o '"transactionHash":"0x[0-9a-f]*"' | head -1 | cut -d'"' -f4)
note "counter contract $CTR, call tx $TX"
cast rpc lab_gasProfile "$TX" --rpc-url "$RPC_URL" | sed 's/"ops":\[/\n  ops:\n    /; s/},{/},\n    {/g' | cut -c1-230
assert "profile says exactly one SSTORE" sh -c "cast rpc lab_gasProfile $TX --rpc-url $RPC_URL | grep -q '\"storageWrites\":1'"

# ------------------------------------------------------------------------------------
step "5. Force a real reorg of depth 3 and see the watcher react"
sleep 4
HEAD_BEFORE=$(cast block-number --rpc-url "$RPC_URL")
cast rpc lab_fork 0x3 --rpc-url "$RPC_URL" | cut -c1-200
sleep 6
grep -E "REWIND|REORG" "$LAB_STATE/watch.log" | tail -3
assert "watcher reported a REORG" grep -q REORG "$LAB_STATE/watch.log"
echo "--- status page JSON (http://127.0.0.1:8081/status.json):"
curl -s http://127.0.0.1:8081/status.json | tr -d '\n ' | cut -c1-330; echo
hard_kill "$(winpid "$WATCH_PID")" 2>/dev/null; WATCH_PID=""

# ------------------------------------------------------------------------------------
step "6. Prometheus metrics straight from the node (no Prometheus needed)"
scripts/metrics-scrape.sh | head -12
scripts/stop-node.sh | head -1

# ------------------------------------------------------------------------------------
step "7. Chaos C1: kill -9 under load, then restart on the SAME datadir (stock geth, defaults)"
note "hypothesis: sealed blocks survive; only unmined pool txs are at risk"
scripts/start-node.sh --persist --period 2 | tail -2 | head -1
(bin/loadgen -rpc "$RPC_URL" -senders 4 -txs 3000 -rate 300 -drain 1s -seed c1demo > "$LAB_STATE/c1-load.log" 2>&1 &)
sleep 11
HEAD_BEFORE=$(cast block-number --rpc-url "$RPC_URL")
PEND_BEFORE=$(cast rpc txpool_status --rpc-url "$RPC_URL")
note "before kill: head=$HEAD_BEFORE txpool=$PEND_BEFORE"
scripts/stop-node.sh --kill | head -1
sleep 1
scripts/start-node.sh --persist --period 2 | tail -2 | head -1
HEAD_AFTER=$(cast block-number --rpc-url "$RPC_URL")
note "after restart: head=$HEAD_AFTER"
grep -E "Unclean shutdown|Head state missing|Switching from full-sync|Loaded local transaction journal" "$LAB_LOGFILE" | cut -c1-150
assert "kill -9 rolled the chain back (blocks were lost)" test "$HEAD_AFTER" -lt "$HEAD_BEFORE"
note "is the node functional? send one tx now, then another 6 s later (period 2 s => each should be mined)"
cast send 0x000000000000000000000000000000000000dEaD --value 1 --private-key "$DEV_KEY" --rpc-url "$RPC_URL" --async >/dev/null 2>&1
sleep 6
H1=$(cast block-number --rpc-url "$RPC_URL")
cast send 0x000000000000000000000000000000000000dEaD --value 1 --private-key "$DEV_KEY" --rpc-url "$RPC_URL" --async >/dev/null 2>&1
sleep 8
H2=$(cast block-number --rpc-url "$RPC_URL")
note "head after first tx: $H1, 8 s after the second tx: $H2"
assert "block production is stuck after the crash (see docs/postmortems/C1)" test "$H1" = "$H2"
grep -c "Ignoring payload while snap syncing" "$LAB_LOGFILE" | xargs -I{} echo "    log lines 'Ignoring payload while snap syncing': {}"
scripts/stop-node.sh --kill | head -1
rm -rf "$LAB_STATE/datadir"

note "mitigation: same crash with --gcmode archive --state.scheme hash"
scripts/start-node.sh --persist --period 2 -- --gcmode archive --state.scheme hash | tail -2 | head -1
(bin/loadgen -rpc "$RPC_URL" -senders 4 -txs 3000 -rate 300 -drain 1s -seed c1demo2 > "$LAB_STATE/c1b-load.log" 2>&1 &)
sleep 11
HEAD_BEFORE=$(cast block-number --rpc-url "$RPC_URL")
scripts/stop-node.sh --kill | head -1
sleep 1
scripts/start-node.sh --persist --period 2 -- --gcmode archive --state.scheme hash | tail -2 | head -1
HEAD_AFTER=$(cast block-number --rpc-url "$RPC_URL")
note "before kill: head=$HEAD_BEFORE  after restart: head=$HEAD_AFTER"
assert "archive+hash loses at most the in-flight block" test "$HEAD_AFTER" -ge $((HEAD_BEFORE-1))
cast send 0x000000000000000000000000000000000000dEaD --value 1 --private-key "$DEV_KEY" --rpc-url "$RPC_URL" --async >/dev/null 2>&1
sleep 5
H1=$(cast block-number --rpc-url "$RPC_URL"); sleep 5; H2=$(cast block-number --rpc-url "$RPC_URL")
assert "block production continues after the crash" test "$H2" -gt "$H1"
scripts/stop-node.sh | head -1

step "Done"
if [ "$FAILED" = 0 ]; then ok "demo finished, all assertions passed"; else fail "$FAILED assertion(s) failed"; exit 1; fi
