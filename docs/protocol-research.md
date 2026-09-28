# LI.FI protocol integration evidence

Observed on 2026-09-28. This document distinguishes the service's advertised coverage, the selected deployed contracts, and the route implemented by this solver. A supported chain entry does not establish a usable route or authorize signing.

## Sources and precedence

Use the development OpenAPI embedded in https://order-dev.li.fi/docs for wire schemas. `internal/lifi/testdata/openapi.json` contains the relevant paths and all their referenced schemas, with examples removed. LI.FI prose provides context but currently disagrees with both that schema and deployed contracts.

The three settlement ABIs in `internal/evm/abi/` come from verified deployed-source responses, not the upstream solver's ABI. Their runtime bytecode was compared byte-for-byte with `eth_getCode` on the selected chain; all three matched. `provenance.json` records addresses, source URLs, RPCs, runtime Keccak hashes, and ABI SHA-256 hashes. Blockscout reports `is_verified=true` but `is_fully_verified=false`; the runtime comparison is additional evidence, not a claim of a security audit.

- Escrow: https://eth-sepolia.blockscout.com/api/v2/smart-contracts/0x00fc00edbe7c003b006f870068c548940000223e
- Output settler: https://eth-sepolia.blockscout.com/api/v2/smart-contracts/0x75220b7600c300005038432a0000f308e0000068
- Polymer: https://eth-sepolia.blockscout.com/api/v2/smart-contracts/0xa70fe63dd97e8e0cb37241ed231fcbca87e99b72
- Standard ERC-20 ABI: https://github.com/lifinance/lintent/blob/ec20871d7dde50342ca31d76eecee97d5dfb1f94/src/lib/abi/erc20.ts

The effective Go guide for implementation is https://go.dev/doc/effective_go. Protocol details below do not require copying the Rust service structure into Go.

## Network and contract coverage

The current development chain list includes EVM, SVM, and TVM networks. Both API deployments advertise the following EVM mainnets: Ethereum `1`, Base `8453`, Optimism `10`, Arbitrum `42161`, Polygon `137`, BSC `56`, Katana `747474`, Pharos `1672`, Robinhood Chain `4663`, Arc `5042`, Tempo `4217`, Injective `1776`, Plasma `9745`, Unichain `130`, Avalanche `43114`, Monad `143`, Gnosis `100`, Stable `988`, Celo `42220`, Ink `57073`, World Chain `480`, HyperEVM `999`, and Mantle `5000`. The production list additionally includes Soneium `1868`. Both list Solana Mainnet `1151111081099710` and Tron `728126428`. Sources: https://order-dev.li.fi/chains/supported and https://order.li.fi/chains/supported

| Test network | Chain ID | Development API | Production API |
| --- | --- | --- | --- |
| Ethereum Sepolia | 11155111 | Listed | Listed |
| Base Sepolia | 84532 | Listed | Listed |
| Optimism Sepolia | 11155420 | Listed | Not listed |
| Arbitrum Sepolia | 421614 | Listed | Listed |
| Arc Testnet | 5042002 | Listed | Listed |
| Solana Testnet | 1151111081099711 | Listed | Not listed |
| Solana Devnet | 1151111081099712 | Listed | Not listed |

This is a dated inventory, not a promise of route availability. At observation time, https://order-dev.li.fi/routes returned one route (a Sepolia same-chain route), while https://order.li.fi/routes returned 12,852 token routes. The requested USDC development route therefore needs an active standing quote. The OpenAPI explicitly says acceptance of a quote for a chain does not itself enable that chain for user quoting.

For **Ethereum Sepolia USDC → Base Sepolia USDC**, the live development catalog supplies:

