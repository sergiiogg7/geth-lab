#!/usr/bin/env bash
# Build the lab tools (loadgen, watch, labnode, nodectl, obscheck) into ./bin
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$LAB_ROOT"
say "go build -p 2 -o bin/ ./cmd/..."
go build -p 2 -o bin/ ./cmd/...
ok "built: $(ls bin | tr '\n' ' ')"
