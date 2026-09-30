# goif-solver

A Go solver for cross-chain intents. In the escrow flow implemented here, a user
locks funds on one chain and asks to receive assets on another. The solver pays
from its destination-chain balance, proves the fill, and claims the user's deposit
on the origin chain.

The included configuration covers **USDC from Ethereum Sepolia to Base Sepolia**,
with LI.FI discovery and Polymer settlement proofs. The project is intended for
development and testnet use. It starts in observation mode; executing intents
requires explicit configuration and authorization.

Discovery, execution, quote publication, and settlement are separate adapters.
You can use on-chain discovery without configuring LI.FI. The coordinator handles
duplicate events, work scheduling, and recovery independently of those adapters.

## Try it locally

You need **Go 1.27.1** (the version used in CI). The helper script uses **Bash**,
and the readiness check below uses **curl**. Run these commands from the
repository root.

To build and start the service in one step:

```sh
bash scripts/dev.sh quick
```

Or run the same steps manually:

1. Create the directory for the binary:

   ```sh
   mkdir -p bin
   ```

2. Build the solver:

   ```sh
   go build -o bin/goif ./cmd/goif
   ```

3. Start it with the development configuration and a local node name:

   ```sh
   ./bin/goif run -config config/development.json -node local-development
   ```

Both paths start the service at `127.0.0.1:8080` with
[the development configuration](config/development.json). It needs no Redis,
RPC endpoints, wallet, or API keys. With no event sources or routes configured,
the service starts idle.

In another terminal, check that it is ready:

```sh
curl --fail --silent --show-error http://127.0.0.1:8080/readyz
```

The response is `ready`. Health and Prometheus metrics are available at `/healthz`
and `/metrics` on the same listener.

Stop with Ctrl-C. This configuration keeps state in memory, discards it on exit,
and rejects funded execution. To remove the generated binary before rebuilding,
use `bash scripts/dev.sh fresh`.

## What works today

| Area | Implemented support |
| --- | --- |
| Execution | EVM escrow intents on configured routes and verified contract deployments |
| Off-chain discovery | LI.FI WebSocket notifications, with bounded REST reconciliation after reconnects |
| On-chain discovery | Confirmed escrow `Open` events, scanned through HTTP RPC with saved checkpoints |
| Settlement | Polymer proofs for the supported EVM contract profile |
| OIF HTTP API | Optional exact-input user-open quotes and submissions, intent status, and supported assets |
| Storage | Memory for one development process; Redis for persistent state and coordination across replicas |

Both discovery sources can run together. An intent seen through WebSocket and
chain logs becomes one record. The on-chain source uses `eth_getLogs`; it does not
use chain WebSocket subscriptions. LI.FI recovery has a finite window and cannot
guarantee replay after a long outage.

The HTTP API implements a [documented subset](docs/oif-compatibility.md) of the
pinned Open Intents Framework (OIF) specification. It is disabled in the sample
configurations. SVM and TVM execution, exact-output quotes, and partial fills are
not implemented.

Pricing uses configured fixed rates or reserves. There is no dynamic market
pricing, automatic rebalancing, or inventory reservation across accepted intents.
See [quote and capital policy](docs/operations.md#quote-and-capital-policy) before
publishing offers.

## Configure a testnet solver

Start with [config/testnet.json](config/testnet.json). It defines both networks,
their RPC pools, the USDC route, signers, sources, and provider bindings. Networks,
contracts, assets, and token decimals come from configuration. RPC clients connect
on demand and support bounded retries and endpoint failover.

The sample contains a historical test account. Replace it with your dedicated
testnet account and supply credentials through environment variables or a secret
manager. The [operations guide](docs/operations.md) covers secret injection,
read-only preflight checks, registration, quotes, and execution.

Funded execution requires Redis, the `-execute` flag, a signer chain allowlist,
and `signing_enabled` on each used chain. Each replica must share the same
execution policy and storage namespace, with a distinct node ID.

The supported Redis setup is a persistent standalone primary with AOF,
`appendfsync always`, and `noeviction`. The solver stops if the approved primary
identity changes; resuming requires reconciliation. Read the
[Redis recovery guide](docs/redis-recovery.md) before deploying workers. Kubernetes
examples are in [deploy/](deploy/); deployment assumptions and capacity limits are
in [the cluster guide](docs/cluster-remediation.md).

## Development and contributions

For a first pass at the Go tests:

```sh
go test ./...
```

Redis integration tests skip unless `TEST_REDIS_ADDR` is set. Before submitting a
change, run the full gate, which provisions disposable Redis instances:

```sh
bash scripts/check.sh
```

The gate needs additional tools and a running Docker daemon; see the
[prerequisites and checks](docs/quality.md). It runs formatting checks, static and
security analysis, builds, tests with race detection, Redis integration tests,
Lua and script linting, architecture checks, and runtime smoke tests. GitHub Actions
uses the same command. No wallet or application credentials are needed.

For performance work, `bash scripts/profile.sh` records struct-layout diagnostics
and event-decoding and route-admission benchmarks. Keep changes focused, describe
the behavior they change, and include the relevant test results in your pull
request. Start with the [architecture guide](docs/architecture.md) when adding an
adapter.

For questions or bug reports, use https://github.com/LuisUrrutia/goif-solver/issues.
Include the commit, reproduction steps, and relevant logs with credentials removed.

## Further reading

- [Architecture](docs/architecture.md): adapter boundaries, event delivery, and configuration changes.
- [Execution and recovery](docs/execution-design.md): worker ownership, signed transaction journals, and failure handling.
- [OIF compatibility](docs/oif-compatibility.md): API endpoints, supported variants, and upstream spec revision.
- [Verification record](docs/verification.md): dated checks, historical funded-test transactions, and the limits of that evidence.
- [Protocol research](docs/protocol-research.md): LI.FI integration, deployed contracts, and upstream references.

## License

This repository does not currently include a license.
