#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mode="${1:-quick}"
container=goif-observe-redis
if [[ "$mode" != fresh && "$mode" != quick ]]; then
  echo 'usage: scripts/dev.sh fresh|quick' >&2
  exit 2
fi
if docker inspect "$container" >/dev/null 2>&1; then
  [[ "$(docker inspect -f '{{index .Config.Labels "goif.mode"}}' "$container")" == observe ]] || {
    echo 'Refusing to change a container not labeled for observation.' >&2
    exit 1
  }
  if [[ "$mode" == fresh ]]; then docker rm -f "$container" >/dev/null; fi
fi
if [[ "$mode" == fresh ]]; then rm -f bin/goif; fi
if ! docker inspect "$container" >/dev/null 2>&1; then
  docker run -d --name "$container" --label goif.mode=observe -p 127.0.0.1:16379:6379 redis:8.10.2-alpine >/dev/null
else
  docker start "$container" >/dev/null
fi
for ((i=0;i<60;i++)); do
  if docker exec "$container" redis-cli ping 2>/dev/null | rg -q PONG; then break; fi
  sleep 0.2
done
mkdir -p bin
go build -o bin/goif ./cmd/goif
export GOIF_REDIS_URL=redis://127.0.0.1:16379/0
# This entry point only observes; live execution requires a separate invocation.
exec bin/goif run -config config/sepolia.json -node local-observer
