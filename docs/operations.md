# Operations and configuration

## Configuration ownership

`config/testnet.json` is the public configuration for the complete multi-chain testnet deployment, including Ethereum Sepolia and Base Sepolia. It replaces the former chain-named profile; update explicit CLI paths and ConfigMap keys. Version 9 retains execution settings under `executions`, service instances under `providers` and `settlements`, and route bindings under `publications` and provider `routes`. It contains addresses, route limits, RPC fallbacks, and environment-variable names. It contains no private keys or API credentials. The pilot signer address is public historical evidence; replace it with the intended dedicated account before using another key.

Each route selects a signer, a named settlement backend, and exact input/output settlers, oracle pair, chain IDs, and tokens. Each signer has an explicit chain allowlist. The current execution strategy supports one configured input and one output with configured decimals (the sample uses six-decimal USDC), limit or exclusive-limit context, empty callbacks, and the verified escrow/Polymer contracts. It rejects Dutch auctions, Compact, zero or malformed amounts, foreign contracts/tokens, unsafe deadlines, nonzero current or scheduled governance fees, and amounts outside route limits.

The origin deposit must be present at the configured confirmation depth and still deposited at the latest block. The order's contract-computed identifier must match the advertised ID. There must be at least one hour between the fill deadline and expiry. A preflight compares settler/oracle runtimes against pinned hashes; changing addresses alone does not enable a different deployment.

`confirmations` is a route operator's block-depth policy, not a claim of consensus or economic finality. Ethereum and Base have different settlement/finality assumptions. The development values are 12 origin blocks and 20 destination blocks. Review them before any other deployment. A detected post-confirmation fill reorg stops the order for reconciliation.

A canonical execution policy is bound to the storage namespace. It includes contracts, assets, pricing, accounts, allowlists, signing bounds, and finality/proof semantics. Transport URLs, credential references, request rates, worker tuning, and unused definitions do not affect it. Nodes with incompatible execution policy cannot join the namespace. Drain intents and signer reservations before changing execution policy, retain the old journals, and use a fresh namespace. Never move a signer while its old namespace can still submit transactions.

## Secret injection

| Variable | Purpose | Needed for observation |
| --- | --- | --- |
| `GOIF_REDIS_URL` | Redis URL; use ACL/TLS for remote Redis | Only with `storage.kind: redis` |
| `GOIF_REDIS_PRIMARY_RUN_ID` | Public identity of the operator-approved Redis process | Required for persistent Redis |
| `SEPOLIA_RPC_URL` | Optional private origin RPC URL | No; public fallback |
| `BASE_SEPOLIA_RPC_URL` | Optional private destination RPC URL | No; public fallback |
| `GOIF_CONTROL_TOKEN` | Bearer token for read/control HTTP endpoints | Only for authenticated HTTP; required on non-loopback bind |
| `LIFI_API_KEY` | LI.FI solver API authentication | No for public reads |
| `POLYMER_API_KEY` | Polymer proof-service bearer token | No |
| `SOLVER_PRIVATE_KEY` | Local testnet signer | No |

Inject secrets through the process environment or a secret manager. Additional signer definitions can refer to different environment variables. Do not put secret values in command arguments, JSON config, shell history, checked-in files, screenshots, or issue comments. The service never prints effective secret configuration. Upstream response bodies and credential-bearing RPC URLs are omitted from errors.

The HTTP control token should contain at least 32 random characters. Keep the HTTP service on a private network; the application does not terminate TLS. Health and metrics are unauthenticated, while operational controls and order reads require `Authorization: Bearer ...`.

## Commands and signing authorization

`goif preflight -config config/testnet.json` is read-only and needs no signing key. It checks current catalog membership, RPC chain IDs, bytecode hashes, token decimals, zero current/pending governance fees, and account balances.

`goif register -config config/testnet.json -authorize-registration` signs the server-issued identity challenge for each missing account, merges the configured settlers into the account's registered sets, and reads identities/contracts back. It does not create an on-chain transaction. Run registration administratively, with no concurrent supported-contract editor; that API replaces whole sets and has no conditional-write version.

`goif run -config config/testnet.json -node worker-1` observes. Adding `-execute` authorizes unattended signing for all discovered orders admitted by that configuration. Adding `-publish-quotes` also authorizes standing quote publication and renewal. For a single funded test, pass `-intent` with the protocol-scoped key (`evm-escrow/0x…`), or set typed `{ "kind": "evm-escrow", "native_id": "0x…" }` entries in `intent_allowlist` in configuration. The allowlist applies both at discovery and execution and participates in the fleet policy digest. Each LI.FI publication checks registration for its own bound route; startup and on-chain execution do not depend on that service. These flags are deliberately absent from the local development scripts and Kubernetes example.

