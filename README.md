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

The default memory setup needs no environment variables. If you want to use the
protected `/control` and `/intents` endpoints, configure `GOIF_CONTROL_TOKEN`
through the [environment setup](#environment-variables) before starting.

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

3. [Configure and load environment variables](#environment-variables) if needed.
   You can skip this step for the default memory run without protected controls.

4. Start it with the development configuration and a local node name:

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

The sample contains a test account. Replace it with your dedicated
testnet account. The [operations guide](docs/operations.md) covers read-only
preflight checks, registration, quotes, and execution.

### Environment variables

The binary and `scripts/dev.sh` read the process environment; neither loads `.env`
automatically. [.env.example](.env.example) lists the names used by the sample
configurations, with empty values. Custom configurations can use other names.

For a local setup, run the following commands in **Bash**. If you use Fish, enter
Bash with `bash` first.

1. Copy the template and restrict access to your local file. `cp -n` preserves an
   existing `.env`:

   ```sh
   cp -n .env.example .env
   chmod 600 .env
   ```

2. Edit `.env` and fill in the values your mode needs, using the table below.
   Keep values single-quoted. The file is loaded as shell code, so use your own
   file and keep secrets out of command history. `.env` is ignored by Git.

3. Export the values, then start the solver in that same terminal:

   ```sh
   set -a
   . ./.env
   set +a
   ```

For the local memory run, you can leave every value empty. Set only
`GOIF_CONTROL_TOKEN` if you want authenticated controls. For deployed workers,
inject values through your deployment's secret manager instead of a local file.

| Variable | When to set it |
| --- | --- |
| `GOIF_CONTROL_TOKEN` | A random token of at least 32 characters for protected controls; required when listening outside loopback |
| `GOIF_REDIS_URL` | Redis connection URL for `config/testnet.json`, including observation mode |
| `GOIF_REDIS_PRIMARY_RUN_ID` | Approved Redis process identity, required before starting a Redis-backed solver |
| `SEPOLIA_RPC_URL`, `BASE_SEPOLIA_RPC_URL` | Optional overrides for the public RPC URLs in the testnet configuration |
| `SEPOLIA_FALLBACK_RPC_URL`, `BASE_SEPOLIA_FALLBACK_RPC_URL` | Optional additional RPC providers for failover |
| `LIFI_API_KEY` | LI.FI registration, quote publication, and withdrawal; not required for public discovery |
| `POLYMER_API_KEY` | Execution with Polymer or authenticated proof checks |
| `SOLVER_PRIVATE_KEY` | Transaction signing and new identity challenges; must match the configured signer address |

For Redis, first provision the primary as described in the
[recovery guide](docs/redis-recovery.md#initial-approval), set `GOIF_REDIS_URL`,
and load the environment. Inspect it with:

```sh
./bin/goif storage-check -config config/testnet.json
```

Review the reported `primary_run_id` before setting `GOIF_REDIS_PRIMARY_RUN_ID`,
then reload `.env`. Do not automatically adopt a new identity after a Redis
restart or restore; follow the recovery guide. With Redis configured, start
testnet observation:

```sh
./bin/goif run -config config/testnet.json -node local-testnet
```

The optional OIF HTTP API needs its own API token and quote-signing key; see
[OIF configuration](docs/oif-compatibility.md#enable-the-adapter) when enabling it.

### Execution and deployment

Funded execution requires Redis, the `-execute` flag, a signer chain allowlist,
and `signing_enabled` on each used chain. Each replica must share the same
execution policy and storage namespace, with a distinct node ID.

The supported Redis setup is a persistent standalone primary with AOF,
`appendfsync always`, and `noeviction`. The solver stops if the approved primary
identity changes; resuming requires reconciliation. Read the
[Redis recovery guide](docs/redis-recovery.md) before deploying workers. Kubernetes
examples are in [deploy/](deploy/); deployment assumptions and capacity limits are
in [the deployment guide](docs/deployment.md).

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
- [Operations](docs/operations.md): route settings, quotes, commands, and controls.
- [Deployment](docs/deployment.md): Kubernetes resources, monitoring, and capacity planning.
- [Execution and recovery](docs/execution-design.md): worker ownership, signed transaction journals, and failure handling.
- [Redis recovery](docs/redis-recovery.md): primary approval, restarts, and reconciliation.
- [OIF compatibility](docs/oif-compatibility.md): API endpoints, supported variants, and upstream spec revision.
- [Quality gate](docs/quality.md): prerequisites, checks, and editor configuration.

## License

This repository does not currently include a license.
