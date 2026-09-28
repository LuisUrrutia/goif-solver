#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
container="goif-check-$$"
cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; }
trap cleanup EXIT
# An isolated real Redis instance exercises Lua, TTLs, and concurrent clients.
docker run --detach --rm --name "$container" -p 127.0.0.1::6379 redis:8.10.2-alpine >/dev/null
for ((i=0;i<60;i++)); do
  if docker exec "$container" redis-cli ping 2>/dev/null | rg -q PONG; then break; fi
  sleep 0.2
done
TEST_REDIS_ADDR="$(docker port "$container" 6379/tcp)"
export TEST_REDIS_ADDR
go_files=()
while IFS= read -r -d '' file; do go_files+=("$file"); done < <(rg --files -0 -g '*.go')
test -z "$(gofmt -l "${go_files[@]}")"
go test ./...
go build ./...
go vet ./...
go test -race ./...

python3 -m unittest discover -s scripts -p 'test_*.py'
for script in scripts/*.sh; do bash -n "$script"; done
if command -v shellcheck >/dev/null 2>&1; then shellcheck scripts/*.sh; fi

luacheck internal/coordination/lua
stylua --check internal/coordination/lua
