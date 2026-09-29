# Intent processing boundaries

## Composition

Configuration version 9 describes execution adapters, provider instances, settlement
backends, sources, and publication bindings separately. `internal/config` owns the
structure and references; each selected adapter parses its own strict settings.
`internal/app/service.go`, quote coordination, preflight aggregation, and the CLI
use neutral contracts. Concrete assembly lives in named application adapter files.

`executions` is keyed by protocol kind. Its settings belong to that execution
adapter. The installed `evm-escrow` adapter defines chains, custody, routes, and the
escrow workflow. Adding another VM means installing an execution factory with its
own settings, discovery, quote sources, audit, and semantic policy. It does not
require inventing EVM addresses, chain IDs, nonces, or signing keys for that VM.
A regression assembles an alternative execution kind without initializing EVM.
SVM and TVM execution adapters are not implemented.

`internal/evm` owns EVM transport, ERC20 reads, signing, and transaction recovery.
It does not import the deployed escrow ABI. `internal/protocol/escrow` owns the
pinned contract profile, StandardOrder codec, identifier, event decoder, route
validation, and pricing binding. `internal/escrow` owns its workflow; a stage
handler table dispatches that workflow outside the neutral coordinator.

A provider instance binds explicit protocol/route pairs. LI.FI catalog checks,
registration, subscriptions, history, and publication only see those bindings.
One configured provider stream serves all its networks; adding networks does not
add WebSockets. A renewable lease selects one owner per semantic source across
the fleet. Provider settings are not initialized when no source or publication
selects them. Another provider can own a different notification schema.

`quote.Source`, `quote.Publisher`, and `preflight.Checker` do not use EVM types.
Publication binds a source and publisher per route. Both the service and CLI use
the same leases, pause handling, and expiry-driven renewal loop. Each binding runs
independently and retains its lease between refreshes. Pod shutdown releases
ownership without withdrawing the fleet offer. Global pause causes withdrawal;
manual withdrawal reports busy while another publisher owns that binding. A failing inventory read or upstream request cannot stop another
binding. Registration checks happen for the route being published. Withdrawal
preserves offer identity and skips inventory and registration reads.

The application creates only route-selected settlement backends. The neutral
settlement interface owns opaque evidence and checkpoints; a concrete adapter
owns validation and proof semantics. The installed Polymer backend handles EVM
log proofs for this contract profile. Selecting a different backend requires its
implementation and compatible deployed contracts, not just a different URL.

## Discovery

Sources emit protocol-scoped candidates. Acceptance returns after executor
normalization and durable insertion. WebSocket and chain observations of the same
intent deduplicate to one `protocol/native-id` record; different protocols can use
the same native identifier without collision.

The LI.FI adapter uses a WebSocket with ping/pong, a 1 MiB frame bound, a bounded
ingress queue, and reconnect backoff. It subscribes before a bounded REST
reconciliation snapshot. Live intake continues during REST requests and retries.
Historical decimal integers can be JSON numbers or strings; the provider adapter
normalizes them without floating-point conversion. Each record is decoded
separately, so an invalid record does not discard its page or stop pagination.
Incomplete snapshots log rejected counts, unavailable pages, or window overflow
and retry with exponential backoff, positive jitter, and `Retry-After` support.
Successful reconciliation stops until the next connection. REST is reconnect
recovery, not the discovery clock. Both paths use one serial durable acceptance
loop; a storage failure or live queue overflow still ends the connection.
A missing notification identifier is calculated locally from the pinned contract
codec. The contract-computed identifier and confirmed deposit are checked again
before spending. Route mismatches are rejected locally without an RPC request.

