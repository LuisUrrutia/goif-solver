#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
go run ./cmd/goif preflight -config config/sepolia.json -intent 0x98441c442077615b279a788283ecb399cb3bbb1e7e86103d375e5b64c9172bb3
