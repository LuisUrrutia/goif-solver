# Verification record

Date: 2026-09-28. Local environment: Go 1.27.1, Docker 29.4.0, macOS arm64. Redis tests use `redis:8.10.2-alpine`.

| Command | Outcome |
| --- | --- |
| `bash scripts/check.sh` | Passed formatting, real Redis behavioral tests, build, vet, race detector, Python secret-loader tests, and shell checks |
| `go test ./...` within the check script | Passed with `TEST_REDIS_ADDR` set by the script |
| `go build ./...` | Passed |
| `go vet ./...` | Passed |
| `go test -race ./...` within the check script | Passed with real Redis and HTTP/RPC test servers |
| `go run ./cmd/goif preflight -config config/sepolia.json` | Passed against public Ethereum Sepolia and Base Sepolia RPCs and the current LI.FI development catalog |
| `bash scripts/preflight.sh` | Passed repeatable historical pilot audit: escrow status 2, global log index 2, matching prefixed hash, `isProven=true` |
| `bash scripts/smoke.sh` | Passed foreground observation startup, health, metrics, unauthenticated-control rejection, and CLI control read; process stopped |
| `go run github.com/yannh/kubeconform/cmd/kubeconform@v0.8.0 -strict -summary deploy/kubernetes.yaml` | Passed: 2 valid resources, 0 errors |
| `docker build -t goif-solver:dev .` | Passed Linux image build with CGO disabled and non-root scratch runtime |

Behavioral coverage includes concurrent duplicate discovery, independent discovery/execution clients, terminal deduplication, lease expiry/replacement, rejection of stale writes, one signer nonce owner, durable reservations after lease loss, signed-byte replay after uncertain broadcast, and control-version conflicts/node precedence. The full route test recreates the engine between persistent steps and observes exactly one destination fill and one origin claim.

Protocol tests use sanitized public fixtures. They verify the deployed batch-fill selector `0x7e7fc653`, full `OutputFilled` mandate decoding, block-global log index, and deployed prefixed Polymer hash `0x55253189e1a56e006fcbc7c0a7033109f5e332f2ba92c4914a0d99fb2b575a4c`. The prior manual order is historical evidence, not an unattended-run result.

## Funded test

The user authorized one new 1-USDC Ethereum Sepolia → Base Sepolia test and injected the LI.FI API key, Polymer API key, and configured solver key through the hidden-entry wizard into an owner-only file outside this checkout. The live runner was restricted to the exact order below. Broader signing remains unauthorized.

Preparation completed before the user funded the order:

- `python3 scripts/testnet.py register -config artifacts/live-test.json -authorize-registration` verified the local key/account match, authenticated solver identities, and supported-contract readback.
- `python3 scripts/testnet.py proof-check -config artifacts/live-test.json -order 0x98441c442077615b279a788283ecb399cb3bbb1e7e86103d375e5b64c9172bb3` returned Polymer job `1931912`, status `complete`, and 769 proof bytes without an on-chain transaction.
- `python3 scripts/testnet.py publish -config artifacts/live-test.json -publish-quotes` repeatedly published and read back the 1-USDC / 0.99-USDC standing quote. After funding, the publisher stopped and `python3 scripts/testnet.py withdraw -config artifacts/live-test.json` passed withdrawal and empty-range readback.
- The user confirmed the escrow deposit through MetaMask on lintent.org. The form used Testnet, Staging, Escrow, and Polymer. The page continued to show `No Quote`, so the input/output amounts were configured manually. This run proves authenticated standing-quote publication/readback, not quote selection by the frontend.

Execution command:

```sh
GOIF_LIVE_REDIS_URL=redis://127.0.0.1:16380/0 python3 scripts/testnet.py run -config artifacts/live-test.json -node funded-test -execute-testnet -order 0x1dcbd936ae5b3c4c64a3a50dfe0e907090959c503b692e24f1d434a1c8e82b50
```

The isolated live Redis instance uses AOF, `appendfsync always`, a persistent volume, and `noeviction`. Its journal must be preserved after signing. The solver discovered the order, waited for 12 origin confirmations, validated escrow/policy, used the existing destination token allowance, delivered 990000 raw USDC units, requested Polymer job `1931923`, and relayed the proof. LI.FI reports `Settled` at `2026-09-28T20:26:00Z`; the chain audit reports escrow status `2` and `isProven=true`.

The journal contains exactly three operations: one fill, one relay, and one claim. The worker autonomously recorded terminal state `settled` at `2026-09-28T20:29:57Z`, after the configured claim confirmations. It was then stopped cleanly; the persistent Redis journal was retained. Quote and worker processes are no longer running.

