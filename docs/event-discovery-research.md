# Event discovery and RPC research

Verified on 2026-09-28. This note records protocol evidence and implementation
recommendations; it does not claim the recommendations are implemented.

## LI.FI WebSocket

LI.FI recommends pushed WebSocket discovery and also supports on-chain discovery.
REST collection is an alternative with throttling constraints. Duplicate intent
notifications are expected; later notifications may add context. Deduplicate by
the intent identifier, while validating immutable content before merging context.
The documentation warns that not every off-chain intent is emitted on-chain.
Sources: https://docs.li.fi/lifi-intents/for-solvers/api-overview and
https://docs.li.fi/lifi-intents/for-solvers/orderflow.

The official linked example uses a plain WebSocket at `wss://order-dev.li.fi/`,
with no Socket.IO framing or subscription request:

```json
{"event":"user:vm-order-submit","data":{"orderType":"CatalystCompactOrder","order":{},"quote":{},"inputSettler":"0x...","sponsorSignature":"0x...","allocatorSignature":"0x..."}}
```

That historical example's order type is illustrative, not a restriction on the
current adapter. The envelope is `{event,data}`. An application message
`{"event":"ping"}` requires `{"event":"pong"}`. Standard WebSocket control
ping/pong handling is separately required. The example supplies no authentication
headers. Source:
https://github.com/lifinance/lintent/blob/a4aa78cd058cade732b73d83aa2843dd4e9ea24d/src/lib/utils/api.ts.

A read-only, unauthenticated HTTP Upgrade probe to the current dev root returned
`101 Switching Protocols` at 2026-09-28 20:43:28 UTC. The server then emitted a
WebSocket control ping and text `{"event":"ping"}`. It closed the curl probe,
which cannot reply to either ping. This verifies current endpoint/framing, not
the shape of a newly submitted intent. No credentials or transactions were used.

Recommendation: bound message size and ingress capacity, implement reconnect
backoff, handle both ping layers, and allow explicit bounded REST reconciliation
after reconnect. The published WebSocket contract provides no durable cursor or
replay guarantee; do not claim lossless off-chain recovery from WebSocket alone.

## Deployed on-chain discovery

The pinned `internal/evm/abi/input-settler.json` has two overloaded events:

```solidity
event Open(bytes32 indexed orderId, StandardOrder order);
event Open(bytes32 indexed orderId);
```

It has no `IntentRegistered`. The orderflow documentation links an older Compact
example for that event; subscribing only to it would miss this deployment.
The current verified `InputSettlerEscrow.open` and `openFor` implementations emit
the full `Open` event. `InputSettlerEscrowLIFI.openForAndFinalise` emits the ID-only
variant and is expressly a same-chain operation. For the supported cross-chain
escrow route, decode the full event directly; no API lookup is needed to hydrate
it. An ID-only event cannot establish the complete intent by itself.
Source, including inherited source files:
https://eth-sepolia.blockscout.com/api/v2/smart-contracts/0x00fc00edbe7c003b006f870068c548940000223e.

The full tuple is `user, nonce, originChainId, expires, fillDeadline, inputOracle,
inputs, outputs`. Output fields are `oracle, settler, chainId, token, amount,
recipient, callbackData, context`. Preserve ABI order in decoding types. Verify
the emitted ID against the contract's identifier, chain and configured settler;
discovery alone is not permission to spend. Retain the existing canonical escrow
and finality validation before execution.

Geth subscriptions only deliver current events, disappear with their connection,
and can close when consumers fall behind. Reorganizations resend old logs with
`removed:true`; the same transaction can produce repeated notifications. Source:
https://geth.ethereum.org/docs/interacting-with-geth/rpc/pubsub.

Recommendation: treat subscribed logs as wakeups, catch up using bounded
`eth_getLogs` ranges from a durable cursor, and advance that cursor only after
durable ingestion. Gate spend on configured finality and canonical block hashes.
Reconnect, overlap replay, and duplicate delivery must be safe. A log adapter may
poll when no subscription endpoint exists without turning the core into an API
polling scheduler.

## OIF boundaries and terminology

OIF's discovery model explicitly defines an `Intent` as raw discovered input,
before validation into an order. Its shared type carries source, standard,
metadata and protocol payload; discovery sends intents through a channel.
The OIF API specification still defines an `Order` union with escrow/resource-lock
variants. Use `Intent` for generic discovery and lifecycle naming, but preserve
`StandardOrder`, wire keys, and ABI names at protocol boundaries.
Sources:
https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-types/src/discovery.rs
and https://github.com/openintentsframework/oif-specs/blob/main/schemas/typescript/types.ts.

The inspected OIF solver actually disables on-chain WebSocket discovery until
removed-log finality buffering exists; it uses finalized log polling behind its
event-producing discovery interface. This is a useful safety constraint, not
evidence that OIF currently supplies a ready-made WebSocket implementation:
https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-discovery/src/implementations/onchain/_7683.rs.

## Multiple RPCs and lazy validation

OIF network configuration owns token support and a list of RPC endpoints, each
with optional HTTP and WebSocket URLs. Its provider helper uses the first URL
and a bounded retry layer; the presence of an endpoint list alone does not prove
actual multi-provider failover. Sources:
https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-types/src/networks.rs
and https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-types/src/provider.rs.

In go-ethereum, HTTP client construction builds a transport; the subsequent
`eth_chainId` call is the network validation. WebSocket dialing does establish a
connection. Dial context only bounds initial setup, so calls need their own
deadlines. Sources:
https://github.com/ethereum/go-ethereum/blob/v1.17.6/rpc/client.go and
https://github.com/ethereum/go-ethereum/blob/v1.17.6/rpc/http.go.

Recommendation: construct endpoint pools without network calls; validate and
cache endpoint chain identity on first use; quarantine mismatches. Fail over
transient transport/rate-limit/server failures with bounded attempts and total
deadline. Preserve deterministic contract errors. Transaction retry must resend
identical journaled bytes, never allocate another nonce because an endpoint timed
out. An explicit preflight can probe networks with bounded concurrency; normal
startup need not probe every configured endpoint.

## Struct alignment

Go's own `fieldalignment` analyzer reports both total size and the prefix the GC
must scan for pointers. Its documentation cautions that compact layout can cause
false sharing and that warnings rarely identify a significant performance
problem. Its automatic fixer currently removes field comments. Run it as an
audit, inspect proposed changes, preserve ABI-bound tuple ordering, and benchmark
hot data structures before claiming a speedup. Sources:
https://pkg.go.dev/golang.org/x/tools/go/analysis/passes/fieldalignment and
https://github.com/golang/tools/blob/master/go/analysis/passes/fieldalignment/fieldalignment.go.

The requested guide supports reducing padding but also discusses false sharing:
https://goperf.dev/01-common-patterns/fields-alignment/. Alignment is one part of
performance work; bounded queues, network deadlines, avoiding startup fan-out,
and measured allocation/throughput benchmarks are separate requirements.
