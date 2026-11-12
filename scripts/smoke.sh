#!/usr/bin/env bash
# JSON-RPC smoke test with `cast`: exits non-zero if any check fails.
# Works against geth and labnode (the lab_ checks only run on labnode).
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

FAILS=0
check() { # name, command...
  local name="$1"; shift
  local out
  if out="$("$@" 2>&1)"; then ok "$name: $(echo "$out" | head -1 | cut -c1-90)"; else fail "$name: $out"; FAILS=$((FAILS+1)); fi
}
expect() { # name, expected, command...
  local name="$1" want="$2"; shift 2
  local out; out="$("$@" 2>&1)"
  if [ "$out" = "$want" ]; then ok "$name = $out"; else fail "$name: got '$out', want '$want'"; FAILS=$((FAILS+1)); fi
}
R=(--rpc-url "$RPC_URL")

check  "web3_clientVersion"  cast rpc web3_clientVersion "${R[@]}"
expect "eth_chainId"   1337  cast chain-id "${R[@]}"
check  "eth_blockNumber"     cast block-number "${R[@]}"
check  "eth_gasPrice"        cast gas-price "${R[@]}"
check  "net_version"         cast rpc net_version "${R[@]}"
check  "txpool_status"       cast rpc txpool_status "${R[@]}"
check  "dev account balance" cast balance "$DEV_ADDR" "${R[@]}"

# Round trip: send 1 wei, wait for the receipt, check it landed.
TO=0x000000000000000000000000000000000000dEaD
BEFORE="$(cast balance "$TO" "${R[@]}")"
# --async returns the hash right away; we poll for the receipt ourselves because a freshly
# started node answers "transaction indexing is in progress" (-32000) to receipt lookups
# until its tx indexer has caught up (observed on geth 1.17.10 with a persistent datadir).
if HASH="$(cast send "$TO" --value 1 --private-key "$DEV_KEY" --async "${R[@]}" 2>&1)"; then
  RCPT=""
  for _ in $(seq 1 100); do
    RCPT="$(cast receipt "$HASH" --json "${R[@]}" 2>/dev/null)" && break
    sleep 0.1
  done
  if echo "$RCPT" | grep -q '"status":"0x1"'; then ok "transfer $HASH mined (status 0x1)"; else fail "no successful receipt for $HASH: $RCPT"; FAILS=$((FAILS+1)); fi
else
  fail "cast send: $HASH"; FAILS=$((FAILS+1))
fi
AFTER="$(cast balance "$TO" "${R[@]}")"
if [ "$AFTER" -gt "$BEFORE" ] 2>/dev/null; then ok "recipient balance $BEFORE -> $AFTER"; else fail "balance did not grow ($BEFORE -> $AFTER)"; FAILS=$((FAILS+1)); fi

if cast rpc lab_nodeInfo "${R[@]}" >/dev/null 2>&1; then
  check "lab_nodeInfo"  cast rpc lab_nodeInfo "${R[@]}"
  check "lab_blockFill" cast rpc lab_blockFill 0x3 "${R[@]}"
else
  say "lab_ namespace not available (stock geth): skipped"
fi
if [ "$FAILS" = 0 ]; then ok "smoke test passed"; else fail "$FAILS check(s) failed"; exit 1; fi
