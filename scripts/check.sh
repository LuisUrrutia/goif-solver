#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
for tool in go docker python3 curl rg luacheck shellcheck; do
  command -v "$tool" >/dev/null || { echo "Required quality tool missing: $tool" >&2; exit 1; }
done
bash scripts/install-tools.sh
export PATH="$PWD/artifacts/tools:$PATH"
golangci-lint config verify
container="goif-check-$$"
guard_container="${container}-guard"
cleanup() { docker rm -f "$container" "$guard_container" >/dev/null 2>&1 || true; }
trap cleanup EXIT
# An isolated real Redis instance exercises Lua, TTLs, and concurrent clients.
# These disposable instances are reachable only through host loopback; tests use no credentials.
for instance in "$container" "$guard_container"; do
  docker run --detach --rm --name "$instance" -p 127.0.0.1::6379 \
    --mount "type=bind,src=$PWD/deploy/redis.conf,dst=/etc/redis/redis.conf,readonly" \
    redis:8.10.2-alpine@sha256:3811787313eba226a2ef38658c6ccb91cd5e110edc89c37767de373120a0e5a0 \
    redis-server /etc/redis/redis.conf --protected-mode no >/dev/null
  for ((i=0;i<60;i++)); do
    if docker exec "$instance" redis-cli ping 2>/dev/null | rg -q PONG; then break; fi
    sleep 0.2
  done
  docker exec "$instance" redis-cli ping | rg -q PONG
done
TEST_REDIS_ADDR="$(docker port "$container" 6379/tcp)"
TEST_REDIS_GUARD_ADDR="$(docker port "$guard_container" 6379/tcp)"
TEST_REDIS_RUN_ID="$(docker exec "$container" redis-cli --raw info server | tr -d '\r' | sed -n 's/^run_id://p')"
export TEST_REDIS_ADDR TEST_REDIS_GUARD_ADDR TEST_REDIS_RUN_ID

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

python3 -m compileall -q scripts
for script in scripts/*.sh; do bash -n "$script"; done
shellcheck scripts/*.sh
actionlint .github/workflows/quality.yml
schema='https://raw.githubusercontent.com/yannh/kubernetes-json-schema/a6f9a32d2ccb64b6e4f5b41419b9c2e8ee0cce18/{{.NormalizedKubernetesVersion}}-standalone{{.StrictSuffix}}/{{.ResourceKind}}{{.KindSuffix}}.json'
kubeconform -strict -summary -kubernetes-version 1.37.0 \
  -schema-location "$schema" deploy/kubernetes.yaml deploy/redis.yaml

luacheck internal/storage/redisstore/lua
stylua --check internal/storage/redisstore/lua

python3 scripts/check-lua-config.py
bash scripts/smoke.sh fresh
bash scripts/smoke.sh quick
printf 'Quality gate passed.\n'
