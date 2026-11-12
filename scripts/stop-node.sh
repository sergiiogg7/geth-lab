#!/usr/bin/env bash
# Stop the node started by start-node.sh.
#   scripts/stop-node.sh          graceful: Ctrl-C equivalent, the node flushes its database
#   scripts/stop-node.sh --kill   hard kill (TerminateProcess / SIGKILL): the node gets no chance to flush
#
# Graceful stop is done by bin/nodectl (Ctrl-Break to the node private console on Windows,
# SIGINT elsewhere). labnode can also be stopped with the lab_shutdown RPC.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

node_alive || { ok "no node running"; rm -f "$LAB_PIDFILE"; exit 0; }
PID="$(cat "$LAB_PIDFILE")"
CLIENT="$(cat "$LAB_CLIENTFILE" 2>/dev/null || echo geth)"

if [ "${1:-}" = "--kill" ]; then
  say "hard kill pid $PID"; hard_kill "$PID"
else
  say "graceful stop pid $PID ($CLIENT)"
  "$LAB_ROOT/bin/nodectl" stop "$PID" 20 || true
  for _ in $(seq 1 100); do node_alive || break; sleep 0.2; done
  if node_alive; then fail "still running after 20 s, killing"; hard_kill "$PID"; fi
fi
sleep 0.3
rm -f "$LAB_PIDFILE"
ok "node stopped"
tail -3 "$LAB_LOGFILE" | cut -c1-160
