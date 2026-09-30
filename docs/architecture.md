# Intent processing boundaries

## Composition

Configuration version 9 separates execution adapters, provider instances,
settlement backends, sources, and publication bindings. `internal/config` checks
the configuration structure and references. Each selected adapter then parses
its own settings strictly. `internal/app/service.go`, quote coordination,
preflight aggregation, and the CLI work through interfaces that do not depend on
concrete adapters. Application adapter files assemble the implementations.

`executions` is keyed by protocol kind. Its settings belong to that execution
adapter. The installed `evm-escrow` adapter defines chains, custody, routes, and the
escrow workflow. Another VM needs an execution factory with its own settings,
discovery, quote sources, audit, and execution policy. That adapter can use its
native addresses, chain identities, transaction model, and custody without
having to fit them into EVM types. SVM and TVM execution adapters are not implemented.

`internal/evm` handles EVM transport, ERC20 reads, signing, and transaction recovery.
It does not import the deployed escrow ABI. `internal/protocol/escrow` defines the
pinned contract profile, StandardOrder codec, identifier, event decoder, route
validation, and pricing binding. `internal/escrow` runs the escrow workflow through
a table of stage handlers, keeping those stages out of the generic coordinator.

Contract ABIs and runtime hashes are pinned in the
[escrow provenance](../internal/protocol/escrow/abi/provenance.json) and
[Polymer oracle provenance](../internal/settlement/polymer/evm/provenance.json).
The LI.FI adapter's wire schemas live in its
[OpenAPI fixture](../internal/lifi/testdata/openapi.json). Update these contracts
and their checks together when adding a deployment or changing an adapter.

A provider instance lists the protocol/route pairs it serves. LI.FI catalog
checks, registration, subscriptions, history, and publication use only those
routes. One configured provider stream serves all its networks, so adding a
network does not open another WebSocket. A renewable lease selects one owner
per source identity across the fleet. Providers are initialized only when a
source or publication selects them. Each provider defines its notification schema.

`quote.Source`, `quote.Publisher`, and `preflight.Checker` do not use EVM types.
Each publication binds a source and publisher to a route. The service and CLI
share the same leases, pause handling, and renewal schedule based on quote expiry.
Each binding runs independently and keeps its lease between refreshes. An
inventory or upstream failure in one binding cannot stop another. Registration
checks cover the route being published.

Pod shutdown releases ownership without withdrawing the fleet offer. Global pause
causes withdrawal; manual withdrawal reports busy while another publisher owns
the binding. Withdrawal preserves the offer identity and skips inventory and
registration reads.

The application creates only settlement backends selected by a route. The shared
settlement interface carries opaque evidence and checkpoints; each adapter
validates its own proof format and rules. The installed Polymer backend handles
EVM log proofs for this contract profile. A different backend needs both an
implementation and compatible deployed contracts. Changing the URL cannot
provide either.

## Discovery

LI.FI discovery uses its WebSocket adapter. There is no LI.FI webhook adapter;
the inbound OIF HTTP API has its own request and authentication contract.

Sources emit candidates with protocol-scoped identities. Acceptance returns
only after the executor normalizes a candidate and storage inserts it durably.
WebSocket and chain observations of the same intent deduplicate to one
`protocol/native-id` record. Different protocols can use the same native
identifier without collision.

The LI.FI adapter uses a WebSocket with ping/pong, a 1 MiB frame bound, a bounded
ingress queue, and reconnect backoff. It subscribes before taking a bounded REST
reconciliation snapshot. Live intake continues during REST requests and retries.

Historical decimal integers can be JSON numbers or strings; the provider adapter
normalizes them without floating-point conversion. Each record is decoded
separately, so an invalid record does not discard its page or stop pagination.
For incomplete snapshots, the adapter logs rejected counts, unavailable pages,
or window overflow. It retries with exponential backoff, positive jitter, and
`Retry-After` support. After successful reconciliation, REST requests stop until
the next connection.

Live notifications and REST recovery share one serial durable acceptance loop.
A storage failure or live queue overflow ends the connection. A missing
notification identifier is calculated locally from the pinned contract codec.
The contract-computed identifier and confirmed deposit are checked again before
spending. Route mismatches are rejected locally without an RPC request.

The escrow log source reads full `Open(bytes32,StandardOrder)` events through HTTP
RPC in ranges of at most 128 confirmed blocks. Its scan interval belongs to that
source; it does not implement `eth_subscribe`. Each accepted event checkpoints
its block height, hash, and log index. A completed range omits the log index.
Block headers are reused within a scan. After a deadline, the next scan resumes
after the last acknowledged event, including within a dense block.

Checkpoint keys include protocol, event, chain, settler, confirmation depth, start
block, and lookback. A display-name change preserves progress; an explicit
backfill policy change starts a separate cursor. Compare-and-swap protects
concurrent scanners. A rejected intent is acknowledged, while a transient intake
failure replays only the uncheckpointed tail. Reorgs at the configured confirmation
depth stop scanning for reconciliation. ID-only events cannot supply this
protocol's full intent.

