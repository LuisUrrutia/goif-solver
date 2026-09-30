# Operations and configuration

## Configuration ownership

[config/testnet.json](../config/testnet.json) configures Ethereum Sepolia and Base
Sepolia. Configuration version 9 keeps execution settings under `executions`,
service instances under `providers` and `settlements`, and route bindings under
`publications` and provider `routes`. The file contains public addresses, route
limits, RPC fallbacks, and environment-variable names. Replace the sample signer
address with your dedicated account; private keys and API credentials belong in
the process environment.

Each route selects a signer, a named settlement backend, and the exact input/output
settlers, oracle pair, chain IDs, and tokens. Each signer has an explicit chain
allowlist. The current execution strategy supports one input and one output with
configured decimals; the sample uses six-decimal USDC. It accepts limit or
exclusive-limit context, empty callbacks, and the verified escrow/Polymer contracts.

Execution rejects Dutch auctions, Compact, zero or malformed amounts, foreign
contracts or tokens, unsafe deadlines, nonzero current or scheduled governance
fees, and amounts outside route limits.

Before spending, the solver requires an origin deposit at the configured
confirmation depth that is still deposited at the latest block. The intent's
contract-computed identifier must match the advertised ID, and expiry must be
at least one hour after the fill deadline. Preflight compares settler and oracle
runtimes against pinned hashes. A different deployment needs a matching contract
profile; changing addresses alone is insufficient.

`confirmations` sets the operator's required block depth. It does not guarantee
consensus or economic finality, and Ethereum and Base have different settlement
assumptions. The development values are 12 origin blocks and 20 destination blocks.
Review them before any other deployment. If the solver detects a reorg of a
confirmed fill, it stops that intent for reconciliation.

The storage namespace is bound to a canonical execution policy covering contracts,
assets, pricing, accounts, allowlists, signing bounds, and finality/proof semantics.
Transport URLs, credential references, request rates, worker tuning, and unused
definitions do not affect it. A node with an incompatible policy cannot join.
Before changing that policy, drain intents and signer reservations, retain the old
journals, and use a fresh namespace. Never move a signer while its old namespace
can still submit transactions.

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

`./bin/goif preflight -config config/testnet.json` checks current catalog membership,
RPC chain IDs, bytecode hashes, token decimals, zero current/pending governance
fees, and account balances. It is read-only and needs no signing key.

`./bin/goif register -config config/testnet.json -authorize-registration` signs the
server-issued identity challenge for each missing account. It merges configured
settlers into the account's registered sets, then reads back identities and
contracts to verify the result. This creates no on-chain transaction. Run it as
an administrative task while no one else is editing supported contracts: the API
replaces whole sets and has no conditional-write version.

`./bin/goif run -config config/testnet.json -node worker-1` starts observation mode.
Add `-execute` to authorize unattended signing for every discovered intent admitted
by the configuration. Add `-publish-quotes` to authorize standing quote publication
and renewal. The local development scripts and Kubernetes example omit these flags.

For a single funded test, pass `-intent` with the protocol-scoped key
(`evm-escrow/0x…`), or configure typed `{ "kind": "evm-escrow", "native_id": "0x…" }`
entries in `intent_allowlist`. The allowlist applies at both discovery and execution
and forms part of the fleet policy digest. Each LI.FI publication checks
registration for its own bound route. Startup and on-chain execution do not
depend on the LI.FI service.

Use a reviewed configuration, dedicated namespace, injected credentials, and an
exact intent allowlist for a funded test. Before funding, run an authenticated
proof check for the intended account.

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

Reading back a LI.FI publication confirms that the server stored the offer. By
default, integrator quote requests select from whitelisted solvers. Before LI.FI
enables your solver for that selection, include its registered address in
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
record contains its stage and saved fill/proof coordinates. Settled records also
contain observed origin/destination USDC balances. When intents share an account,
these snapshots cannot attribute a balance change to one intent.

## Quote and capital policy

Each route advertises one fixed-size input ticket equal to `max_input`. The
selected `pricing.kind` determines its output:

- `fixed-reserve` subtracts `pricing.min_margin` and requires equal decimals.
- `fixed-rate` applies `pricing.rate` in human asset units and supports different
  decimals.

