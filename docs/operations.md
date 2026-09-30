# Operations and configuration

## Configuration ownership

[config/testnet.json](../config/testnet.json) configures Ethereum Sepolia and Base
Sepolia. Configuration version 9 keeps execution settings under `executions`,
service instances under `providers` and `settlements`, and route bindings under
`publications` and provider `routes`. The file contains public addresses, route
limits, RPC fallbacks, and environment-variable names. Replace the sample signer
address with your dedicated account; private keys and API credentials belong in
the process environment.

Each route selects a signer, a named settlement backend, and exact input/output settlers, oracle pair, chain IDs, and tokens. Each signer has an explicit chain allowlist. The current execution strategy supports one configured input and one output with configured decimals (the sample uses six-decimal USDC), limit or exclusive-limit context, empty callbacks, and the verified escrow/Polymer contracts. It rejects Dutch auctions, Compact, zero or malformed amounts, foreign contracts/tokens, unsafe deadlines, nonzero current or scheduled governance fees, and amounts outside route limits.

The origin deposit must be present at the configured confirmation depth and still deposited at the latest block. The order's contract-computed identifier must match the advertised ID. There must be at least one hour between the fill deadline and expiry. A preflight compares settler/oracle runtimes against pinned hashes; changing addresses alone does not enable a different deployment.

`confirmations` is a route operator's block-depth policy, not a claim of consensus or economic finality. Ethereum and Base have different settlement/finality assumptions. The development values are 12 origin blocks and 20 destination blocks. Review them before any other deployment. A detected post-confirmation fill reorg stops the order for reconciliation.

A canonical execution policy is bound to the storage namespace. It includes contracts, assets, pricing, accounts, allowlists, signing bounds, and finality/proof semantics. Transport URLs, credential references, request rates, worker tuning, and unused definitions do not affect it. Nodes with incompatible execution policy cannot join the namespace. Drain intents and signer reservations before changing execution policy, retain the old journals, and use a fresh namespace. Never move a signer while its old namespace can still submit transactions.

## Secret injection

