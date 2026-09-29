# goif-solver

A Go intent solver with a protocol-independent coordinator and an initial EVM escrow adapter for **Ethereum Sepolia USDC → Base Sepolia USDC**. LI.FI WebSocket notifications and confirmed on-chain `Open` events feed durable intent processing. Redis coordinates fleet workers and signed transaction recovery; the escrow adapter fills and claims through a route-selected settlement backend. The initial backend verifies and relays Polymer proofs.

An optional inbound OIF HTTP adapter supports exact-input user-open quotes and submissions on configured escrow routes. Its four endpoints follow a pinned OpenAPI snapshot; see [OIF compatibility](docs/oif-compatibility.md) for configuration and supported variants.

The default mode observes. Execution requires `-execute`, an explicit signer chain allowlist, and `signing_enabled` on each used chain. Networks, RPC pools, contracts, tokens, and decimals come from configuration. HTTP clients are lazy, with bounded retries and provider failover.

## Current evidence

- Real Redis tests cover duplicate discovery, separate discovery/execution clients, lease expiry, stale-worker fencing, signer reservations, and versioned controls.
- An end-to-end test uses real `ethclient`, signed transactions, Redis, and HTTP/RPC test servers. It restarts the engine between steps and reaches settlement with one fill and one claim.
- Read-only public testnet checks verify chain IDs, catalog entries, deployed runtime hashes, USDC decimals, governance fees, and configured account balances.
- The historical pilot fixture reproduces the deployed fill selector, event decoding, global log index, and Polymer proof hash.
- Before the event-source refactor, one authorized 1-USDC test completed unattended through fill, Polymer relay, and settlement after the user funded its escrow. See the [verification record](docs/verification.md) for transactions, balances, and limits. Credentials remain outside the repository. The refactor has not had another funded run.

## Run locally

Requires Go 1.27.1 and Bash. The development configuration uses process-local memory, no providers, and no RPCs. It listens on `127.0.0.1:8080`. It starts idle until event sources and routes are configured.

Fresh development run, rebuilding the generated binary:

```sh
bash scripts/dev.sh fresh
```

Quick development run:

```sh
bash scripts/dev.sh quick
```

Stop with Ctrl-C. Memory state disappears when the process exits, so both starts use empty storage. Development mode rejects funded execution. Use `config/testnet.json` and a durable Redis deployment for a coordinated fleet; that configuration selects LI.FI WebSocket and EVM log sources separately.

## Verify

```sh
bash scripts/check.sh
bash scripts/smoke.sh
bash scripts/preflight.sh
bash scripts/profile.sh
docker build -t goif-solver:dev .
```

`check.sh` runs the full [quality gate](docs/quality.md): pinned Go formatters, golangci-lint, staticcheck, vet, gosec, govulncheck, build, tests, race detection, real Redis integration, Lua checks, workflow/script lint, architecture boundaries, and both memory runtime smoke modes. `AGENTS.md` requires this gate after implementation; GitHub Actions uses the same entry point. Plain `go test` skips Redis integration tests unless `TEST_REDIS_ADDR` is set.


## Documentation

- [Event architecture and migration](docs/architecture.md): source delivery, protocol boundaries, lazy RPC policy, state migration, and measured layout/benchmark evidence.

- [Operations and configuration](docs/operations.md): commands, secrets, quote policy, control versions, Kubernetes, and recovery.
- [Execution design and threat model](docs/execution-design.md): ownership, fencing, signed transaction recovery, and limits.
- [Protocol evidence and support matrix](docs/protocol-research.md): current LI.FI sources, deployed ABIs, upstream OIF reference, and historical testnet evidence.
- [Verification record](docs/verification.md): exact checks, funded-test evidence, and deployment limits.

Production dependencies include `go-ethereum`, `go-redis/v9`, `gorilla/websocket`, `zap`, and `x/sync`. Versions are pinned in `go.mod` and `go.sum`.