Both cap output at `max_output`. The exchange rate is truncated to 36 decimal
places, and integer token arithmetic avoids floating-point errors. Quotes expire
after 60 seconds. Each publication binding has a separate fleet lease for renewal.

The publisher withdraws a route when destination inventory falls below the priced
output. `max_output` caps a quote's output; it does not set a minimum wallet reserve.
OIF checks inventory against each request's priced output, so a smaller request
can be funded even when the full standing offer is unavailable.

`min_margin` is an operator-supplied USDC cost reserve for this development route.
It does not fetch native-token exchange rates or prove profitability. EIP-1559 gas
limits and fee caps bound each transaction. Simulation and native-balance checks
run before signing, but Base's additional L1 data fee is not converted into the
USDC quote.

This is a development pricing policy. It has no automatic rebalancer, dynamic
market pricing, price feed, cumulative spending budget, or token inventory
reservation across accepted intents. Fill simulation and token-balance checks
prevent spending beyond available inventory. A quote still cannot guarantee
capital for unlimited simultaneous requests.

Standing quotes are published through the primary LI.FI API. That API does not
accept Redis fencing tokens, so a former publisher's HTTP request can still take
effect after its lease expires. Requests have bounded deadlines and quotes expire
quickly. A global pause stops new worker steps and triggers withdrawal on the next
quote cycle, but cannot retract an already signed transaction. Stop publication
and wait for withdrawal or expiry before treating liquidity as unavailable to new
users.

## Cluster controls

`./bin/goif control -config config/testnet.json` reads the current operational
control document. The default is version zero, with no node overrides. Discovery
and execution follow the process mode.

`./bin/goif control -config config/testnet.json -control-file config/control-paused.json`
applies the example version-one global pause. Each subsequent document must use
exactly the current version plus one; stale versions are rejected.

The authenticated `GET /control` and `PUT /control` endpoints expose the same state.
PUT takes `{ "expected_version": 0, "control": { "version": 1, "paused": true, "nodes": {} } }`.
Each node override has `paused` and `workers`, keyed by its exact node ID.
Global pause takes precedence, followed by node pause or concurrency reduction,
then the process's startup worker limit. An override cannot increase the configured
maximum of 32 workers.

Workers read control before each step and continue while work is ready. Checking
queue readiness and acquiring a lease happen atomically, so a stale scan cannot
execute a deferred or terminal intent. The configured worker interval applies
when the queue is empty or execution is paused. Storage failures use bounded
backoff; deferred intents keep their own durable retry times without delaying
other ready work.

Control updates do not cancel a step already running. During a pause, the
signer-recovery loop continues reconciling previously authorized transactions
so their reservations do not remain stranded.

Any eligible pod can own a configured source. Observation pods discover and
persist intents but start neither execution workers nor signer recovery.
Redis retains the work so another eligible process can resume it.

Endpoints:

| Endpoint | Access | Meaning |
| --- | --- | --- |
| `GET /healthz` | Public | Solver engine is running |
| `GET /readyz` | Public | Engine is running and coordination storage passes its safety checks |
| `GET /metrics` | Public | Process counters, owner gauges, shared queue and pending-signer age |
| `GET /control`, `PUT /control` | Bearer | Versioned fleet and node controls |
| `GET /intents/{id}` | Bearer | One durable intent record |

Readiness does not continuously check RPC, API, or proof-service health. Use
structured logs to inspect deferred intents and cycle failures. Intent IDs
correlate worker logs; transaction-preparation logs include the operation,
transaction hash, chain, and nonce. Keep intent IDs out of metric labels.

## Deployment

See [deployment and monitoring](deployment.md) for Kubernetes resources, secrets,
network access, and capacity planning. The [Redis recovery guide](redis-recovery.md)
defines the persistent primary profile and approval procedure.

## Settlement recovery

An intent advances through `discovered → validated → approved → filled → proven → settled`.
While it is filled, the selected settlement backend saves a checkpoint from which
it can resume. Policy rejections are terminal. Uncertain network, proof, or
transaction outcomes keep the durable record and schedule a retry with bounded
backoff. The solver does not automatically sign replacement transactions.
A revert, changed mandate, conflicting immutable discovery, corrupt journal,
or deep reorg requires diagnosis.

