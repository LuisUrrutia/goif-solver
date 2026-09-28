#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p artifacts
go build -o bin/goif ./cmd/goif
bash scripts/dev.sh "${1:-quick}" >artifacts/smoke.log 2>&1 &
pid=$!
cleanup() { kill -TERM "$pid" 2>/dev/null || true; wait "$pid" || true; }
trap cleanup EXIT
ready=false
for ((i=0;i<90;i++)); do
  kill -0 "$pid" 2>/dev/null || { cat artifacts/smoke.log; exit 1; }
  if curl --fail --silent http://127.0.0.1:8080/readyz >artifacts/ready.txt 2>/dev/null; then ready=true; break; fi
  sleep 1
done
[[ "$ready" == true ]]
curl --fail --silent http://127.0.0.1:8080/healthz
curl --fail --silent http://127.0.0.1:8080/metrics
[[ "$(curl --silent -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/control)" == 401 ]]
GOIF_REDIS_URL=redis://127.0.0.1:16379/0 bin/goif control -config config/sepolia.json
printf 'Observation service and protected control smoke passed.\n'
