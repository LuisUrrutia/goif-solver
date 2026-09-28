# Execution ownership

Every discovery process can insert the same canonical order into Redis. The insertion script creates one durable record and queue entry. A different process can acquire that order. No execution state lives exclusively in the discovering process.

Order leases have monotonically increasing fencing tokens. Every state transition checks the lease token and expected prior stage in one Lua script. Redis supplies the queue clock. Expired workers cannot update records or release replacement leases. Completed records remain in Redis to suppress rediscovery.

A lease cannot revoke signed EVM transactions. The transaction sender must acquire both the order lease and a chain/address signer lease, reconcile the chain nonce, then atomically persist signed bytes and reserve that signer before broadcast. The reservation has no TTL. Recovery reuses those exact bytes. The signer can prepare another operation only after the prior receipt reaches route-specific finality. This deliberately limits each chain/address to one pending transaction; different signers and chains can proceed independently.

The transaction journal is an authorization surface: signed transactions can be broadcast by anyone who reads them. Redis needs private networking, ACLs, TLS, persistent storage, and restricted backups. Do not delete coordination state while transactions may be pending. Ordinary asynchronous Redis failover can lose acknowledged writes and violate the journal invariant; live deployments require a durability/failover policy that prevents rollback, or must stop signing and reconcile after failover. Redis fencing alone does not solve storage rollback, malicious operators, reorgs beyond configured finality, or external use of the same signer.

Use dedicated solver accounts. Never send transactions from another wallet process using those accounts. On uncertain network outcomes, retain the reservation and reconcile. A reverted transaction remains in the journal; automatic replacement with new signed bytes is prohibited.

## Verification

`scripts/check.sh` creates an isolated real Redis container, checks formatting, runs behavioral tests, builds, vets, and runs the race detector. The tests cover concurrent duplicate discovery, independent discovery/execution clients, terminal deduplication, lease replacement, stale writes, signer exclusivity, and reservation recovery.

Coordination tests do not establish unattended live settlement or authorize signing. See `verification.md` for the funded-test gap.

## Threat model and trust boundaries

The order server is an untrusted discovery source. The solver parses exact integers/addresses, pins the route, recomputes the identifier through the configured escrow, and checks deposit status before spending. Metadata such as API status is not proof of escrow or settlement. Provider RPC responses, verified contract bytecode, and the selected proof system remain trust dependencies; the development service does not run independent consensus clients.

An expired order lease cannot write a new stage, and an expired signer lease cannot prepare another transaction. A signed transaction outlives both leases. The journal closes the crash window between nonce selection and broadcast: recover the same operation and bytes, inspect canonical receipts, and never silently replace or forget a reservation. Tests exercise this with real Redis Lua/TTLs and the actual EVM sender.

A process or Redis administrator with write access can change policy or the transaction journal. A local process with key access can sign outside the coordinator. Neither threat is solved by a lease. Limit key access to solver processes, use distinct accounts for independent deployments, secure Redis and its backups, and authenticate operational controls.

The current implementation serializes each signer/chain until configured confirmation depth. This favors recoverability over transaction throughput. It does not claim exactly-once effects under Redis rollback, signer reuse outside this system, a consensus reorg beyond that depth, or malicious RPC responses. Public-chain transaction hashes and contract state remain the final reconciliation evidence.

Proof bytes are not signing secrets, but remote proof-service responses are untrusted. They are size-bounded and passed only to the configured oracle. Settlement proceeds only after the oracle reports the expected payload proven. Governance fees are required to remain zero, and all transaction proposals are simulated with configured gas caps. A mutable USDC implementation or blacklist change can still make a later step fail; such failures retain durable recovery state.
