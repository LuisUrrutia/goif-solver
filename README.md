# goif-solver

A Go solver for the LI.FI development route **Ethereum Sepolia USDC → Base Sepolia USDC**. It polls orders, validates escrow and route policy, coordinates workers through Redis, journals signed transactions before broadcast, fills on Base Sepolia, requests and relays Polymer proofs, and finalises on Ethereum Sepolia.

The default mode observes and validates. It does not load signing keys, publish quotes, or send transactions. Mainnet signing is rejected by the local signer and transaction sender.

## Current evidence

- Real Redis tests cover duplicate discovery, separate discovery/execution clients, lease expiry, stale-worker fencing, signer reservations, and versioned controls.
- An end-to-end test uses real `ethclient`, signed transactions, Redis, and HTTP/RPC test servers. It restarts the engine between steps and reaches settlement with one fill and one claim.
- Read-only public testnet checks verify chain IDs, catalog entries, deployed runtime hashes, USDC decimals, governance fees, and configured account balances.
- The historical pilot fixture reproduces the deployed fill selector, event decoding, global log index, and Polymer proof hash.
- A new funded unattended settlement has **not** been run. LI.FI/Polymer credentials and the solver key are not supplied by this repository.

## Run locally

Requires Go 1.27.1, Docker, Python 3, Bash, curl, and ripgrep. The scripts use an isolated, disposable observation Redis container on `127.0.0.1:16379`. The service listens on `127.0.0.1:8080`.

Fresh observation run, removing only the labeled development Redis container and generated binary:

```sh
bash scripts/dev.sh fresh
```

Quick observation run:

```sh
bash scripts/dev.sh quick
```

Stop the foreground solver with Ctrl-C. The observation Redis container remains available for the next quick run. These scripts cannot enable signing. Use a separate durable Redis deployment and namespace for funded work; never reset its state while transactions or orders may be active.

## Verify

```sh
bash scripts/check.sh
bash scripts/smoke.sh
bash scripts/preflight.sh
docker build -t goif-solver:dev .
```

`check.sh` checks formatting, runs `go test ./...`, `go build ./...`, `go vet ./...`, and `go test -race ./...` against a temporary real Redis instance. Plain `go test` skips Redis integration tests unless `TEST_REDIS_ADDR` is set. `smoke.sh` starts the observation service, checks health/metrics/authentication and CLI control, and stops the process. It leaves its log in ignored `artifacts/smoke.log`.

## Documentation

- [Operations and configuration](docs/operations.md): commands, secrets, quote policy, control versions, Kubernetes, and recovery.
- [Execution design and threat model](docs/execution-design.md): ownership, fencing, signed transaction recovery, and limits.
- [Protocol evidence and support matrix](docs/protocol-research.md): current LI.FI sources, deployed ABIs, upstream OIF reference, and historical testnet evidence.
- [Verification record](docs/verification.md): exact checks and remaining funded-test prerequisites.

The only nonstandard production dependencies are `go-ethereum` for EVM encoding/RPC/signing, `go-redis/v9` for Redis, and `zap` for structured logs. Versions are pinned in `go.mod` and `go.sum`; the initial versions were older than the requested three-day release cooldown.