| Component | Chain | Address | Read-only evidence |
| --- | --- | --- | --- |
| InputSettlerEscrowLIFI | 11155111 | `0x00fc00edbe7c003b006f870068c548940000223e` | 20,299 runtime bytes |
| OutputSettlerSimple | 84532 | `0x75220b7600c300005038432a0000f308e0000068` | 5,781 runtime bytes; fill selector present |
| PolymerOracleMapped | 11155111 | `0xa70fe63dd97e8e0cb37241ed231fcbca87e99b72` | 5,964 runtime bytes; catalog active |
| Output oracle identity | Encoded in Base output | `0x000000000000000000000000a70fe63dd97e8e0cb37241ed231fcbca87e99b72` | Origin oracle identity, padded to bytes32 |
| Origin USDC | 11155111 | `0x1c7d4b196cb0c7b01d743fbc6116a902379c7238` | Public pilot order; 6 decimals |
| Destination USDC | 84532 | `0x036cbd53842c5426634e7929541ec2318f3dcf7e` | Public pilot order; 6 decimals |

Source: https://order-dev.li.fi/api/v1/contracts. Token addresses also appear in current OpenAPI route examples. Public RPCs used: https://ethereum-sepolia.publicnode.com and https://sepolia.base.org. `eth_chainId` returned `0xaa36a7` and `0x14a34` respectively. The architecture prose lists another testnet oracle (`0xC401…3a90`); do not silently substitute it. Source of that disagreement: https://docs.li.fi/lifi-intents/architecture/overview

The catalog exposes oracle lifecycle (`active`, `replaced`, `retired`), multiple oracle versions, and contract types. Preserve these distinctions. Same-chain output-as-oracle, Compact, Vow, SVM, and TVM need their own verified adapters and policy; they are not implemented merely because the catalog lists them. In particular, Solana proof entry points and oracle identities differ, and Tron uses chain-specific address and token behavior. Sources: https://order-dev.li.fi/api/v1/contracts and https://docs.li.fi/lifi-intents/architecture/oracle-systems

## Registration, quotes, and discovery

Solver authentication uses the `x-api-key` header. The current registration flow is:

1. `GET /api/v1/solver/register/message` returns `data.message` with a server-issued nonce.
2. Sign that message with the intended solver identity and `POST /api/v1/solver/register` with `{message,signature,account,chain}`. `chain` is CAIP-2, such as `eip155:11155111`. A nonce is valid for 24 hours and consumed on the first POST attempt even when registration fails.
3. `GET /solver-api/solver/identities` reads registration back.
4. `PUT /api/v1/solver/supported-contracts` replaces the full `inputSettler` and `outputSettler` lists. Each entry is `{chain,address}`. Omitted lists become empty; the deprecated `oracle` list is ignored. Read back with GET before quoting.

These API actions have no intrinsic on-chain registration transaction for the EOA path. Registration signatures still require intentional signer use. Sources: https://order-dev.li.fi/docs and https://docs.li.fi/lifi-intents/authentication

`POST /quotes/submit` takes `{quotes:[...]}`. Each quote has `fromChain`, `toChain`, `fromAsset`, `toAsset`, integer decimal counts, a Unix-second `expiry`, optional `exclusiveFor`, and `ranges`. Every range has decimal integer strings `minAmount` and `maxAmount` in **input token base units**, and a positive decimal exchange rate string `quote` in **output tokens per one input token**. For a plain rate:

```text
outputBase = floor(inputBase / 10^fromDecimals × quote × 10^toDecimals)
```

Use integer/rational arithmetic. Fixed costs are denominated in input base units. `oracleCosts:[{inputOracle,outputOracle,fixedCost}]` declares and prices supported oracle pairs; when present it takes precedence over range `fixedCost` for cross-chain quoting. Include destination gas, proof/relay and origin finalisation costs, governance fees, margin, and capital policy. The API may accept broader shapes; the solver should publish only what its execution policy supports.

**Withdrawal is a route submission with `ranges:[]` and a valid future expiry.** Sending an expired quote is not reliable: the current schema says expired submissions are skipped. All ranges for a route must be sent in one request; a later route submission replaces earlier ranges. `quotes:[]` is not a documented global withdrawal. Read back `GET /solver-api/quotes` with route filters to confirm removal. The current limit is 50,000 quotes, 100,000 total ranges, and a 32 MiB decompressed body. Respect `Retry-After` on HTTP 429/503. Source: `/quotes/submit` in https://order-dev.li.fi/docs; older conflicting advice: https://docs.li.fi/lifi-intents/for-solvers/quoting