The [README environment setup](../README.md#environment-variables) lists the sample
variables and local loading commands. The solver reads its process environment;
it does not load `.env` automatically. Deployments should inject values through
their secret manager. Additional signer and provider definitions can refer to
different environment variables.

Keep secret values out of command arguments, JSON configuration, shell history,
checked-in files, screenshots, and issue comments. The service does not print
effective secret configuration or include credential-bearing RPC URLs and upstream
response bodies in errors.

Use a control token with at least 32 random characters. A non-loopback listener
requires it. Keep the HTTP service on a private network and terminate TLS at the
gateway. Health and metrics are unauthenticated; operational controls and intent
reads require `Authorization: Bearer ...`.

## Commands and signing authorization

`./bin/goif preflight -config config/testnet.json` is read-only and needs no signing key. It checks current catalog membership, RPC chain IDs, bytecode hashes, token decimals, zero current/pending governance fees, and account balances.

`./bin/goif register -config config/testnet.json -authorize-registration` signs the server-issued identity challenge for each missing account, merges the configured settlers into the account's registered sets, and reads identities/contracts back. It does not create an on-chain transaction. Run registration administratively, with no concurrent supported-contract editor; that API replaces whole sets and has no conditional-write version.

`./bin/goif run -config config/testnet.json -node worker-1` observes. Adding `-execute` authorizes unattended signing for all discovered orders admitted by that configuration. Adding `-publish-quotes` also authorizes standing quote publication and renewal. For a single funded test, pass `-intent` with the protocol-scoped key (`evm-escrow/0x…`), or set typed `{ "kind": "evm-escrow", "native_id": "0x…" }` entries in `intent_allowlist` in configuration. The allowlist applies both at discovery and execution and participates in the fleet policy digest. Each LI.FI publication checks registration for its own bound route; startup and on-chain execution do not depend on that service. These flags are deliberately absent from the local development scripts and Kubernetes example.

For a funded test, use a reviewed configuration, dedicated namespace, injected credentials, and an exact intent allowlist. Run an authenticated proof check for the intended account before funding the intent.

Set `INTENT_ID` to the canonical `evm-escrow/<native-id>` of the intent you want to
inspect. `./bin/goif proof-check -config config/testnet.json -intent "$INTENT_ID"`
requests a proof through the route's settlement backend. It uses authenticated
proof-service access without sending an on-chain transaction. By default, it
reads the durable local record. Add `-history-provider lifi` to inspect a public
intent that this deployment has not discovered.

`./bin/goif publish -config config/testnet.json -publish-quotes` runs only standing
quote publication. It checks inventory and renews after one third of the remaining
lifetime, independently of worker cadence. It uses the same policy binding,
renewable ownership, and global pause control as the service; it never loads a
signing key. Stopping it releases ownership without withdrawing shared quotes.
For a single-intent test whose ID is not known during preparation, use a dedicated
publication-only namespace and stop that publisher before starting the sole
execution fleet with its exact `-intent` allowlist. Do not change the execution
allowlist inside an already bound namespace.

LI.FI publication readback proves that the server stored an offer. By default,
integrator quote requests select from whitelisted solvers. Before LI.FI enables
your solver for that selection, include its registered address in
`intent.metadata.exclusiveFor` when calling `/quote/request` or
`/api/v1/integrator/quote/request`. In https://lintent.org/, select the matching
environment and Escrow input, enter the active offer's solver address in
**Exclusive**, enable **Lock Exclusive**, and leave **1:1 demo** unchecked.
The lock checkbox defaults to off; demo mode suppresses the solver selector.
The offer's own `exclusiveFor` field does not supply the request-side selector.
Public selection without that selector requires LI.FI onboarding. Sources:
https://docs.li.fi/lifi-intents/for-solvers/testing-integration,
https://order-dev.li.fi/docs, and
https://github.com/lifinance/lintent/blob/main/src/lib/screens/IssueIntent.svelte.

`./bin/goif withdraw -config config/testnet.json` withdraws each configured route and
verifies it through the publisher adapter. It requires the selected provider key
and approved coordination storage. It reports a busy binding while a live
publisher owns it. Prefer global pause for an active fleet: its owner performs the
withdrawal. For an explicit one-shot withdrawal, pause and stop publishers first.
Withdrawal does not cancel already funded intents.

`./bin/goif status -config config/testnet.json -intent "$INTENT_ID"` reads the local
durable record. The intent must have been discovered by this deployment. The
record contains its stage and persisted fill/proof coordinates; settled records
also contain observed origin/destination USDC balances. These snapshots are not
attributed balance deltas when other intents share the account.

## Quote and capital policy

Each route advertises one fixed-size input ticket equal to `max_input`. The selected `pricing.kind` determines output: `fixed-reserve` subtracts `pricing.min_margin` and requires equal decimals; `fixed-rate` applies `pricing.rate` in human asset units and supports different decimals. Both cap output at `max_output`. The exchange rate is truncated to 36 decimal places; integer token arithmetic avoids floating-point errors. Quotes expire after 60 seconds and are renewed by a separate fleet lease per publication binding. Destination inventory below the priced output causes a route withdrawal; `max_output` is a ceiling, not a minimum wallet reserve. OIF requests check inventory against their own priced output, so a smaller request can be funded even when the full standing offer is unavailable.

`min_margin` is an operator-supplied USDC cost reserve for this development route. It does not fetch native-token exchange rates or prove profitability. EIP-1559 gas limits and fee caps bound each transaction, and simulation/native-balance checks run before signing. Base's additional L1 data fee is not converted into the USDC quote. There is no automatic rebalancer, dynamic market pricing, price feed, cumulative spending budget, or token inventory reservation across multiple accepted orders. Do not describe this policy as production pricing. Actual fill simulation and token balances stop spending beyond available inventory, but a quote is not a guarantee of available capital for unlimited simultaneous requests.

Quote publication uses the primary LI.FI API. Its API does not accept Redis fencing tokens, so lease fencing cannot revoke an HTTP request already sent by a former publisher. Requests have bounded deadlines and quotes have short expiry; a global pause stops new worker steps and causes withdrawal on the next quote cycle. A pause cannot retract an already signed transaction. Stop publication and wait for withdrawal/expiry before treating liquidity as unavailable to new users.

## Cluster controls

`./bin/goif control -config config/testnet.json` reads the current operational control document. Version zero is the default, with discovery/execution enabled by process mode and no node overrides.

`./bin/goif control -config config/testnet.json -control-file config/control-paused.json` applies the example version-one global pause. A stale version is rejected. Create each subsequent document with exactly the current version plus one.

The authenticated `GET /control` and `PUT /control` endpoints expose the same state. PUT takes `{ "expected_version": 0, "control": { "version": 1, "paused": true, "nodes": {} } }`. Each node override has `paused` and `workers`, keyed by its exact node ID. Precedence is global pause, then node pause/concurrency reduction, then the process's startup worker limit. An override cannot increase the configured maximum of 32 workers.

Workers read control before each step and continue while work is ready. Queue readiness and lease acquisition are checked atomically, so a stale scan cannot execute a deferred or terminal intent. The configured worker interval applies when the queue is empty or execution is paused; storage failures use bounded backoff. Deferred intents retain their own durable retry time and do not delay other ready intents. Updates do not cancel an already running step. The signer-recovery loop continues reconciling previously authorized transactions during a pause, so their reservations do not remain stranded. Any eligible pod can own a configured source. Observation pods discover and persist intents, but start neither execution workers nor signer recovery. Redis records, not the discoverer's memory, own the work.

Endpoints:

| Endpoint | Access | Meaning |
| --- | --- | --- |
| `GET /healthz` | Public | Solver engine is running |
| `GET /readyz` | Public | Engine is running and coordination storage passes its safety checks |
| `GET /metrics` | Public | Process counters, owner gauges, shared queue and pending-signer age |
| `GET /control`, `PUT /control` | Bearer | Versioned fleet and node controls |
| `GET /intents/{id}` | Bearer | One durable order record |

Readiness does not continuously attest to RPC, API, or proof-service health. Structured logs expose deferred orders and cycle failures. Order IDs correlate worker logs; transaction-preparation logs include the operation, transaction hash, chain, and nonce. Do not put order IDs into metric labels.

## Deployment

See [deployment and monitoring](deployment.md) for Kubernetes resources, secrets,
network access, and capacity planning. The [Redis recovery guide](redis-recovery.md)
defines the persistent primary profile and approval procedure.

## Settlement recovery

The state sequence is `discovered → validated → approved → filled → proven → settled`. While filled, the selected settlement backend persists its own resumable checkpoint. Policy rejections are terminal. Network/proof/transaction uncertainty leaves the record durable and schedules a bounded-backoff retry. No replacement transaction is signed automatically. A reverted transaction, changed mandate, conflicting immutable discovery, corrupt journal, or deep reorg requires diagnosis.

Redis order state, signer reservations, and signed transaction bytes must survive restarts together. A sender with an outstanding operation cannot prepare another operation until a canonical receipt reaches configured depth. A separate loop recovers reservations even when an order expires or workers are paused. Proof-request interruption before job persistence can create another provider job on retry; it cannot create another fill. The proof provider offers no verified idempotency token for that call.

Polymer distinguishes a terminal job result (`status: "error"`) from a pending job or a failed HTTP/RPC request. A terminal result saves the failed job ID, a bounded diagnostic reason, and the accepted-request count before scheduling a replacement. The default budget is three accepted requests per intent, with 30- and 60-second waits before the replacements. The Redis checkpoint and queue deadline survive worker changes. Pending jobs, transport errors, unknown statuses, and malformed proofs retain the current job. The documented terminal status is described at https://docs.polymerlabs.org/docs/build/get%20started/prove-api-V2/errorhandling/; the configured method aliases follow https://github.com/lifinance/lintent/blob/ec20871d7dde50342ca31d76eecee97d5dfb1f94/src/routes/polymer/%2Bserver.ts.

When the budget is exhausted, the intent stays `filled` and reports `polymer proof job limit reached`. Workers stop requesting and querying Polymer for that intent, but continue checking the origin oracle. To recover:

1. Inspect the durable record's settlement checkpoint: `job`, `requests`, and `last_failure`. The provider's diagnostic reason is truncated to 512 bytes and kept out of automatic error logs.
2. Check the fill transaction, block, global log index, and provider availability. Correct the external cause before allowing more requests.
3. Increase the selected backend's `settings.max_proof_jobs` above the persisted `requests` count and roll out that configuration across execution pods. The next scheduled attempt resumes from the same fill. These operational settings do not change the fleet's execution-policy binding. An independently relayed valid proof also unblocks the intent through the existing oracle check.

Do not clear intent records, transaction journals, or signer reservations to reset this budget. The counter covers accepted requests whose IDs reached durable storage; a crash after provider acceptance but before persistence can still create an extra remote job. The `proof-check` diagnostic still reports the result of one job; it does not consume or reset an intent's budget.

## Local memory backend

`config/development.json` selects `storage.kind: memory` and `development: true`. Run it with `scripts/dev.sh`. Each process owns independent volatile records, leases, checkpoints, and controls; exiting the process discards them. The development configuration has no networks or sources, so startup needs neither Docker nor provider credentials. Add explicitly configured sources and routes to observe real events. Development mode rejects `-execute` because signed transaction recovery needs durable storage.

Use authenticated HTTP to inspect or control that running process. The `status` and `control` CLI commands target persistent storage and reject memory mode instead of opening an unrelated empty instance.

## Settlement configuration

The configuration defines named `settlements`, for example `polymer-testnet` with `kind: "polymer"` and `settings` containing `api`, `key_env`, `request_method`, `query_method`, and optional `requests_per_second`. An execution route selects it with `settlement: "polymer-testnet"`. Root Polymer credentials and chain-specific LI.FI instances are not accepted.

Polymer also accepts `max_proof_jobs` (1–100, default 3) and `proof_retry_seconds` (1–300, default 30). Omitted or zero values select the defaults. Each failed job doubles the replacement delay, up to five minutes. Raising the job budget can recover existing intents; lowering it does not discard an active job or an already obtained proof.

Only backends selected by a route are constructed. Their credentials are resolved
for execution or an explicit proof-access diagnostic, never for ordinary public
preflight. An unused backend definition needs no injected secret. The development
sample has no routes or settlement backends and needs no Polymer service.

A new backend must implement its own oracle validation and resumable verification;
changing `kind` alone cannot make a deployed oracle support another proof system.
Before changing a route's backend, drain and reconcile its active intents. Retain
the namespace, journals, and matching binary/configuration until that finishes.

## Preflight report format

Preflight reports identify networks with strings such as `eip155:11155111` and
report their current `height`. Balance entries identify their `network`, `account`,
and `asset`. Native amounts use `native_base_units`; all amounts are decimal
strings.

Intent reports expose `intent_id`, `kind`, `route`, API status, and
`settlement.verified` / `settlement.reference`. Adapter-specific fields live under
`details`. For EVM, these include `destination_chain`, `fill_block`,
`global_log_index`, `fill_transaction`, and `escrow_status`.

## OIF HTTP API

Optional `apis` entries select independently authenticated inbound adapters. The installed OIF adapter serves all four pinned `/v1` endpoints for the user-open subset. See [OIF compatibility](oif-compatibility.md) for its exact wire contract, credentials, supported authorization, and examples.

## Configuration changes

Before an incompatible configuration or execution-policy change, drain existing
work with its original binary, reconcile signer reservations, and retain the
namespace and journals. Start the changed policy in a fresh namespace. Do not copy
ready queues or transaction hashes into it. No automatic configuration migration
or deletion of durable state runs. The sample uses `goif-intents-v9`.