For a funded test, use a separately reviewed configuration, dedicated namespace, injected credentials, and explicit authorization for the funded order flow. The authorized development run completed; see [verification.md](verification.md) for its exact commands and evidence. Run the authenticated proof check for each intended account before funding a new test.

`goif proof-check -config config/testnet.json -intent evm-escrow/0x98441c442077615b279a788283ecb399cb3bbb1e7e86103d375e5b64c9172bb3 -history-provider lifi` requests and polls a Polymer proof for the already settled pilot. Without `-history-provider`, inspection reads the durable local record. With an explicitly selected history provider, it can inspect a historical public intent absent from this deployment. The command uses the audited route's selected backend and reports its route and completion status. This is an authenticated proof-service request, not an on-chain transaction; it checks account/method compatibility before the ten-minute funded-intent window begins.

`goif publish -config config/testnet.json -publish-quotes` runs only standing
quote publication. It checks inventory and renews after one third of the remaining
lifetime, independently of worker cadence. It uses the same policy binding,
renewable ownership, and global pause control as the service; it never loads a
signing key. Stopping it releases ownership without withdrawing shared quotes.
For a single-order pilot whose ID is not known during preparation, use a dedicated
publication-only namespace and stop that publisher before starting the sole
execution fleet with its exact `-intent` allowlist. Do not change the execution
allowlist inside an already bound namespace.

The task's credential wizard writes `~/.config/goif-solver/testnet.env` with mode `0600`. `python3 scripts/testnet.py` runs solver commands with those values, parses the file as literal assignments rather than shell code, rejects symlinks/shared permissions, and never prints secret values. Its `run` command requires an exact `-intent` argument. Build operations occur before injecting secrets into the solver process.

`goif withdraw -config config/testnet.json` withdraws each configured route and
verifies it through the publisher adapter. It requires the selected provider key
and approved coordination storage. It reports a busy binding while a live
publisher owns it. Prefer global pause for an active fleet: its owner performs the
withdrawal. For an explicit one-shot withdrawal, pause and stop publishers first.
Withdrawal does not cancel already funded intents.

`goif status -config config/testnet.json -intent evm-escrow/0x98441c442077615b279a788283ecb399cb3bbb1e7e86103d375e5b64c9172bb3` reads that order's local durable record. A historical public order is absent unless this deployment discovered it. The record contains its stage and persisted fill/proof coordinates; settled records also contain observed origin/destination USDC balances. These snapshots are not attributed balance deltas when other orders share the account.

## Quote and capital policy

Each route advertises one fixed-size input ticket equal to `max_input`. The selected `pricing.kind` determines output: `fixed-reserve` subtracts `pricing.min_margin` and requires equal decimals; `fixed-rate` applies `pricing.rate` in human asset units and supports different decimals. Both cap output at `max_output`. The exchange rate is truncated to 36 decimal places; integer token arithmetic avoids floating-point errors. Quotes expire after 60 seconds and are renewed by a separate fleet lease per publication binding. Low destination inventory causes a route withdrawal.

`min_margin` is an operator-supplied USDC cost reserve for this development route. It does not fetch native-token exchange rates or prove profitability. EIP-1559 gas limits and fee caps bound each transaction, and simulation/native-balance checks run before signing. Base's additional L1 data fee is not converted into the USDC quote. There is no automatic rebalancer, dynamic market pricing, price feed, cumulative spending budget, or token inventory reservation across multiple accepted orders. Do not describe this policy as production pricing. Actual fill simulation and token balances stop spending beyond available inventory, but a quote is not a guarantee of available capital for unlimited simultaneous requests.

Quote publication uses the primary LI.FI API. Its API does not accept Redis fencing tokens, so lease fencing cannot revoke an HTTP request already sent by a former publisher. Requests have bounded deadlines and quotes have short expiry; a global pause stops new worker steps and causes withdrawal on the next quote cycle. A pause cannot retract an already signed transaction. Stop publication and wait for withdrawal/expiry before treating liquidity as unavailable to new users.

## Cluster controls

`goif control -config config/testnet.json` reads the current operational control document. Version zero is the default, with discovery/execution enabled by process mode and no node overrides.

`goif control -config config/testnet.json -control-file config/control-paused.json` applies the example version-one global pause. A stale version is rejected. Create each subsequent document with exactly the current version plus one.