`GET /orders` returns `{data:[envelope],meta:{total,limit,offset}}`; limit is 1–50 and offset 0–1000. Filters include origin/destination chain IDs, `exclusiveFor`, status, and on-chain ID. `GET /orders/status?onChainOrderId=…` returns one envelope. WebSocket `user:vm-order-submit` can deliver duplicates and later context; answer ping with pong. Deduplicate by on-chain order ID and settle disputes against chain state. Polling must respect rate limits and bounded pagination; a websocket/replay strategy is needed for order rates that can outrun the REST window. Sources: https://order-dev.li.fi/docs and https://docs.li.fi/lifi-intents/for-solvers/orderflow

The response order type is named `CompactOrderResponseDto` in OpenAPI even for escrow. Its wire fields `nonce`, `originChainId`, `expires`, `fillDeadline`, input values, and output `chainId`/`amount` are all **strings**. Inputs are arrays of two decimal uint256 strings: token ID and amount. EVM output oracle/settler/token/recipient are hex bytes32. `callbackData` and `context` are hex bytes. The envelope includes `inputSettler`, `order`, optional signatures, `quote`, and `meta`; GET responses do not carry a reliable `orderType` discriminator. Choose an implementation from validated contract addresses and explicit configuration, not schema naming or display metadata. Source: checked-in OpenAPI fixture from https://order-dev.li.fi/docs

## Escrow validation and exact execution

Decode and validate the complete order before allocating capital. Require configured chains, settlers, oracle pair, tokens, signer route, affordable amounts, sufficient deadlines, clean EVM addresses, and supported context. Reject input token IDs with nonzero upper 12 bytes, fee-on-transfer tokens, unrecognized callbacks, unsupported output counts, and unknown execution types. For USDC check token pause/blacklist behavior for signer and recipient. Inputs with zero amounts are not an excuse to accept unknown tokens. Sources: https://docs.li.fi/lifi-intents/for-solvers/orderflow and the verified escrow source above.

Call origin `orderIdentifier(StandardOrder)` and compare its returned bytes32 with the envelope ID. Then require `orderStatus(id)==1` (`Deposited`) at a suitably confirmed origin block. Values are `0=None`, `1=Deposited`, `2=Claimed`, `3=Refunded`. Never trust API `Open` or `Signed` alone as escrow evidence. The `StandardOrder` ABI tuple is:

```text
(address user,uint256 nonce,uint256 originChainId,uint32 expires,
 uint32 fillDeadline,address inputOracle,uint256[2][] inputs,MandateOutput[] outputs)
MandateOutput = (bytes32 oracle,bytes32 settler,uint256 chainId,bytes32 token,
                 uint256 amount,bytes32 recipient,bytes callbackData,bytes context)
```

The deployed destination call is:

```text
fillOrderOutputs(bytes32 orderId,MandateOutput[] outputs,uint48 fillDeadline,bytes fillerData)
selector = 0x7e7fc653
fillerData = the solver identity encoded as exactly 32 bytes
```

It is payable and returns no value. It differs from both the filling guide and upstream oif-solver's batch ABI. Approve only the configured output settler. The single-output `fill` path can return an existing fill record; batch semantics protect first-output solver ownership. Check `getFillRecord(orderId,output)` before sending and reconcile it after uncertain delivery. Source: verified OutputSettlerSimple source and ABI above; conflicting guide: https://docs.li.fi/lifi-intents/for-solvers/filling-orders

Context decoding is packed, not ABI encoding. Empty context and exactly `0x00` mean a limit order. Exclusive limit is **exactly 37 bytes**, with `0xe0`, then 32-byte exclusive solver, then a big-endian uint32 start time. Before that time only the named solver may fill; after it the order becomes permissionless. Dutch (`0x01`, 41 bytes) and exclusive Dutch (`0xe1`, 73 bytes) require auction pricing and should be rejected until implemented. Source: `src/output/simple/FulfilmentLib.sol` and `OutputSettlerSimple.sol` in the verified output contract source.

