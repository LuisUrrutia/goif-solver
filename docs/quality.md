# Quality gate

Run `bash scripts/check.sh` before committing or reporting work complete. GitHub
Actions runs the same command on pushes and pull requests, including forks. It
uses read-only repository access and no application secrets, cancels superseded
runs, and stops after 30 minutes.

Install Go 1.27.1, Docker, Python 3.9+, Bash, curl, ripgrep, luacheck 1.2.0, and
ShellCheck on the host. Running the development service needs only Go and Bash.
The full gate also uses Docker to test Redis scripts, leases, and multiple clients
against two isolated Redis processes. Both use `deploy/redis.conf` to exercise
primary replacement and durability checks. Their anonymous test endpoints bind
only to host loopback. Production keeps protected mode enabled and requires
deployment credentials.

The gate checks prerequisites before `install-tools.sh` installs pinned tools into
the ignored `artifacts/tools/` directory, outside the application module. Go tools
use the module checksum database. Downloaded golangci-lint and StyLua archives are
checked against published SHA-256 values, and cached release executables are
hashed before reuse. A missing prerequisite or failed installation stops the gate.

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
| Scripts and workflow | Python and Bash syntax, ShellCheck, actionlint v1.7.12 |
| Kubernetes manifests | kubeconform v0.8.0, strict Kubernetes 1.37.0 schemas at pinned revision |
| Runtime smoke | `bash scripts/smoke.sh fresh` and `bash scripts/smoke.sh quick` |

The layout check runs pinned `fieldalignment` on production structs. It exempts
the two positional Solidity tuple types because reordering them would change ABI
decoding. Test fixtures remain visible in the separate profile report. The gate
never changes field order automatically.

The architecture check follows transitive imports, so an indirect dependency can
also fail the gate. Generic quote coordination, preflight, and settlement must
remain independent of VM runtimes and concrete adapters. The Polymer HTTP client
must also remain independent of its EVM implementation.

Staticcheck, vet, and gosec each run once, outside golangci-lint. Format checks are
read-only. To apply formatting, run `artifacts/tools/goimports -w` followed by
`artifacts/tools/gofumpt -w` with the changed Go file paths.

Every check must pass. There is no baseline-only mode or security severity filter.
Fix findings in source. Existing `#nosec` comments cover only local file paths
selected by the operator and numeric conversions with explicit preceding bounds.
Each comment records the trust boundary or invariant that permits the exception.

Request and test decoding errors must be handled. Response-write and read-only
cleanup errors are explicitly discarded only when the response is already
complete or the resource is already being discarded.

## Lua in editors

Open the repository root as the editor workspace so LuaLS, used by Zed, can find
`.luarc.json`. That file selects Lua 5.1 and declares the globals Redis injects:
`redis`, `KEYS`, `ARGV`, and `cjson`. Undefined-global diagnostics stay enabled for
every other name. `.luacheckrc` declares the same environment, and the gate checks
that the two configurations agree.

The LuaLS binary is not required by the gate; luacheck checks the scripts on every run.

Sources:

- https://github.com/mvdan/gofumpt
- https://golangci-lint.run/docs/welcome/install/
- https://staticcheck.dev/docs/running-staticcheck/cli/
- https://github.com/securego/gosec
- https://go.dev/security/vuln/
- https://luals.github.io/wiki/configuration/

## Check scope

Fill, proof relay, and settlement tests use local RPC/HTTP fixtures. The gate does
not submit funded transactions, load application secrets, deploy, or query live
LI.FI/Polymer services. To check a configured route against public services, run
the read-only `./bin/goif preflight -config config/testnet.json` command separately.