LI.FI has no durable replay token. Its finite REST window and mutable pagination
cannot guarantee lossless recovery after long offline periods. Other replicas
take over a source after its owner exits or its lease expires. Reconnecting keeps
that ownership and honors provider retry deadlines with positive jitter.
On-chain replay complements off-chain discovery; it cannot replace it.

## RPC, custody, and policy

The application constructs clients only for networks used by configured routes.
Construction makes no network requests. On first use, a client verifies the
endpoint's chain and shares that check among concurrent callers. Wrong-chain
endpoints are quarantined. Clients prefer healthy endpoints and use bounded
retries, failover, and deadlines for each attempt. Deterministic contract reverts
do not trigger failover; broadcasts replay identical bytes.

Waiting for a local request quota uses the caller's time budget. The network
timeout starts after quota admission and includes reading the response body.
With multiple endpoints, each attempt reserves part of the remaining budget for
failover. A local wait timeout does not impose a provider cooldown.

An endpoint's `env` overrides its public `url` fallback. An unset endpoint without
a fallback is skipped. Endpoint request rates override chain rates, which override
the process default. LI.FI and Polymer each have separate rate overrides. Budgets
are per process, so operators divide upstream fleet limits across replicas.

Selecting custody builds the public signing policy without loading a key. Only
execution or explicit registration of a missing account opens custody. The
installed adapter is `local-key`; another custody factory can return `evm.Signer`.
Returned accounts and signed transactions are checked against the configured
account and exact unsigned payload before journaling. Optional challenge signing
is a separate capability.

Pricing is selected explicitly. `fixed-reserve` assumes equal decimals and input/
output value parity. `fixed-rate` converts between configured decimal precisions
using an operator-supplied asset exchange rate. Both enforce input/output caps,
use integer arithmetic, and share quote and admission calculations. Neither
strategy provides market data, dynamic gas conversion, or portfolio rebalancing.

Escrow admission parses each external intent once and checks its allowlist entry.
It matches typed route identities before looking up a signer or applying the
route's pricing and deadline policy. Checking another route reuses the parsed
integers and addresses.

## Durable coordination

Workers use `ClaimNext` to obtain one due intent with its fencing lease. Redis
checks bounded batches atomically and hides claimed entries until the later of
their retry time and lease expiry. A schedule hash preserves the retry time:
renewal moves visibility forward, release restores the schedule, and expiry
makes abandoned work claimable.

After an empty scan, workers subscribe before checking the next queue deadline.
Redis Pub/Sub and memory-store broadcasts wake idle workers on insertion or
rescheduling. Notifications prompt a rescan; the durable queue grants ownership.
Workers recover from missed notifications within `work_interval_seconds`, which
also bounds idle reconciliation. Deferred work and expired leases wake workers
at their own deadlines. Storage errors use worker backoff. An operational pause
prevents workers from claiming more work.

`coordination.Backend` defines the storage contract, implemented by Redis and
process-local memory. Consumers use narrower interfaces for reads, leases, and
journals. Memory serves one development process and rejects funded execution.
Redis scripts are embedded from linted Lua files, with Redis globals declared
for LuaLS and luacheck.

Intent keys are scoped by protocol. Attempts store an opaque codec, bytes, hash,
and adapter metadata. Completion atomically persists immutable finality or
verified expiry evidence and releases the reservation. An old completion cannot
release a new attempt. EVM uses canonical receipt evidence and does not replace
transactions on expiry. Redis durability must preserve acknowledged journal writes.

The namespace binds a canonical execution policy: contracts, assets, pricing,
accounts, allowed intents, finality, signing limits, and proof semantics. It ignores
transport URLs, secret references, rates, local worker tuning, and unused adapters.
Reordering equivalent definitions does not change the digest. Conflicting execution
policies cannot share a namespace.

For incompatible configuration changes, drain the existing deployment, reconcile
signer reservations, and retain its journals before using a fresh namespace.
See [configuration changes](operations.md#configuration-changes). Independent
namespaces must not share signer accounts.

## Extending the solver

New sources implement `intent.Source` and handle their connection, replay,
heartbeat, and backpressure. A protocol that requires polling can use this
interface without changing the coordinator. New execution adapters implement
`solver.Executor` and register a kind when the application is assembled.
Quote publishers define their service's publication schema; custody providers
must retain journal and nonce coordination.

A storage backend must preserve atomic fencing, persistent reservations, and
immutable journals. A settlement backend must validate its own contracts,
encoding, finality, and proof semantics. Adding a VM requires an execution
implementation; changing chain IDs cannot turn the EVM adapter into SVM or TVM.

Preserve positional Solidity tuple order when changing ABI types. Run the
[quality gate](quality.md) after changes.
