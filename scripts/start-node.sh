#!/usr/bin/env bash
# Start a Geth dev node in the background.
#
#   scripts/start-node.sh                    fresh in-memory chain (state is lost on stop)
#   scripts/start-node.sh --persist          persistent datadir in .lab/datadir (survives restarts)
#   scripts/start-node.sh --client labnode   use bin/labnode (geth + lab_ namespace + gasProfile tracer)
#   scripts/start-node.sh --period 2         seal a block every 2 s (default 0: one block per tx)
#   scripts/start-node.sh --mem-limit-mb 700 cap the node committed memory with a Windows Job Object (Windows only; see docs/postmortems/C7)
#   scripts/start-node.sh -- <flags>         extra flags passed to the node, e.g. -- --txpool.globalslots 64
#
# Flags passed to geth (labnode understands the same names):
#   --dev                    dev mode: PoS chain, simulated beacon (fake consensus client), prefunded account
#   --datadir D              persistent database (omitted = in-memory)
#   --http --http.api ...    JSON-RPC over HTTP on 127.0.0.1 with the listed namespaces
#   --ipcdisable             no named pipe / unix socket
#   --metrics ...            Prometheus endpoint: http://127.0.0.1:6060/debug/metrics/prometheus
#   --dev.period N           block every N seconds (0 = on demand)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

PERSIST=0; CLIENT=geth; PERIOD=0; MEMMB=0; EXTRA=()
while [ $# -gt 0 ]; do
  case "$1" in
    --persist) PERSIST=1 ;;
    --client)  CLIENT="$2"; shift ;;
    --period)  PERIOD="$2"; shift ;;
    --mem-limit-mb) MEMMB="$2"; shift ;;
    --) shift; EXTRA=("$@"); break ;;
    *) die "unknown option $1" ;;
  esac
  shift
done

mkdir -p "$LAB_STATE"
node_alive && die "a node is already running (pid $(cat "$LAB_PIDFILE")). Run scripts/stop-node.sh first."

case "$CLIENT" in
  geth)    BIN="$(resolve_geth)" ;;
  labnode)
    BIN="$LAB_ROOT/bin/labnode"
    if [ ! -x "$BIN" ] && [ ! -x "$BIN.exe" ]; then "$LAB_ROOT/scripts/build.sh"; fi ;;
  *) die "--client must be geth or labnode" ;;
esac

ARGS=(--dev --http --http.addr 127.0.0.1 --http.port "$HTTP_PORT"
      --http.api eth,net,web3,txpool,debug,admin --ipcdisable
      --metrics --metrics.addr 127.0.0.1 --metrics.port "$METRICS_PORT")
if [ "$PERIOD" != 0 ]; then ARGS+=(--dev.period "$PERIOD"); fi
if [ "$PERSIST" = 1 ]; then mkdir -p "$LAB_DATADIR"; ARGS+=(--datadir "$LAB_DATADIR"); fi
if [ ${#EXTRA[@]} -gt 0 ]; then ARGS+=("${EXTRA[@]}"); fi

say "starting $CLIENT: $BIN ${ARGS[*]}"
: > "$LAB_LOGFILE"
ensure_nodectl
"$LAB_ROOT/bin/nodectl" start -mem-mb "$MEMMB" -log "$LAB_LOGFILE" -pid "$LAB_PIDFILE" -- "$BIN" "${ARGS[@]}" >/dev/null
echo "$CLIENT" > "$LAB_CLIENTFILE"

if secs="$(wait_rpc 60)"; then
  ok "RPC ready after ${secs}s at $RPC_URL (pid $(cat "$LAB_PIDFILE"), log $LAB_LOGFILE)"
  if [ "$PERSIST" = 1 ]; then say "persistent datadir: $LAB_DATADIR"; else say "in-memory chain: state disappears when the node stops"; fi
else
  tail -20 "$LAB_LOGFILE" >&2; die "node did not answer within 60 s"
fi
