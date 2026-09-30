#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p artifacts
# Audit only: automatic layout fixes can break positional ABI tuple decoding.
if ! go run golang.org/x/tools/go/analysis/passes/fieldalignment/cmd/fieldalignment@v0.50.0 ./internal/... >artifacts/fieldalignment.txt 2>&1; then
  if ! rg -q '^exit status 3$' artifacts/fieldalignment.txt; then
    cat artifacts/fieldalignment.txt
    exit 1
  fi
fi
go test ./internal/protocol/escrow -run '^$' -bench BenchmarkDecodeOpen -benchmem -count=3 | tee artifacts/discovery-bench.txt
go test ./internal/escrow -run '^$' -bench BenchmarkPrepareRoutes -benchmem -count=3 | tee artifacts/admission-bench.txt
printf 'Layout audit: artifacts/fieldalignment.txt (ABI tuple order is intentional).\n'