The authenticated `GET /control` and `PUT /control` endpoints expose the same state. PUT takes `{ "expected_version": 0, "control": { "version": 1, "paused": true, "nodes": {} } }`. Each node override has `paused` and `workers`, keyed by its exact node ID. Precedence is global pause, then node pause/concurrency reduction, then the process's startup worker limit. An override cannot increase the configured maximum of 32 workers.

Workers read control before each scheduling cycle. Updates do not cancel an already running step. The signer-recovery loop continues reconciling previously authorized transactions during a pause, so their reservations do not remain stranded. Any eligible pod can own a configured source. Observation pods discover and persist intents, but start neither execution workers nor signer recovery. Redis records, not the discoverer's memory, own the work. Test `TestDuplicateDiscoveryAndIndependentExecutor` demonstrates that a separate client can execute a discovered order.

Endpoints:

| Endpoint | Access | Meaning |
| --- | --- | --- |
| `GET /healthz` | Public | Solver engine is running |
| `GET /readyz` | Public | Engine is running and coordination storage passes its safety checks |
| `GET /metrics` | Public | Process counters, owner gauges, shared queue and pending-signer age |
| `GET /control`, `PUT /control` | Bearer | Versioned fleet and node controls |
| `GET /intents/{id}` | Bearer | One durable order record |

Readiness does not continuously attest to RPC, API, or proof-service health. Structured logs expose deferred orders and cycle failures. Order IDs correlate worker logs; transaction-preparation logs include the operation, transaction hash, chain, and nonce. Do not put order IDs into metric labels.

## Kubernetes

Build the image with `docker build -t goif-solver:dev .`. `deploy/kubernetes.yaml` is an observation-mode example with two replicas, a non-root user, a read-only filesystem, bounded resources, health probes, and no Kubernetes API token. It also includes a disruption budget, a zero-unavailable rolling update, topology spread preferences, and an ingress policy. The manifests are schema-validated; this task did not deploy them to a cluster.

Provide a `goif-solver-config` ConfigMap whose `testnet.json` key contains the reviewed configuration. Set `listen` to `0.0.0.0:8080` for pod probes. Provide `goif-solver-secrets` through your cluster's secret mechanism; include `GOIF_REDIS_URL`, `GOIF_REDIS_PRIMARY_RUN_ID`, and a sufficiently long `GOIF_CONTROL_TOKEN`. The manifest intentionally contains no secret values. Publish the image through your own authorized registry workflow and pin its digest before deployment.

Use the persistent single-primary profile in `deploy/redis.conf` and the
[Redis recovery procedure](redis-recovery.md). Production startup requires the
externally approved `GOIF_REDIS_PRIMARY_RUN_ID`; a new physical connection to a
different process is rejected even after all solver pods restart. Durability or
identity changes stop execution. AOF every-second persistence and automatic
asynchronous promotion do not satisfy this profile. The current client uses a
single direct endpoint, without Sentinel/Cluster topology discovery.

See [cluster remediation](cluster-remediation.md) for bootstrap resources, monitoring, capacity limits, and regression evidence.

Pods use their Kubernetes names as node IDs. API/RPC request budgets are per process; divide the provider's fleet allowance across replicas. The quote lease and signer reservations are cluster-wide. Give different independent fleets different namespaces **and different signer accounts**. Never let independent namespaces share a signer.

## Recovery and extension

The state sequence is `discovered → validated → approved → filled → proven → settled`. While filled, the selected settlement backend persists its own resumable checkpoint. Policy rejections are terminal. Network/proof/transaction uncertainty leaves the record durable and schedules a bounded-backoff retry. No replacement transaction is signed automatically. A reverted transaction, changed mandate, conflicting immutable discovery, corrupt journal, or deep reorg requires diagnosis.

Redis order state, signer reservations, and signed transaction bytes must survive restarts together. A sender with an outstanding operation cannot prepare another operation until a canonical receipt reaches configured depth. A separate loop recovers reservations even when an order expires or workers are paused. Proof-request interruption before job persistence can create another provider job on retry; it cannot create another fill. The proof provider offers no verified idempotency token for that call.

New custody providers register an `evm.CustodyFactory` and implement `evm.Signer` while retaining transaction journaling and nonce coordination. New intent sources implement `intent.Source`; a streaming source owns its connection, heartbeat, replay, and backpressure behavior. A polling-only protocol can implement that same boundary without changing the coordinator. New execution strategies implement `solver.Executor` and register their own kind at application composition. Neutral `quote.Publisher` implementations own publication schemas. Neither core execution nor preflight imports LI.FI.

