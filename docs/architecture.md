# Event-driven intent processing

`intent.Source.Run(ctx, emit)` is a long-lived producer. The consumer acknowledges
an intent only after the selected executor normalizes it and Redis durably
accepts its immutable payload. A duplicate identifier with different immutable
content is a conflict. Mutable API metadata never enters that payload.

The core packages `intent`, `solver`, `quote`, and `settlement` have no EVM, LI.FI, Polymer, or
application configuration dependency. `solver.Engine` dispatches by a typed
intent kind to an `Executor`; the executor owns validation and persisted strategy
state. A separate-protocol integration test uses a non-EVM identifier and completes
through the same coordinator. `scripts/check-architecture.py` checks transitive
imports so these boundaries remain enforced.

`app` assembles configured adapters. The first execution adapter is `escrow`,
which uses the generic EVM encoding/RPC/signer package and a route-bound
`settlement.Backend`. Polymer is an optional concrete backend.
Its typed stages select named methods from a static dispatch table. No workflow
closure map is rebuilt per step. Asynchronous transaction waits use a typed
`intent.Deferred`; settlement backends return a pending result and retry delay.
Real failures retain exponential retry backoff. Quotes are
neutral `quote.Offer` values; only `lifi.PublishOffer` translates them to LI.FI's
HTTP schema. LI.FI catalog checks belong to application composition. The EVM
preflight and execution packages do not import LI.FI.

OIF public API support is not yet implemented. See `oif-compatibility.md` for
the checked specification revision, concrete API gaps, and upstream schema drift.

## Sources

The sample enables both sources concurrently:

- `lifi-websocket`: plain WebSocket at the configured URL, application and control
  ping/pong, a 1 MiB frame limit, and a 32-envelope ingress queue. A slow consumer
  that exhausts the queue causes a reconnect rather than silent loss. Reconnect
  backoff is bounded at 30 seconds and respects cancellation. The subscription is
  established before the bounded REST reconciliation snapshot. REST is used only
  for connect/reconnect recovery, not as the regular discovery scheduler.
- `evm-logs`: reads full `Open(bytes32,StandardOrder)` events from the configured
  input settler, in ranges of at most 128 blocks below the configured confirmation
  depth. This adapter monitors logs using HTTP RPC, independently of the core.
  Its interval is a source setting. It does not implement `eth_subscribe`.

The on-chain adapter records a block number and hash after durable acceptance.
Checkpoint CAS prevents a stale scanner overwriting a concurrent scanner's
progress. A restart resumes the durable checkpoint; the first run uses
`start_block`, or a bounded `lookback` when `start_block` is zero. Rejected intents
are acknowledged and skipped. Transient ingestion failures leave the checkpoint
unchanged and replay the range. Confirmation-depth reorgs and inconsistent log
hashes stop progress for reconciliation. Normal shallower reorgs are excluded by
the confirmation window. A block-depth policy is not consensus finality.

ID-only `Open` events do not carry the supported cross-chain order. They are not
hydrated by guessing fields. Off-chain notifications missing their server ID are
resolved through `orderIdentifier` on a configured input settler; immutable policy,
identifier, deposit status and finality are still checked before spending.

The WebSocket protocol has no durable replay token. Its REST snapshot has a finite
window (offsets through 1000), and pagination is not a consistent snapshot. Long
offline periods or overload therefore cannot be called lossless off-chain
recovery. On-chain replay does not replace the off-chain source. Protocol evidence
and upstream OIF behavior are recorded in `event-discovery-research.md`.

## RPCs, policy and startup

Each chain owns an ordered `rpcs` list, with optional environment references and
public URL defaults. An unset optional endpoint is skipped. Constructing a client
per chain does not open connections. A provider's chain identity is verified on
first use, with concurrent verification coalesced and cancelable. Wrong-chain
providers are quarantined. Transient transport, overload and server failures fail
over with bounded attempts; a single provider gets one retry. Successful providers
are preferred on subsequent calls. Failed attempts rotate the next-call starting
point, so a deadline cannot permanently hide later healthy providers. Each endpoint has a rate limit and cooldown;
per-attempt and overall deadlines bound failure latency. Deterministic contract
reverts do not trigger failover. Broadcast retries reuse identical journaled bytes.

A configured chain may remain completely unused. A network explicitly selected
by a discovery source is used when that source starts. `goif preflight` is the
explicit full audit; normal construction performs no RPC preflight. Before an
execution step, route verification checks pinned runtimes and configured token
decimals, coalesces concurrent checks, and caches success for one minute. Failed
checks are never cached. Current/pending governance fees remain checked before a
new fill. Recovery only rebroadcasts previously journaled transactions.

The signer accepts only its configured chain allowlist; the sender additionally
requires that chain's `signing_enabled` policy. There are no hardcoded network IDs
or token addresses in the signer, sender, intent parser, or preflight. Token
addresses and decimals belong to routes. The current fixed-reserve strategy
requires equal token decimals and assumes configured input/output value parity;
it is not a general market-making strategy.

## Persistence and migration

Configuration version 3 uses `intent_sources`, `intent_allowlist`,
`work_interval_seconds`, chain `rpcs` arrays, `signing_enabled`, and route
`input_decimals`/`output_decimals`. The execution interval governs durable work,
not source discovery. CLI scope is `-intent`, signing is `-execute`, and the
protected record endpoint is `/intents/{id}`. Historical wire `Order` names and
Solidity `StandardOrder` tuple fields remain protocol names.

