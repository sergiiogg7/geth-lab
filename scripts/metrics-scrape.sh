#!/usr/bin/env bash
# Scrape the node's Prometheus endpoint.
#   scripts/metrics-scrape.sh           summary: family count and the operator-relevant values
#   scripts/metrics-scrape.sh --raw     the raw exposition text
#   scripts/metrics-scrape.sh --names   one "<family> <type>" per line
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
URL="http://127.0.0.1:$METRICS_PORT/debug/metrics/prometheus"
RAW="$(curl -fsS "$URL")" || die "no metrics at $URL (was the node started with --metrics?)"
case "${1:-}" in
  --raw)   echo "$RAW" ;;
  --names) echo "$RAW" | grep '^# TYPE' | awk '{print $3, $4}' | sort ;;
  *)
    echo "metric families: $(echo "$RAW" | grep -c '^# TYPE')"
    echo "--- selected values ---"
    echo "$RAW" | grep -E '^(chain_head_(block|header|safe|finalized)|txpool_(pending|queued|local|valid|invalid|known|underpriced|overflowed)|p2p_peers|system_(cpu_procload|memory_used|memory_allocs)|chain_inserts_count|geth_info|rpc_(requests|success|failure)) ' || true
    ;;
esac
