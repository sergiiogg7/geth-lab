#!/usr/bin/env bash
# Shared settings and helpers for the lab scripts. Sourced, never executed.
# Works in Git Bash on Windows and on Linux/macOS.

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LAB_STATE="${LAB_STATE:-$LAB_ROOT/.lab}"          # runtime files (git-ignored)
LAB_DATADIR="${LAB_DATADIR:-$LAB_STATE/datadir}"  # persistent chain database
LAB_PIDFILE="$LAB_STATE/node.pid"
LAB_LOGFILE="$LAB_STATE/node.log"
LAB_CLIENTFILE="$LAB_STATE/node.client"

HTTP_PORT="${HTTP_PORT:-8545}"
METRICS_PORT="${METRICS_PORT:-6060}"
RPC_URL="${RPC_URL:-http://127.0.0.1:$HTTP_PORT}"

# Public developer key printed by `geth --dev` itself. TEST KEY ONLY.
DEV_KEY="0xb71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291"
DEV_ADDR="0x71562b71999873DB5b286dF957af199Ec94617F7"

say()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m OK\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL\033[0m %s\n' "$*" >&2; }
die()  { fail "$*"; exit 1; }

# Go toolchain of the author's machine (skipped elsewhere).
[ -f /c/Users/srg/tools/env.sh ] && source /c/Users/srg/tools/env.sh

# geth binary: $GETH_BIN, then PATH, then GOPATH/bin.
resolve_geth() {
  if [ -n "${GETH_BIN:-}" ]; then echo "$GETH_BIN"; return; fi
  if command -v geth >/dev/null 2>&1; then command -v geth; return; fi
  local gp; gp="$(go env GOPATH 2>/dev/null || true)"
  if [ -n "$gp" ] && { [ -x "$gp/bin/geth" ] || [ -x "$gp/bin/geth.exe" ]; }; then echo "$gp/bin/geth"; return; fi
  die "geth not found. Run: go install github.com/ethereum/go-ethereum/cmd/geth@v1.17.10"
}

# nodectl: tiny Go helper (start detached, graceful stop, alive probe), see cmd/nodectl.
ensure_nodectl() {
  if [ ! -x "$LAB_ROOT/bin/nodectl" ] && [ ! -x "$LAB_ROOT/bin/nodectl.exe" ]; then (cd "$LAB_ROOT" && go build -o bin/ ./cmd/nodectl); fi
}

# Is the pid in the pidfile alive?
node_alive() {
  [ -f "$LAB_PIDFILE" ] || return 1
  ensure_nodectl
  "$LAB_ROOT/bin/nodectl" alive "$(cat "$LAB_PIDFILE")"
}

# Windows pid of a background job started from Git Bash (the MSYS pid differs from the Windows pid).
winpid() { if [ -r "/proc/$1/winpid" ]; then cat "/proc/$1/winpid"; else echo "$1"; fi; }

hard_kill() { # $1 = (windows) pid
  if command -v taskkill >/dev/null 2>&1; then taskkill //F //PID "$1" >/dev/null 2>&1; else kill -9 "$1"; fi
}

# Poll until the JSON-RPC endpoint answers; prints the seconds waited. Returns 1 on timeout.
wait_rpc() {
  local timeout="${1:-60}" i=0 t0 t1
  t0=$(date +%s%N)
  while ! cast block-number --rpc-url "$RPC_URL" >/dev/null 2>&1; do
    i=$((i+1)); [ "$i" -gt $((timeout*10)) ] && return 1; sleep 0.1
  done
  t1=$(date +%s%N)
  awk "BEGIN{printf \"%.1f\", ($t1-$t0)/1e9}"
}