The sample `sources` enables LI.FI WebSocket and on-chain escrow logs. See `architecture.md` for checkpoint/reorg behavior, bounded REST recovery, source configuration, and version-9 configuration migration. Chain `rpcs` entries are attempted lazily; optional `SEPOLIA_FALLBACK_RPC_URL` and `BASE_SEPOLIA_FALLBACK_RPC_URL` can supply independent providers. New settlement strategies must validate their own contracts, encoding, finality, token behavior, and proof semantics; SVM and TVM cannot reuse EVM by changing chain IDs.

Redis operations implement `coordination.Backend`. A future backend must preserve atomic fencing, persistent reservations, and immutable journals. Lua scripts are external embedded files checked by the normal test script. SQLite and file backends remain future work; the memory backend is limited to development.

## Local memory backend

`config/development.json` selects `storage.kind: memory` and `development: true`. Run it with `scripts/dev.sh`. Each process owns independent volatile records, leases, checkpoints, and controls; exiting the process discards them. The development configuration has no networks or sources, so startup needs neither Docker nor provider credentials. Add explicitly configured sources and routes to observe real events. Development mode rejects `-execute` because signed transaction recovery needs durable storage.

Use authenticated HTTP to inspect or control that running process. The `status` and `control` CLI commands target persistent storage and reject memory mode instead of opening an unrelated empty instance.

## Settlement configuration

The configuration defines named `settlements`, for example `polymer-testnet` with `kind: "polymer"` and `settings` containing `api`, `key_env`, `request_method`, `query_method`, and optional `requests_per_second`. An execution route selects it with `settlement: "polymer-testnet"`. Root Polymer credentials and chain-specific LI.FI instances are not accepted.

Only backends selected by a route are constructed. Their credentials are resolved
for execution or an explicit proof-access diagnostic, never for ordinary public
preflight. An unused backend definition needs no injected secret. The development
sample has no routes or settlement backends and needs no Polymer service.

A new backend must implement its own oracle validation and resumable verification;
changing `kind` alone cannot make a deployed oracle support another proof system.
Before changing a route's backend, drain and reconcile its active intents. The
sample's `goif-intents-v9` namespace isolates the new checkpoint format; retain old
journals and their matching binary/configuration until that reconciliation finishes.

## Preflight report format

Preflight reports use `network` identifiers such as `eip155:11155111` and `height`
instead of numeric `chain_id` and `block` fields. Balance entries identify their
`network`, `account`, and `asset`; native amounts use `native_base_units` instead
of an EVM-specific unit name. Amounts remain decimal strings.

Historical intent reports expose a string `intent_id`, `kind`, `route`, API status,
and `settlement.verified` / `settlement.reference`. EVM receipt and escrow fields
now live under `details`: `destination_chain`, `fill_block`, `global_log_index`,
`fill_transaction`, and `escrow_status`. The duplicate top-level `proven` field is
removed. Update consumers of the diagnostic JSON accordingly. These reports are adapter-independent; EVM-specific fields remain under `details`.

## OIF HTTP API

Optional `apis` entries select independently authenticated inbound adapters. The installed OIF adapter serves all four pinned `/v1` endpoints for the user-open subset. See [OIF compatibility](oif-compatibility.md) for its exact wire contract, credentials, supported authorization, and examples.

## Version 9 persistence cutover

The current version uses protocol-scoped durable keys (`evm-escrow/<native-id>` for the
current adapter). Source identity is deliberately absent: WebSocket and chain
logs must deduplicate the same intent. The authenticated `/intents/{id}` endpoint
and `status -intent` take this canonical key. Execution allowlists contain a protocol kind and native identifier.

Records include immutable creation time and transition update time. Redis uses server time; memory uses the process clock. Duplicate discovery does not reset timestamps.

Transaction attempts persist a codec and adapter-owned JSON metadata instead
of a shared EVM nonce. Finality or verified expiry evidence is written atomically
with reservation release. Bytes and outcomes are immutable per attempt; another
attempt needs a distinct operation key and the previous reservation must have
been resolved. EVM continues to replay only identical signed bytes and never
uses expiry-based replacement.

Drain older work with its original binary, retain that namespace and its journals, and start version 9 in a new namespace. Do not copy ready queues or transaction hashes into the new namespace. The sample uses `goif-intents-v9`. No migration or deletion of existing durable state runs automatically.