Intent state, signer reservations, and signed transaction bytes must survive Redis
restarts together. A sender with an outstanding operation waits for a canonical
receipt at the configured depth before preparing another. A separate loop recovers
reservations even if the intent expires or workers are paused.

If a proof request is interrupted before its job is stored, retrying may create
another provider job. It cannot create another fill. No provider idempotency token
has been verified for that request.

Polymer reports a terminal job failure as `status: "error"`. The solver saves that
job's ID, a bounded diagnostic reason, and the accepted-request count before
scheduling a replacement. The default budget allows three accepted requests per
intent, with waits of 30 and 60 seconds before the replacements. The Redis
checkpoint and queue deadline survive worker changes.

Pending jobs, HTTP/RPC transport errors, unknown statuses, and malformed proofs
keep the current job. Polymer documents the terminal status at
https://docs.polymerlabs.org/docs/build/get%20started/prove-api-V2/errorhandling/;
the configured method aliases follow
https://github.com/lifinance/lintent/blob/ec20871d7dde50342ca31d76eecee97d5dfb1f94/src/routes/polymer/%2Bserver.ts.

After the budget is exhausted, the intent stays `filled` and reports
`polymer proof job limit reached`. Workers stop requesting and querying Polymer
for that intent but continue checking the origin oracle. To recover:

1. Inspect the durable record's settlement checkpoint: `job`, `requests`, and `last_failure`. The provider's diagnostic reason is truncated to 512 bytes and kept out of automatic error logs.
2. Check the fill transaction, block, global log index, and provider availability. Correct the external cause before allowing more requests.
3. Increase the selected backend's `settings.max_proof_jobs` above the persisted `requests` count and roll out that configuration across execution pods. The next scheduled attempt resumes from the same fill. These operational settings do not change the fleet's execution-policy binding. An independently relayed valid proof also unblocks the intent through the existing oracle check.

Keep intent records, transaction journals, and signer reservations intact when
changing the budget. The counter covers accepted requests whose IDs reached
durable storage. A crash between provider acceptance and persistence can still
create an extra remote job. The `proof-check` diagnostic reports one job's result;
it neither consumes nor resets an intent's budget.

## Local memory backend

`config/development.json` selects `storage.kind: memory` and `development: true`.
Run it with `scripts/dev.sh`. Records, leases, checkpoints, and controls belong to
that process and disappear when it exits. The sample has no networks or sources,
so startup needs neither Docker nor provider credentials. Configure sources and
routes to observe real events. Development mode rejects `-execute` because
signed transaction recovery needs durable storage.

Use authenticated HTTP to inspect or control the running process. The `status`
and `control` CLI commands require persistent storage. They reject memory mode
because a separate CLI process would open an unrelated empty instance.

## Settlement configuration

Define named `settlements`, such as `polymer-testnet` with `kind: "polymer"`.
Its `settings` contain `api`, `key_env`, `request_method`, `query_method`, and
optional `requests_per_second`. An execution route selects the backend with
`settlement: "polymer-testnet"`. The configuration rejects root Polymer credentials
and chain-specific LI.FI instances.

Polymer also accepts `max_proof_jobs` (1–100, default 3) and `proof_retry_seconds`
(1–300, default 30). Omitted or zero values select the defaults. Each failed job
doubles the replacement delay, up to five minutes. Raising the budget can recover
existing intents. Lowering it preserves active jobs and proofs already obtained.

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

Optional `apis` entries enable inbound adapters with separate authentication.
The installed OIF adapter serves all four pinned `/v1` endpoints for the user-open
subset. See [OIF compatibility](oif-compatibility.md) for the wire contract,
credentials, supported authorization, and examples.

## Configuration changes

Before an incompatible configuration or execution-policy change, drain existing
work with its original binary, reconcile signer reservations, and retain the
namespace and journals. Start the changed policy in a fresh namespace. Do not copy
ready queues or transaction hashes into it. No automatic configuration migration
or deletion of durable state runs. The sample uses `goif-intents-v9`.
