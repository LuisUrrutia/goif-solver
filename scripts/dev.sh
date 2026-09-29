#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mode="${1:-quick}"
if [[ "$mode" != fresh && "$mode" != quick ]]; then
  echo 'usage: scripts/dev.sh fresh|quick' >&2
  exit 2
fi
if [[ "$mode" == fresh ]]; then rm -f bin/goif; fi
mkdir -p bin
go build -o bin/goif ./cmd/goif
exec bin/goif run -config config/development.json -node local-development
