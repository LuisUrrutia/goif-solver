# OIF compatibility scope

Reviewed against oif-specs commit
`ba57c972e990b024f1ed4fd019649e27bf811795` and oif-solver commit
`e6cf9f999bf57b713411558a9f7069ff42dc2d78`.

Run `python3 scripts/inspect-oif-spec.py` to fetch the pinned files, verify their
SHA-256 hashes, and reproduce the endpoint/schema inventory. It does not test
runtime conformance.

## Implemented today

The configured `lifi-websocket` and `evm-logs` sources run concurrently when the
service runs. Each source has its own lifecycle and emits into the same durable
coordinator. The observation smoke test stops its process on completion; source
configuration does not mean a process remains running.

The on-chain source recognizes the full Open event in the pinned deployed escrow
ABI. It does not imply support for every OIF deployment or custody mechanism.
Contract support includes ABI, identifier, validation, fill and settlement behavior;
changing a source label or contract address is insufficient.

The service does not yet implement the public OIF API. Its internal Candidate,
Progress and standing Offer models are not OIF wire schemas. In particular,
`GET /intents/{id}` is an administrative record endpoint, not the OIF status API.

## Recommended adapter scope

Keep one small ingestion interface and concrete adapters:

| Adapter | Transport | Responsibilities |
| --- | --- | --- |
| LI.FI stream | Outbound WebSocket | LI.FI envelopes, heartbeat, reconnect and bounded recovery |
| OIF public API | Inbound HTTP | OIF request/response schemas, quote/submission correlation, signature and authorization checks |
| Configured escrow events | Chain RPC logs | Contract-specific event decoding, confirmation policy, replay and durable checkpoints |

Only ingestion and durable acceptance are shared. Provider-specific notification
formats stay inside their adapter. A hypothetical webhook or WebSocket format is
not added to the OIF standard. Source configuration selects an implemented adapter
and that adapter's settings; it does not expose an unrestricted transport/decoder
combination matrix.

The OIF solver's off-chain adapter accepts intents in-process from its public HTTP
handler after intake validation. It is not an external orderfeed to which another
solver can universally subscribe. Consuming an external solver's notifications
therefore requires that publisher's endpoint and notification contract.

Source:
https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-discovery/src/implementations/offchain/_7683.rs

LI.FI's published discovery interface is WebSocket, with event
`user:vm-order-submit`; webhook delivery would be a different interface:
https://docs.li.fi/lifi-intents/for-solvers/orderflow

## Public API conformance gap

The pinned OpenAPI document defines these operations and no notification callback
or WebSocket stream:

| Operation | Current implementation |
| --- | --- |
| `POST /v1/quotes` | Missing; standing LI.FI inventory is a different quote interface |
| `POST /v1/orders` | Missing; existing discovery does not validate OIF submission signatures |
| `GET /v1/orders/{id}` | Missing; requires OIF response fields and lifecycle translation |
| `GET /v1/assets` | Missing; route configuration is not an OIF discovery response |

Source:
https://github.com/openintentsframework/oif-specs/blob/ba57c972e990b024f1ed4fd019649e27bf811795/specs/openapi.yaml

Wire names such as `Order`, `orderId` and `/orders` must remain as specified even
when the internal domain calls discovered inputs intents. The OIF order union
includes `oif-escrow-v0`, `oif-resource-lock-v0`, `oif-3009-v0` and
`oif-user-open-v0`; accepting the JSON shape does not establish execution support
for every variant. Advertised capabilities must match implemented execution.
Signatures cannot be discarded during normalization. Solidity byte values need
schema-compatible JSON encoding rather than Go's default base64 for byte slices.
Internal escrow `settled` currently means the claim completed; the OIF lifecycle
has a separate `finalized` state, so a literal state-name passthrough is incorrect.

## Upstream inconsistencies to resolve before claiming compatibility

At the pinned commit, the README/TypeScript definitions and generated OpenAPI are
not fully synchronized:

- OpenAPI defines `GET /v1/assets`; README/TypeScript comments describe
  `/api/tokens` and a chain-specific token endpoint.
- OpenAPI `NetworkAssets` requires numeric `chain_id` and its asset addresses have
  an ERC-7930-shaped pattern. TypeScript `NetworkAssets` uses a CAIP chain string
  and native asset addresses.
- The reference solver's discovery documentation names `/api/v1/orders`, while
  oif-specs OpenAPI defines `/v1/orders`.

Sources:
https://github.com/openintentsframework/oif-specs/blob/ba57c972e990b024f1ed4fd019649e27bf811795/README.md
https://github.com/openintentsframework/oif-specs/blob/ba57c972e990b024f1ed4fd019649e27bf811795/schemas/typescript/types.ts

A compatibility implementation needs one explicit versioned wire contract and
request/response validation tests against that snapshot. The recommended baseline
is the pinned OpenAPI for HTTP interoperability, with documented upstream drift;
copying the reference solver's routes does not prove conformance to oif-specs.
This document records the gap and recommendation, not completed API support.
