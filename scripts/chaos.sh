#!/usr/bin/env bash
# Run the chaos scenarios (they start and kill real geth/labnode processes).
#   scripts/chaos.sh            all of C1..C6 (about 6 minutes)
#   scripts/chaos.sh C1 C3      selected scenarios
# Results: out/chaos/<scenario>.json and node logs in out/chaos/logs/. Postmortems: docs/postmortems/.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$LAB_ROOT"
scripts/stop-node.sh --kill >/dev/null 2>&1 || true

RUN=""
if [ $# -gt 0 ]; then
  for s in "$@"; do RUN="$RUN|Test${s}_"; done
  RUN="${RUN#|}"
else
  RUN="Test"
fi
say "go test -tags chaos -run '$RUN' ./test/chaos"
go test -tags chaos -count=1 -v -timeout 20m ./test/chaos -run "$RUN" 2>&1 | tee out/chaos-run.txt | grep -E "^(=== RUN|--- |    --- |ok|FAIL|PASS)" || true
ok "observations in out/chaos/*.json"
