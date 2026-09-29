# OIF compatibility scope

The HTTP wire contract is pinned to oif-specs commit
`ba57c972e990b024f1ed4fd019649e27bf811795`, OpenAPI version 0.1.0:
https://github.com/openintentsframework/oif-specs/blob/ba57c972e990b024f1ed4fd019649e27bf811795/specs/openapi.yaml

## Implemented subset

| Operation | Behavior |
| --- | --- |
| `POST /v1/quotes` | Exact-input, one input/output, `oif-user-open-v0`, configured escrow route and pricing; checks inventory and contract policy |
| `POST /v1/orders` | Validates user-open calldata, allowances, route, pricing, and deadlines before durable intake; optional quote token binds exact content and expiry |
| `GET /v1/orders/{id}` | Durable state, amounts, settlement, optional fill transaction, and persisted creation/update timestamps in Unix milliseconds |
| `GET /v1/assets` | Configured assets with symbols, decimals, numeric `chain_id`, and ERC-7930 addresses as required by the pinned OpenAPI |

`oif-escrow-v0`, `oif-resource-lock-v0`, `oif-3009-v0`, exact-output, partial fills,
callbacks, asset locks, and protocol-submitted/gasless authorization are rejected.
They are not advertised as executable. Nonempty submission signatures are rejected
for user-open rather than discarded. The installed execution adapter is EVM
escrow; the HTTP schemas and handler have no EVM, LI.FI, or Polymer dependency.

The client checks the quote, grants the stated ERC20 allowance, submits
`openIntentTx` itself, and posts the returned order before the quote expires.
The quote has a 60-second validity window, a ten-minute fill deadline, and expiry
one hour after that deadline. `gasRequired` is the configured conservative gas
ceiling; the wallet should simulate and estimate the actual transaction. Refund
handling is `refund-claim`, not an automatic solver refund service.

The order's calldata binds the user, recipient, amounts, networks, and deadlines.
The user-open contract collects funds from the transaction sender. Intake does not
assert that a deposit already exists: execution separately checks the identifier,
confirmed deposit, current deposit state, and route policy before spending.
The upstream escrow description of that funding flow is:
https://github.com/openintentsframework/oif-contracts/blob/main/src/input/escrow/InputSettlerEscrow.sol
The installed adapter additionally verifies the pinned deployed runtime profile.

Quote IDs are signed stateless tokens, so replicas sharing the quote key can
validate them without a process-local cache. Changing the order or expiry breaks
the signature. Repeated valid submissions converge on the same protocol-scoped
record, including across handlers. An expired quote is rejected; query the durable
order ID after an uncertain response instead of requesting a replacement intent.
A direct user-open submission may omit `quoteId`; it must pass the same calldata
and execution admission checks. `quoteId` is not added to the canonical intent
payload, so chain events and API submissions deduplicate identically.

Internal completed escrow `settled` maps to OIF `finalized`; proof-ready maps to
OIF `settled`. Status does not invent timestamps on GET. Storage records creation
once and updates time atomically with each transition, using Redis server time
for Redis. Duplicate discovery preserves both values. Failed/rejected execution
is not represented as refunded; no refund tracker is claimed.

## Enable the adapter

The sample configs leave the inbound API disabled. Add this root configuration
fragment to enable it for a selected route:

```json
{
  "apis": [{
    "kind": "oif",
    "settings": {
      "provider": "my-solver",
      "token_env": "GOIF_OIF_API_TOKEN",
      "quote_key_env": "GOIF_OIF_QUOTE_KEY",
      "requests_per_second": 10
    },
    "routes": [{"protocol": "evm-escrow", "name": "sepolia-base-usdc"}]
  }]
}
```

Each selected route needs `input_symbol` and `output_symbol`. Inject distinct
random API and quote-signing secrets of at least 32 characters. API clients send
`Authorization: Bearer <API token>`; they do not receive the signing secret or
administrative control token. Serve TLS at the deployment proxy. The API is
mounted on the configured HTTP listener and cannot access `/control` with its
own credentials. Rate limits are per process; request bodies and duration are
bounded. Global/node pause and observation mode reject quotes and submissions;
assets and existing order status remain readable. There is no inbound API process
left running by tests or smoke checks.

Configuration version 8 is required for durable timestamps. Drain older namespaces
with their original binaries and retain journals; no automatic migration runs.

## Event interfaces

LI.FI WebSocket and configured escrow log sources can run alongside inbound OIF
HTTP. They share durable acceptance, not notification schemas. The spec defines
no universal OIF webhook or WebSocket feed. A remote service needs its own adapter.
The reference solver's off-chain discovery adapter accepts intents in-process from
its HTTP handler:
https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-discovery/src/implementations/offchain/_7683.rs

LI.FI publishes `user:vm-order-submit` over WebSocket:
https://docs.li.fi/lifi-intents/for-solvers/orderflow

Wire names `Order`, `orderId`, and `/orders` remain as specified. Internal work
uses intents and protocol-scoped identities; URL-encode the full returned order
ID when inserting it into a client URL.

## Conformance evidence and upstream drift

`internal/oif/testdata/schemas.json` is a compact snapshot of the six relevant
request/response schemas. Its provenance records the upstream hash. Regenerate
from the verified YAML with `ruby scripts/snapshot-oif.rb artifacts/oif-openapi.yaml`;
the generator rejects another source revision. The normal Go tests validate
requests and actual handler responses against every validation keyword present
in that snapshot. Tests also cover byte-array encoding, unsupported authorization,
quote tampering, duplicate intake, restart, timestamps, pause, rate limits, body
bounds, and the ERC-7930 published Ethereum example:
https://eips.ethereum.org/EIPS/eip-7930

`python3 scripts/inspect-oif-spec.py` independently verifies pinned upstream file
hashes and inventories endpoints. At this revision, README/TypeScript mention
`/api/tokens`, CAIP network fields, and native asset addresses, while OpenAPI uses
`/v1/assets`, numeric `chain_id`, and interoperable addresses. The reference solver
also documents `/api/v1/orders`. This implementation follows the pinned OpenAPI
paths and fields; it does not claim every upstream document is synchronized.

Sources:
https://github.com/openintentsframework/oif-specs/blob/ba57c972e990b024f1ed4fd019649e27bf811795/README.md
https://github.com/openintentsframework/oif-specs/blob/ba57c972e990b024f1ed4fd019649e27bf811795/schemas/typescript/types.ts
