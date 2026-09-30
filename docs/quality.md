# Quality gate

Run `make check` before committing or reporting work complete. GitHub
Actions runs the same command on pushes and pull requests, including forks. It
uses read-only repository access and no application secrets, cancels superseded
runs, and stops after 30 minutes.

Install the [development tools](../README.md#development-tools) first. Make uses
the executables on `PATH`; it does not install tools or change their versions.
GitHub Actions prepares the documented versions before invoking the same targets.

`make test-integration` starts two isolated Redis instances to exercise Lua
scripts, leases, concurrent clients, primary replacement, and durability checks.
Both use `deploy/redis.conf`. Their anonymous test endpoints bind only to host
loopback, and the containers are removed when the target exits, including on
failure. Production keeps protected mode enabled and requires deployment
credentials. Running the development service itself needs only Go and Bash.

| Check | Tool or command |
| --- | --- |
| Import formatting | goimports |
| Go formatting | gofumpt |
| Error handling and unused code | golangci-lint: errcheck, ineffassign, unused |
| Static analysis | staticcheck and `go vet ./...` |
| Security analysis | gosec |
| Reachable vulnerabilities | govulncheck with the current vulnerability database |
| Behavior | `go test ./...` against isolated real Redis and memory |
| Build | `make build` |
| Race detection | `go test -race -count=1 ./...` |
| Lua | luacheck and StyLua |
| Scripts and workflows | Bash syntax and ShellCheck for scripts; actionlint for every workflow in `.github/workflows/` |
| Kubernetes manifests | kubeconform with strict Kubernetes 1.37.0 schemas at a pinned revision |
| Runtime smoke | `make smoke` runs both fresh and quick startup |

Staticcheck, vet, and gosec each run once, outside golangci-lint. Format checks are
read-only. Run `make fmt` to apply Go and Lua formatting.

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
every other name. `.luacheckrc` declares the same environment for command-line
linting.

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
