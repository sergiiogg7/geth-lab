#!/usr/bin/env bash
# Delete the persistent datadir and runtime files (kills the node first).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
"$LAB_ROOT/scripts/stop-node.sh" --kill || true
rm -rf "$LAB_STATE"
ok "removed $LAB_STATE"
