# Quality gate

Run `bash scripts/check.sh` after implementation and before declaring completion. GitHub Actions executes this same command on pushes and pull requests, including forks, with read-only repository access and no application secrets. The workflow has a 30-minute limit and cancels superseded runs.

The host needs Go 1.27.1, Docker, Python 3.9+, Bash, curl, ripgrep, luacheck 1.2.0, and ShellCheck. Development runtime itself needs only Go and Bash; Docker is used by the full gate to test real Redis scripts, leases, and multiple clients. Two isolated Redis processes use `deploy/redis.conf` to exercise primary replacement and durability checks. Their anonymous test endpoints bind only to host loopback; the production profile retains protected mode and requires deployment credentials.

`install-tools.sh` installs pinned tools into ignored `artifacts/tools/`, outside the application module. Go tools use the module checksum database; downloaded golangci-lint and StyLua archives are checked against published SHA-256 values. Cached release executables are also hashed before reuse. Tool installation or any missing prerequisite fails the gate.

| Check | Version / command |
| --- | --- |
| Import formatting | goimports, x/tools v0.50.0 |
| Go formatting | gofumpt v0.12.0 |
| Error handling and unused code | golangci-lint v2.14.0: errcheck, ineffassign, unused |
| Static analysis | staticcheck v0.8.1 and `go vet ./...` |
| Security analysis | gosec v2.29.0 |
| Reachable vulnerabilities | govulncheck v1.8.0, current vulnerability database |
| Behavior | `go test ./...` against isolated real Redis and memory |
| Build | `go build ./...` |
| Race detection | `go test -race -count=1 ./...` |
| Struct layout | `python3 scripts/check-layout.py`, x/tools v0.50.0 |
| Architecture | `python3 scripts/check-architecture.py` |
| Lua | luacheck, StyLua v2.5.2, LuaLS/luacheck configuration consistency |
| Scripts and workflow | Python tests, Bash syntax, ShellCheck, actionlint v1.7.12 |
| Kubernetes manifests | kubeconform v0.8.0, strict Kubernetes 1.37.0 schemas at pinned revision |
| Runtime smoke | `bash scripts/smoke.sh fresh` and `bash scripts/smoke.sh quick` |

The layout gate runs pinned `fieldalignment` on production structs. The two positional Solidity tuple types are explicit exceptions; test fixtures remain visible in the separate profile report. Field order is never auto-fixed in the completion gate.

The architecture check follows transitive imports. Generic quote coordination, preflight, and settlement cannot import a VM runtime or concrete adapter. The Polymer HTTP client also cannot depend on its EVM implementation.

Staticcheck, vet, and gosec run as separate tools rather than being duplicated inside golangci-lint. Format checks are read-only. For an intentional formatting change, run `artifacts/tools/goimports -w` followed by `artifacts/tools/gofumpt -w` with the changed Go file paths.

Every failing check blocks success; there is no baseline-only mode or security severity filter. Fix findings in source. Existing `#nosec` comments are limited to local operator-selected file paths and numeric conversions with explicit preceding bounds. Each comment records its boundary or invariant. Request/test decoding failures are checked. Errors from response writes and read-only resource cleanup are explicitly discarded when the response is already complete or the resource is already being discarded.

## Lua in editors

Redis injects `redis`, `KEYS`, `ARGV`, and `cjson` into Lua scripts. The root `.luarc.json` declares exactly those globals and Lua 5.1 for LuaLS, which Zed uses. Undefined-global diagnostics remain enabled for every other name. `.luacheckrc` declares the same execution environment; the gate rejects drift between the two files. Open the repository root as the editor workspace so it can discover `.luarc.json`.

LuaLS 3.19.1 was also run directly against the Redis scripts with an absolute configuration path: diagnosis completed with no problems. The full LuaLS binary is not required by the gate; luacheck checks the actual scripts on every run.

Sources:
- https://github.com/mvdan/gofumpt
- https://golangci-lint.run/docs/welcome/install/
- https://staticcheck.dev/docs/running-staticcheck/cli/
- https://github.com/securego/gosec
- https://go.dev/security/vuln/
- https://luals.github.io/wiki/configuration/

## Boundaries of the evidence

The gate uses local RPC/HTTP fixtures for fill, proof relay, and settlement. It does not submit funded transactions, load application secrets, deploy, or query live LI.FI/Polymer services. `scripts/preflight.sh` is a separate read-only public-network check. Workflow syntax and commands are verified locally; a hosted GitHub run requires publishing the branch.
