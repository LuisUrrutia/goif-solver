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

The adapter rejects `oif-escrow-v0`, `oif-resource-lock-v0`, `oif-3009-v0`,
exact-output requests, partial fills, callbacks, asset locks, and
protocol-submitted/gasless authorization. It does not advertise them as executable.
User-open submissions must have empty signatures; a nonempty signature causes
rejection. Execution currently uses EVM escrow, while the HTTP schemas and handler
have no EVM, LI.FI, or Polymer dependency.

The client checks the quote, grants the stated ERC20 allowance, submits
`openIntentTx` itself, and posts the returned order before the quote expires.
The quote has a 60-second validity window, a ten-minute fill deadline, and expiry
one hour after that deadline. `gasRequired` is the configured conservative gas
ceiling; the wallet should simulate and estimate the actual transaction.
Refunds use `refund-claim` and require a claim. The solver does not refund
automatically.

The order's calldata binds the user, recipient, amounts, networks, and deadlines.
The user-open contract collects funds from the transaction sender. Accepting a
submission does not prove that the deposit exists. Before spending, execution
checks the identifier, confirmed deposit, current deposit state, and route policy.
The upstream escrow contract defines this funding flow:
https://github.com/openintentsframework/oif-contracts/blob/main/src/input/escrow/InputSettlerEscrow.sol
The installed adapter additionally verifies the pinned deployed runtime profile.

Quote IDs are signed stateless tokens, so replicas sharing the quote key can
validate them without a local cache. Changing the order or expiry breaks the
signature. Repeated valid submissions resolve to the same protocol-scoped record,
even across handlers. An expired quote is rejected. After an uncertain submission
response, query the durable order ID instead of requesting a replacement intent.

A direct user-open submission may omit `quoteId`, but must pass the same calldata
and execution admission checks. The canonical intent payload excludes `quoteId`,
so chain events and API submissions deduplicate identically.

The internal escrow state `settled` means execution is complete and maps to OIF
`finalized`. Proof-ready maps to OIF `settled`. GET responses use stored timestamps:
storage records creation once and updates the timestamp atomically with each
transition. Redis uses its server time. Duplicate discovery preserves both values.
Failed or rejected execution is not reported as refunded; the solver has no refund
tracker.

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

Each selected route needs `input_symbol` and `output_symbol`. Inject separate
random API and quote-signing secrets of at least 32 characters. Give clients only
the API token, which they send as `Authorization: Bearer <API token>`. Keep the
signing secret and administrative control token private.

Serve TLS at the deployment proxy. The API uses the configured HTTP listener;
its credentials do not grant access to `/control`. Rate limits apply per process,
and request bodies and duration are bounded. A global or node pause, or running
in observation mode, rejects quotes and submissions. Assets and existing order
status remain readable.

Persistent storage requires an externally approved Redis primary identity. Set
it through the [environment setup](../README.md#environment-variables).

## Event interfaces

LI.FI WebSocket and configured escrow log sources can run alongside inbound OIF
HTTP. All three use durable acceptance, with separate notification schemas. The
spec defines no universal OIF webhook or WebSocket feed. Each remote service needs
its own adapter.
The reference solver's off-chain discovery adapter accepts intents in-process from
its HTTP handler:
https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-discovery/src/implementations/offchain/_7683.rs

LI.FI publishes `user:vm-order-submit` over WebSocket:
https://docs.li.fi/lifi-intents/for-solvers/orderflow

The API keeps the specified wire names `Order`, `orderId`, and `/orders`. Internally,
the solver uses intents and protocol-scoped identities. URL-encode the full
returned order ID when inserting it into a client URL.

## Maintaining the schema snapshot

`internal/oif/testdata/schemas.json` contains a compact snapshot of the six relevant
request/response schemas, with the upstream hash recorded in its provenance.
When changing the pinned spec revision, update the snapshot and its provenance
together. Go tests validate requests and actual handler responses
against every validation keyword in the snapshot. Asset addresses follow ERC-7930:
https://eips.ethereum.org/EIPS/eip-7930

At the pinned revision, the upstream README and TypeScript schemas mention
`/api/tokens`, CAIP network fields, and native asset addresses, while OpenAPI uses
`/v1/assets`, numeric `chain_id`, and interoperable addresses. The reference solver
also documents `/api/v1/orders`. Because these upstream documents differ, this
implementation follows the pinned OpenAPI paths and fields.

Sources:
https://github.com/openintentsframework/oif-specs/blob/ba57c972e990b024f1ed4fd019649e27bf811795/README.md
https://github.com/openintentsframework/oif-specs/blob/ba57c972e990b024f1ed4fd019649e27bf811795/schemas/typescript/types.ts
