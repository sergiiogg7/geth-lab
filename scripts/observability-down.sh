#!/usr/bin/env bash
# Stop the Prometheus and Grafana started by observability-up.sh (by pid file; never by process name).
#   scripts/observability-down.sh          interrupt, then a hard kill of that pid after 8 s
#   scripts/observability-down.sh --clean  also delete .obs/ (TSDB, Grafana database, logs)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
OBS="$LAB_ROOT/.obs"
ensure_nodectl
for f in grafana prometheus; do
  [ -f "$OBS/$f.pid" ] || { ok "$f: no pid file"; continue; }
  pid="$(cat "$OBS/$f.pid")"
  if "$LAB_ROOT/bin/nodectl" alive "$pid"; then
    say "stopping $f (pid $pid)"
    "$LAB_ROOT/bin/nodectl" stop "$pid" 8 >/dev/null 2>&1 || true
    if "$LAB_ROOT/bin/nodectl" alive "$pid"; then say "$f ignored the interrupt, killing pid $pid"; hard_kill "$pid" || true; sleep 0.5; fi
  fi
  rm -f "$OBS/$f.pid"; ok "$f stopped"
done
if [ "${1:-}" = "--clean" ]; then rm -rf "$OBS"; ok ".obs removed"; fi
