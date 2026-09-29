# Coupling audit remediation

Scope: the 15 findings against `0f004387d53820942ec2f7e5bdacc016f0510f9b`,
plus the public OIF API gap recorded during that audit. Changes stay in the
`feat/lifi-solver` checkout. No funded-test credentials or historical Redis state
were loaded or migrated, and no new live transactions were submitted.

## Findings and evidence

| Finding | Resolution | Regression evidence |
| --- | --- | --- |
| F1 — Root configuration forces EVM/local custody | Adapter-owned execution settings and factories; custody compiles independently and opens only when required | `app.TestRuntimeSelectsNonEVMExecutionWithoutBuiltinInitialization`; `evm.TestCustodySelectionAndAccountBinding` |
| F2 — EVM infrastructure owns a particular escrow deployment | Deployed codec, ABI, identifier and validation moved to `internal/protocol/escrow`; EVM retains transport, ERC20, signing and recovery | Transitive architecture gate; historical ABI/fill/identifier fixture tests |
| F3 — Fixed reserve defines every route | Explicit fixed-reserve and fixed-rate policies share quoting/admission; rate supports different decimal precisions | `quote.TestRatePricingSharesPrecisionAndCapsWithAdmission` |
| F4 — LI.FI gates unrelated routes and custody | Explicit provider route bindings filter registration, catalog, stream, history and publication; publication checks its route independently | `app.TestLIFIBindingsExcludeOtherRoutesAndCustody`; on-chain-only initialization test |
| F5 — CLI duplicates concrete publication/signing logic | CLI uses application capabilities; service and CLI share publication leases, renewal, pause and withdrawal | Architecture gate on CLI/generic entry points; shared Quoter behavior tests |
| F6 — Historical diagnostics require LI.FI | Default inspection uses durable canonical evidence; external history requires explicit provider selection | `app.TestDurableOnChainHistoryDoesNotNeedAnOrderProvider` |
| F7 — Historical route selection matches too few fields | Immutable match includes networks, settlers, oracles and tokens | `evmpreflight.TestHistoricalRouteMatchesTokensBeforeSelectingBackend` |
| F8 — Native IDs collide across protocols | Durable key contains protocol and native identity; sources share that identity | `solver.TestNativeIdentityIsScopedByProtocolAndDeduplicatedAcrossSources` |
| F9 — Local/RPC changes force namespace migration | Canonical execution-policy digest excludes URLs, credentials, rates, capacity and display metadata; preserves exact JSON integers | Both `app.TestFleetPolicy…` tests, including object order and large integer precision |
| F10 — Shared journal prescribes EVM nonce/finality | Adapter-owned codec/metadata and immutable terminal evidence; atomic reservation release; explicit verified expiry capability | Memory and real-Redis backend contract suite, including old completion vs new attempt |
| F11 — Broad capabilities and resource strings leak | Consumer-owned interfaces for solver, journal, escrow and quotes; centralized intent/signer/quote resource constructors | Architecture gate, fencing/lease/journal tests |
| F12 — A route failure blocks others | Independent publication/recovery; local ID hydration; bounded parallel OIF quote collection | Blocked/failing route tests, RPC failover/cancellation tests, source hydration test |
| F13 — Worker cadence can outlive quote TTL | Each publication renews against remaining offer lifetime; independent control checks | `solver.TestRenewalUsesRemainingOfferValidity`; independent publisher loop tests |
| F14 — One request rate for every upstream | LI.FI, Polymer and each RPC endpoint have explicit overrides; endpoint falls back to chain then process rate | Endpoint settings validation, real local HTTP/RPC integration and rate-isolation tests |
| F15 — Core execution handles a concrete HTTP error | Neutral retry hint interface; transport owns HTTP status interpretation | Transitive solver/transport import prohibition and backoff tests |

## OIF and performance

The optional OIF HTTP adapter implements all four pinned endpoints for the
explicitly supported user-open subset. Schema fixtures, authenticated quote
correlation, byte arrays, direct submission validation, durable timestamps,
replay after handler restart, stage mapping, controls and request bounds are
covered by tests. See `oif-compatibility.md` for the wire baseline and limitations.

Struct layout is now part of the completion gate. All production findings must
pass except the two named Solidity ABI tuples whose field order is externally
specified. Positional anonymous literals were converted to keyed initializers
before reordering. Tests cover their observable HTTP/RPC behavior. The raw layout
report retains test-fixture diagnostics rather than presenting them as production
memory defects. No throughput improvement is inferred merely from field order.

The event decoder benchmark retains 124 allocations and approximately 7.2 KB per
operation. Timing varies with host load; benchmark output is available under
`artifacts/discovery-bench.txt`. No production traffic benchmark or profitability
claim is made.

## Verification and remaining capability limits

`bash scripts/check.sh` is the local and GitHub Actions entry point. It covers
formatting, staticcheck, vet, golangci-lint, gosec, govulncheck, build, behavior,
real Redis, race detection, architecture, layout, Lua, scripts, and both memory
runtime smoke modes. `bash scripts/profile.sh` separately records layout and
allocation evidence. No hosted CI run is claimed without publishing the branch.

The coupling audit introduced schema version 8. Cluster remediation now uses
version 9; see [Redis recovery](redis-recovery.md). Reconcile and drain older deployments with their
matching binary/configuration, retain their journals, and use a fresh namespace.
Transport or local tuning changes within the same execution policy do not need
that cutover. Independent namespaces must not share signer accounts.

Execution implementations remain EVM escrow with Polymer and local-key custody.
The seams are tested with another execution kind, VM-independent quotes, and an
injected custody provider; they do not constitute production SVM, TVM, remote-key,
or alternative-proof implementations. Market pricing, rebalancing, cumulative
capital reservation, and complete gasless OIF variants remain separate features.
There is no outstanding item from the 15-finding coupling audit.
