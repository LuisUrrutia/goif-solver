SHELL := /bin/bash -euo pipefail
.DEFAULT_GOAL := help

.PHONY: help fmt fmt-check lint build test test-race test-integration smoke check

help:
	@printf '%s\n' \
	  'make fmt               Format Go and Lua files' \
	  'make fmt-check         Check Go and Lua formatting' \
	  'make lint              Check formatting, code, scripts, and manifests' \
	  'make build             Build bin/goif' \
	  'make test              Run Go tests (Redis tests need TEST_REDIS_ADDR)' \
	  'make test-race         Run Go tests with the race detector' \
	  'make test-integration  Run tests and race detection with disposable Redis' \
	  'make smoke             Check fresh and quick development startup' \
	  'make check             Run the full local and CI quality gate'

fmt:
	rg --files -0 -g '*.go' | xargs -0 goimports -w
	rg --files -0 -g '*.go' | xargs -0 gofumpt -w
	stylua internal/storage/redisstore/lua

fmt-check:
	@for formatter in goimports gofumpt; do \
	  unformatted="$$(rg --files -0 -g '*.go' | xargs -0 "$$formatter" -l)"; \
	  if [[ -n "$$unformatted" ]]; then \
	    printf '%s requires formatting:\n%s\n' "$$formatter" "$$unformatted" >&2; \
	    exit 1; \
	  fi; \
	done
	stylua --check internal/storage/redisstore/lua

lint: fmt-check
	golangci-lint config verify
	golangci-lint run
	staticcheck ./...
	gosec -quiet ./...
	govulncheck ./...
	go vet ./...
	luacheck internal/storage/redisstore/lua
	for script in scripts/*.sh; do bash -n "$$script"; done
	shellcheck scripts/*.sh
	actionlint .github/workflows/quality.yml
	kubeconform -strict -summary -kubernetes-version 1.37.0 \
	  -schema-location 'https://raw.githubusercontent.com/yannh/kubernetes-json-schema/a6f9a32d2ccb64b6e4f5b41419b9c2e8ee0cce18/{{.NormalizedKubernetesVersion}}-standalone{{.StrictSuffix}}/{{.ResourceKind}}{{.KindSuffix}}.json' \
	  deploy/kubernetes.yaml deploy/redis.yaml

build:
	mkdir -p bin
	go build -o bin/goif ./cmd/goif

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

test-integration:
	@primary="goif-test-$$$$"; \
	guard="$${primary}-guard"; \
	trap 'docker rm -f "$$primary" "$$guard" >/dev/null 2>&1 || true' EXIT; \
	for instance in "$$primary" "$$guard"; do \
	  docker run --detach --rm --name "$$instance" -p 127.0.0.1::6379 \
	    --mount "type=bind,src=$(CURDIR)/deploy/redis.conf,dst=/etc/redis/redis.conf,readonly" \
	    redis:8.10.2-alpine@sha256:3811787313eba226a2ef38658c6ccb91cd5e110edc89c37767de373120a0e5a0 \
	    redis-server /etc/redis/redis.conf --protected-mode no >/dev/null; \
	  for ((i=0;i<60;i++)); do \
	    if docker exec "$$instance" redis-cli ping 2>/dev/null | rg -q PONG; then break; fi; \
	    sleep 0.2; \
	  done; \
	  docker exec "$$instance" redis-cli ping | rg -q PONG; \
	done; \
	TEST_REDIS_ADDR="$$(docker port "$$primary" 6379/tcp)"; \
	TEST_REDIS_GUARD_ADDR="$$(docker port "$$guard" 6379/tcp)"; \
	TEST_REDIS_RUN_ID="$$(docker exec "$$primary" redis-cli --raw info server | tr -d '\r' | sed -n 's/^run_id://p')"; \
	export TEST_REDIS_ADDR TEST_REDIS_GUARD_ADDR TEST_REDIS_RUN_ID; \
	$(MAKE) --no-print-directory -j1 test test-race

smoke:
	bash scripts/smoke.sh fresh
	bash scripts/smoke.sh quick

check:
	@for tool in go docker curl rg goimports gofumpt golangci-lint staticcheck \
	  gosec govulncheck luacheck stylua shellcheck actionlint kubeconform; do \
	  command -v "$$tool" >/dev/null || { \
	    printf 'Missing tool: %s. See README.md#development-tools.\n' "$$tool" >&2; \
	    exit 1; \
	  }; \
	done
	$(MAKE) --no-print-directory -j1 lint build test-integration smoke
	@printf 'Quality gate passed.\n'