The deployed escrow is `InputSettlerEscrowLIFI`. Read `owner()`, `governanceFee()`, `nextGovernanceFee()`, and `nextGovernanceFeeTime()`. A claim normally receives gross input less `floor(amount × fee / 10^18)` when owner is nonzero. Fees can be scheduled with a seven-day delay and applied by an explicit call; maximum is 5%. Price the worst fee that may apply before claim or reject pending increases. Source: `GovernanceFee.sol` and `_resolveLock` in the verified escrow source.

## Fill evidence, Polymer, and claim

After the required destination confirmation depth, select the receipt log by all of:

- Exact configured output settler address.
- Topic 0 `0xfef24569acf839f2b5cb23fd59d8a9bcc21650ff711ac1961ca3c5d4681ffe12` (`OutputFilled`).
- Topic 1 equal to the validated order ID.
- Decoded solver, output fields, and final amount matching the expected execution.

Use the event's `uint32 timestamp`, not local time. Persist block hash, block number, transaction hash, and the log's **block-global `logIndex`**. Do not substitute its position in the receipt logs or transaction index. `getFillRecord(id,output)` is `keccak256(solver32 || timestamp4)` for the fill; zero means absent. Sources: verified output contract; https://docs.polymerlabs.org/docs/build/get%20started/prove-api-V2/api-endpoints/

LI.FI's first-party client uses testnet endpoint `https://api.testnet.polymer.zone/v1/`, JSON-RPC `polymer_requestProof` with `params:[{srcChainId,srcBlockNumber,globalLogIndex}]`, followed by `polymer_queryProof` with `params:[jobID]`. The bearer API key is separate from LI.FI's key. A complete proof is **base64**, decoded to bytes for `receiveMessage(bytes)` on the origin input oracle. Persist the job ID before polling; treat JSON-RPC error objects as errors even with HTTP 200. Pending/initialized are not completion; unknown/not-found states must be bounded and diagnosed. Source: https://github.com/lifinance/lintent/blob/ec20871d7dde50342ca31d76eecee97d5dfb1f94/src/routes/polymer/%2Bserver.ts

Polymer's current documentation instead names the methods `proof_request` and `proof_query`. These sources do not prove alias availability for a supplied account. Keep the method pair explicit and check authenticated compatibility before publishing inventory. Do not switch endpoint/API versions silently. Source: https://docs.polymerlabs.org/docs/build/get%20started/prove-api-V2/api-endpoints/

**The deployed fill proof hash includes a domain prefix absent from current upstream OIF source.** The verified deployment uses the following packed bytes, with integers in big-endian fixed widths:

```text
payloadHash = keccak256(
    0xd1252dff                 // first four bytes of keccak256("OIF.Fill")
    || solver32 || orderId32 || timestamp4
    || output.token32 || output.amount32 || output.recipient32
    || callbackLength2 || callbackData || contextLength2 || context
)
```

The prefix is required. `isProven(output.chainId,output.oracle,output.settler,payloadHash)` runs on the input oracle. The API's encoded output oracle for this EVM Polymer route is the **origin** oracle identity, even if a destination deployment address differs. The oracle verifies the actual emitting application and stores proof under its own local identifier. Source: verified `PolymerOracle.sol`, `MandateOutputEncodingLib.sol`, and `InputSettlerBase.sol`; OpenAPI oracle metadata descriptions.

Once proven, call escrow `finalise(order,solveParams,destination,call)` where solveParams has one `{uint32 timestamp,bytes32 solver}` per output, in original output order. The first output determines the canonical solver; the caller must control that identity. Use a policy-approved bytes32 claim destination and an empty callback for the development route. Inspect receipt success, `Finalised`, escrow status `2`, and token balance changes. Source: verified escrow source and https://docs.li.fi/lifi-intents/for-solvers/settlement

## Reproducible public pilot evidence

`internal/lifi/testdata/pilot-order.json` was fetched from the public endpoint below; signature fields and integrator key hashes were removed. It is historical input for tests, not permission to fill another order.

https://order-dev.li.fi/orders/status?onChainOrderId=0x98441c442077615b279a788283ecb399cb3bbb1e7e86103d375e5b64c9172bb3

Read-only checks against the public RPCs above established:

