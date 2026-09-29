#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/install-tools.sh
export PATH="$PWD/artifacts/tools:$PATH"
for tool in docker python3 curl rg luacheck shellcheck; do
  command -v "$tool" >/dev/null || { echo "Required quality tool missing: $tool" >&2; exit 1; }
done
golangci-lint config verify
container="goif-check-$$"
cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; }
trap cleanup EXIT
# An isolated real Redis instance exercises Lua, TTLs, and concurrent clients.
docker run --detach --rm --name "$container" -p 127.0.0.1::6379 redis:8.10.2-alpine@sha256:3811787313eba226a2ef38658c6ccb91cd5e110edc89c37767de373120a0e5a0 >/dev/null
for ((i=0;i<60;i++)); do
  if docker exec "$container" redis-cli ping 2>/dev/null | rg -q PONG; then break; fi
  sleep 0.2
done
TEST_REDIS_ADDR="$(docker port "$container" 6379/tcp)"
export TEST_REDIS_ADDR
go_files=()
while IFS= read -r -d '' file; do go_files+=("$file"); done < <(rg --files -0 -g '*.go')
for formatter in goimports gofumpt; do
  unformatted="$("$formatter" -l "${go_files[@]}")"
  if [[ -n "$unformatted" ]]; then
    printf '%s requires formatting:\n%s\n' "$formatter" "$unformatted" >&2
    exit 1
  fi
done
golangci-lint run
staticcheck ./...
gosec -quiet ./...
govulncheck ./...
go test ./...
go build ./...
go vet ./...
python3 scripts/check-architecture.py
python3 scripts/check-layout.py
go test -race -count=1 ./...

python3 -m unittest discover -s scripts -p 'test_*.py'
for script in scripts/*.sh; do bash -n "$script"; done
shellcheck scripts/*.sh
actionlint .github/workflows/quality.yml

luacheck internal/storage/redisstore/lua
stylua --check internal/storage/redisstore/lua

python3 scripts/check-lua-config.py
bash scripts/smoke.sh fresh
bash scripts/smoke.sh quick
printf 'Quality gate passed.\n'