The strategy payload is wrapped with its intent kind; persisted progress separates
strategy state from retry metadata. This is not compatible with the previous
journal schema. Drain the old fleet and reconcile all signer reservations before
starting a new namespace. Version 3 introduced the `goif-intents-v3` namespace. Do not rewrite or
reset an active funded journal. The existing Redis `order:` resource/key prefix is
preserved as a storage encoding; `coordination.IntentResource` centralizes it.

Redis Lua lives in `internal/storage/redisstore/lua/`, embedded at build time. The check
script runs Lua 5.1-aware `luacheck`, `stylua --check`, and real-Redis concurrency
and fencing tests. Changing those scripts requires preserving atomic invariants,
not just syntactic validity.

## Performance evidence

Run `bash scripts/profile.sh` for the pinned `fieldalignment` audit and discovery
benchmark. Layout changes are selective: the version-3 arm64 audit reduced `Route` from 232 to 224
bytes (Go allocator class 240 to 224). Pointer-bearing fields in retained intent,
RPC, signer and progress structs were reordered to shorten GC scan prefixes.
`StandardOrder` and `Output` intentionally retain positional ABI layout; the full
Open-event round-trip test guards encoding/decoding. Cold diagnostic structs and
synchronization-containing structs may still appear in the audit. Do not blindly
apply its fixer or pack independent atomics onto a hot shared cache line.

On the Apple M3 Max development machine, the three recorded `DecodeOpen` runs were
5.91–5.93 microseconds/op, 7201 B/op and 124 allocations/op. These are local
baselines, not a before/after speedup claim or fleet throughput measurement. ABI
reflection and JSON dominate this path. Bounded workers/queues, lazy RPCs,
connection reuse, source isolation and bounded retries address the more immediate
resource and latency risks. No pool or unsafe conversion was added without a
measured benefit.

## Optional providers and storage

Configuration version 4 replaces `redis_url_env` with `storage: {"kind": "redis", "url_env": "GOIF_REDIS_URL"}` and moves LI.FI settings into `providers.lifi: {"api": "https://order-dev.li.fi", "key_env": "LIFI_API_KEY"}`. `quote_publisher: "lifi"` selects publication independently of event sources. Remove that publisher and all `lifi-websocket` sources to run only the on-chain adapter. An unused LI.FI provider is never instantiated, checked for registration, or queried for its catalog.

`internal/coordination` owns the backend contract and domain types. `internal/storage/redisstore` owns Redis keys and atomic Lua scripts. `internal/storage/memorystore` owns mutex-protected process-local state. The same contract suite checks both for deduplication, fencing, immutable transaction journals, signer reservations, delayed retries, control isolation, and checkpoint CAS. Redis additionally has a separate-client coordination test.

Memory storage requires `development: true`. It has no external dependency, persistence, or cross-process coordination. Development mode cannot execute funded intents because restarting would lose the signed transaction journal. A separate CLI process cannot inspect this store: use the running process's authenticated `/control` and `/intents/{id}` endpoints. `config/development.json` starts without event sources, networks, or providers.

`internal/quote.FixedReserve` prices generic assets and validator/solver identifiers. The EVM composition adapter maps its concrete route into that model. SVM/TVM identifiers work in pricing tests; their execution and signing adapters remain unimplemented.

Version 4 introduced a new namespace. Existing fleet policy digests deliberately reject this configuration change in place; drain and migrate old namespaces rather than resetting their state.

## Settlement backends

Configuration version 5 moves the root Polymer fields into named
`settlement.backends` entries. Each route must reference one entry through its
`settlement` field. An entry selects a typed `kind` and its corresponding settings;
the sample names a `polymer` backend `polymer-testnet`. Multiple routes may share
its proof API client while keeping their own oracle pair, signer, and chain binding.
Unused entries never instantiate clients or resolve credentials. Observation and
public preflight do not load proof-service credentials.

`settlement.Backend` owns compatibility verification, resumable advancement, and
read-only attestation inspection. Its evidence has a typed kind and an opaque
payload; its checkpoint is adapter-owned JSON. The interface has no EVM address,
proof job, proof bytes, or remote polling API. An adapter may wait for an externally
delivered attestation and return `Pending` until its verifier confirms it. The
worker's retry scheduling does not prescribe how that adapter receives evidence.
A test adapter uses a delivered event without a remote proof job.

`escrow` persists the selected backend ID with the intent, refuses a changed
binding, and durably stores each pending checkpoint before advancing again. It
confirms the adapter's verified result through `Inspect` before enabling the claim.
Fleet policy digests also bind the complete configuration, including backend
settings. Signed effects use the existing fenced, immutable transaction journal.

`settlement/polymer` owns the deployed oracle ABI and runtime fingerprint, event
payload hash, proof request/query protocol, and relay transaction. Its private
versioned checkpoint stores the job and proof across restarts. A pending job does
not create another request on the next worker. Relay operations include the backend
ID and reuse journaled signed bytes. LI.FI's catalog only checks whether the
configured oracle pair is active under one published oracle; it does not select
Polymer by name. Each backend checks its actual contract compatibility.

The architecture gate prohibits dependencies on any settlement adapter from core,
EVM, escrow, and preflight packages. Application composition selects implementations.
Polymer remains the only production implementation. Adding another backend still
requires its own configuration variant, factory case, evidence handling, verifier,
and recovery tests; no Hyperlane, SVM, or TVM execution support is implied.

Version 5 replaces proof-specific escrow stages with opaque settlement progress and
requires route bindings. The sample namespace is `goif-intents-v5`. Drain and
reconcile existing intents with their original configuration and binary before
switching; do not rewrite funded journals or reuse their namespace with this schema.