- `orderIdentifier(order)` returned `0x98441c442077615b279a788283ecb399cb3bbb1e7e86103d375e5b64c9172bb3`.
- `orderStatus(id)` returned `2`, matching API `Settled`.
- Destination fill transaction `0xb50456da95733d218811e94341c6bf32d65b2e92729dada3558ed8f5c2a8fcec` succeeded. Its matching fill log has block `47425376`, global log index `2`, timestamp `1790619040`, and final amount `990000`.
- Origin proof transaction `0xbdf7f119b2f168c1b3932cf9001a3afb4726b9a2b26e9379e8d0ea6442b65866` emitted `OutputProven` with payload hash `0x55253189e1a56e006fcbc7c0a7033109f5e332f2ba92c4914a0d99fb2b575a4c`.
- Computing the prefixed formula above reproduced that hash; origin `isProven(84532,paddedOracle,paddedSettler,hash)` returned true. The old unprefixed upstream formula returned false. This check exposed the deployment/version mismatch.
- Origin finalisation transaction is `0x47c2bc3d96a903d762fdbdf981889f1aafc0943f3c563428ae3f9310b3fed82d`. The public API still has `orderVerifiedTxHash:null`; API metadata therefore cannot be the sole proof reconciliation mechanism.

These checks use only `eth_chainId`, `eth_blockNumber`, `eth_getCode`, `eth_getTransactionReceipt`, and `eth_call`; no keys, proof-service credentials, or signing were used. They establish the historical route and encoding, not unattended execution of a new funded order.

## Required upstream solver reference

Reference commit: `e6cf9f999bf57b713411558a9f7069ff42dc2d78` in https://github.com/openintentsframework/oif-solver

- **Lifecycle and state:** Separate discovery, validation/strategy, delivery, settlement readiness, and claim. Upstream persists Created → Pending → Executing → Executed, optional PostFilled/PreClaimed, settlement-ready, and Finalized states. Its `Settled` means ready to claim, unlike LI.FI API `Settled`. Do not equate those external statuses. Sources: https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/README.md and https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-types/src/order.rs
- **Pricing:** Per-chain EIP-1559 fees, caps, and OP Stack L1 data fees are separate inputs. Native gas cannot be priced as token base units without a conversion policy. Source: https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/docs/fee-policy.md
- **Delivery:** Ambiguous submission outcomes can already have propagated. Preserve transaction attempts, reconcile receipts before replacement, and keep nonce ownership through recovery. Upstream's local nonce cache is process-local; it is not a distributed signer lock. Sources: https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-delivery/src/implementations/evm/nonce.rs and https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/docs/tx-bump-operations.md
- **Settlement:** Persist the selected settlement binding for an order and do not reroute an in-flight order when defaults change. Available upstream backends are direct, broadcaster, and Hyperlane; there is no Polymer backend. Source: https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-settlement/src/lib.rs
- **Operations:** Redis configuration is versioned, seeded once, and updated with optimistic locking. Active orders must survive the complete settlement/recovery window; active-state indexes avoid missing recovery work. Source: https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/docs/config-storage.md
- **Compatibility gaps:** Upstream discovery lists EIP-7683 on/off-chain implementations, not a LI.FI order-server adapter. Its batch fill ABI omits the deployed uint48 deadline. Its testnet seed covers OP/Base Sepolia with other contract addresses and omits Ethereum Sepolia. Sources: https://github.com/openintentsframework/oif-solver/tree/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-discovery/src/implementations , https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-types/src/standards/eip7683.rs and https://github.com/openintentsframework/oif-solver/blob/e6cf9f999bf57b713411558a9f7069ff42dc2d78/crates/solver-service/src/seeds/testnet.rs

Design implication: a Go implementation can reuse these lifecycle boundaries while supplying LI.FI-specific discovery, pinned deployed ABI and proof encoding, a Polymer backend, and Redis-coordinated execution. A worker lease alone cannot revoke a signed transaction from a former owner. Persist the signed transaction and its nonce before broadcast, replay the same bytes after uncertainty, and reconcile contract state before preparing another effect. Treat this as an implementation requirement to test, not an upstream guarantee.