The escrow log source reads full `Open(bytes32,StandardOrder)` events through HTTP
RPC in ranges of at most 128 confirmed blocks. Its interval belongs to that source;
it does not implement `eth_subscribe`. Each accepted event checkpoints its block
height, hash, and log index. A completed range omits the log index. Block headers
are reused within a scan; a deadline resumes after the last acknowledged event,
including within a dense block.
Their keys include protocol, event, chain, settler, confirmation depth, start block,
and lookback. A display-name change preserves progress; an explicit backfill
policy change starts a separate cursor. The `open-v2` cursor imports an existing
completed `open-v1` checkpoint once. Its separate key prevents older replicas
from treating a partial block as complete. No cursor is copied automatically from
the old display-name format.
CAS protects concurrent scanners. A rejected intent is acknowledged; a transient
ingestion failure replays only the uncheckpointed tail. Confirmation-depth reorgs stop progress for
reconciliation. ID-only events cannot supply this protocol's full intent.

LI.FI has no durable replay token. Its finite REST window and mutable pagination
cannot guarantee lossless recovery after long offline periods. Each semantic source has one renewable fleet owner. Other replicas take over
after owner exit or lease expiry. Source reconnects preserve ownership and honor
provider retry deadlines with positive jitter. On-chain replay complements, but
does not replace, off-chain discovery.

## RPC, custody, and policy

Only networks used by configured routes have clients constructed. Construction
makes no network requests. Each endpoint verifies its chain on first use;
concurrent checks coalesce. Wrong-chain endpoints are quarantined. Bounded retries,
failover, per-attempt deadlines, and preferred healthy endpoints contain failures.
Deterministic contract reverts do not fail over; broadcasts replay identical bytes.

An endpoint's `env` overrides its public `url` fallback. An unset endpoint without
a fallback is skipped. Endpoint request rates override chain rates, which override
the process default. LI.FI and Polymer each have separate rate overrides. Budgets
are per process, so operators divide upstream fleet limits across replicas.

Custody selection compiles a public policy without loading a key. Only execution
or explicit missing-account registration opens custody. The installed adapter is
`local-key`; another custody factory can return `evm.Signer`. Returned accounts and
signed transactions are checked against the configured account and exact unsigned
payload before journaling. Optional challenge signing is a separate capability.

Pricing is selected explicitly. `fixed-reserve` assumes equal decimals and input/
output value parity. `fixed-rate` converts between configured decimal precisions
using an operator-supplied asset exchange rate. Both enforce input/output caps,
use integer arithmetic, and share quote and admission calculations. Neither
strategy provides market data, dynamic gas conversion, or portfolio rebalancing.

## Durable coordination and cutover

`coordination.Backend` defines storage semantics; Redis and process-local memory
implement them. Consumers depend on narrower read/lease/journal contracts. Memory
is for one development process and rejects funded execution. Redis scripts are
embedded from linted Lua files with the Redis globals declared for LuaLS and
luacheck.

Intent keys are scoped by protocol. Attempts store an opaque codec, bytes, hash,
and adapter metadata. Completion atomically persists immutable finality or verified
expiry evidence and releases the reservation. An old completion cannot release a
new attempt. EVM uses canonical receipt evidence and does not replace transactions
on expiry. Redis durability must preserve acknowledged journal writes.

The namespace binds a canonical execution policy: contracts, assets, pricing,
accounts, allowed intents, finality, signing limits, and proof semantics. It ignores
transport URLs, secret references, rates, local worker tuning, and unused adapters.
Reordering equivalent definitions does not change the digest. Conflicting execution
policies cannot share a namespace.

Version 7 replaced root EVM/LI.FI fields with adapter settings and explicit route
bindings. Version 8 adds durable creation/update timestamps and optional public APIs. Version 6 introduced scoped intent keys and terminal attempt evidence.
Drain any older deployment with its original binary and configuration, reconcile
all signer reservations, retain its journals, and start version 9 in a fresh
namespace (`goif-intents-v9` in the sample). No migration, namespace deletion, or
live-state rewriting runs automatically. Independent namespaces must not share
signer accounts.

## Verification

`scripts/check-architecture.py` rejects transitive VM/provider/storage dependencies
from the neutral core, escrow dependencies from EVM infrastructure, and concrete
adapter imports in application entry points. The complete gate runs that guard,
real Redis semantics, race detection, Lua checks, and local runtime smoke tests.
`scripts/profile.sh` records layout diagnostics and event-decoding benchmarks;
ABI tuple fields retain their required positional order.