| Evidence | Value |
| --- | --- |
| Order | `0x1dcbd936ae5b3c4c64a3a50dfe0e907090959c503b692e24f1d434a1c8e82b50` |
| User escrow deposit | `0x2f099caa23f58d9e50543bedcd0915fd35393e13b7f2626b85f3b6d23426fb80` |
| Solver fill, Base Sepolia | `0xcca470288db6c94dc3c8d80c1a67c2a2092679e2ffc01955c885947dad79dc26` |
| Proof relay, Ethereum Sepolia | `0xfff899e2a6b82b2b963b544b98f1d831a7b3dab32fca926f645388218bc191ad` |
| Solver claim, Ethereum Sepolia | `0xd2d105b996b33fe300d9ece4160701230dae3871e44dfe645e1f03661026fa89` |
| Fill block / global log index | `47429258` / `47` |
| Polymer payload hash | `0x39939f8273866414fcc0c98cd5c62dffbe9671f465019c581578cc091c9c8e2f` |

Reconciliation command: `bin/goif preflight -config artifacts/live-test.json -order 0x1dcbd936ae5b3c4c64a3a50dfe0e907090959c503b692e24f1d434a1c8e82b50`. It passed against both public RPCs and the API.

| Solver balance | Before | After |
| --- | ---: | ---: |
| Sepolia USDC (raw units) | 61000000 | 62000000 |
| Base Sepolia USDC (raw units) | 39010000 | 38020000 |
| Sepolia native (wei) | 1999160066503732667 | 1998975969255856022 |
| Base Sepolia native (wei) | 1000000000000000000 | 999999446726505340 |

The solver received 1 USDC on origin and spent 0.99 USDC on destination. The 0.01-USDC spread excludes native gas; this does not demonstrate production profitability. The recipient was the user's connected wallet, `0x3333333351e46fe70247b7082ae505c85dabec7c`.

Public evidence: https://order-dev.li.fi/orders/status?onChainOrderId=0x1dcbd936ae5b3c4c64a3a50dfe0e907090959c503b692e24f1d434a1c8e82b50 and https://sepolia.basescan.org/tx/0xcca470288db6c94dc3c8d80c1a67c2a2092679e2ffc01955c885947dad79dc26 and https://sepolia.etherscan.io/tx/0xd2d105b996b33fe300d9ece4160701230dae3871e44dfe645e1f03661026fa89

## Deployment limits

`kubectl create --dry-run=client --validate=false -f deploy/kubernetes.yaml -o name` could not run because no Kubernetes API server is configured (`localhost:8080` refused the connection). Offline strict schema validation passed with kubeconform. The manifest is an example, not a tested cluster deployment. Production pricing, deep-reorg recovery, transaction fee replacement, automatic Redis failover safety, and additional network/custody/storage adapters remain outside the implemented development support matrix.

## Event architecture correction (2026-09-28)

The funded run above predates the event-source refactor. No signing key was loaded,
quote published, or new funded intent executed during this correction.

The corrected implementation passed:

- `bash scripts/check.sh`: Go formatting, all package tests with disposable real
  Redis, `go build ./...`, `go vet ./...`, dependency-boundary checks,
  `go test -race ./...`, four credential-runner tests, shell checks, and Lua lint
  and formatting (11 scripts; zero warnings/errors).
- `bash scripts/smoke.sh fresh` and `bash scripts/smoke.sh`: fresh and reused
  observation state, health/readiness, metrics, protected controls, clean shutdown.
- `bash scripts/preflight.sh`: public catalog, RPC chain identity, pinned contract
  runtimes, configured token decimals, governance fees, balances, and the historical
  pilot's settled/proven evidence. This command is read-only.
- `bash scripts/profile.sh`: pinned field-alignment audit and three runs of the
  full Open-event decode benchmark; measurements and intentional ABI exceptions
  are recorded in `architecture.md`.
- `docker build -t goif-solver:dev .`: the deployable image builds successfully.

New behavioral coverage includes off-chain application/control heartbeats,
subscription-before-snapshot ordering, durable-ack failures, reconnect deduplication,
confirmed-log replay, checkpoint CAS, deep-reorg rejection, full ABI round trips,
independent non-EVM executor dispatch, configured quote decimals, lazy application
construction, wrong-chain RPC exclusion, cancelable verification, timeout failover,
single-provider retries, deterministic-revert handling, and identical raw
transaction replay. The restarted escrow workflow still completes with one fill
and one claim against real Redis and local RPC/proof servers.

On-chain discovery currently monitors finalized log ranges over HTTP RPC; it does
not claim native chain WebSocket subscriptions. LI.FI's WebSocket does not provide
a durable replay cursor, and its bounded REST recovery is not a lossless guarantee.
Version-3 configuration and journal migration requirements are in `architecture.md`.
The funded Redis namespace and historical journal were not migrated or reset.
