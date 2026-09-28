# Execution ownership

Every discovery process can insert the same canonical order into Redis. The insertion script creates one durable record and queue entry. A different process can acquire that order. No execution state lives exclusively in the discovering process.

Order leases have monotonically increasing fencing tokens. Every state transition checks the lease token and expected prior stage in one Lua script. Redis supplies the queue clock. Expired workers cannot update records or release replacement leases. Completed records remain in Redis to suppress rediscovery.

A lease cannot revoke signed EVM transactions. The transaction sender must acquire both the order lease and a chain/address signer lease, reconcile the chain nonce, then atomically persist signed bytes and reserve that signer before broadcast. The reservation has no TTL. Recovery reuses those exact bytes. The signer can prepare another operation only after the prior receipt reaches route-specific finality. This deliberately limits each chain/address to one pending transaction; different signers and chains can proceed independently.

The transaction journal is an authorization surface: signed transactions can be broadcast by anyone who reads them. Redis needs private networking, ACLs, TLS, persistent storage, and restricted backups. Do not delete coordination state while transactions may be pending. Ordinary asynchronous Redis failover can lose acknowledged writes and violate the journal invariant; live deployments require a durability/failover policy that prevents rollback, or must stop signing and reconcile after failover. Redis fencing alone does not solve storage rollback, malicious operators, reorgs beyond configured finality, or external use of the same signer.

Use dedicated solver accounts. Never send transactions from another wallet process using those accounts. On uncertain network outcomes, retain the reservation and reconcile. A reverted transaction remains in the journal; automatic replacement with new signed bytes is prohibited.

## Verification

`scripts/check.sh` creates an isolated real Redis container, checks formatting, runs behavioral tests, builds, vets, and runs the race detector. The tests cover concurrent duplicate discovery, independent discovery/execution clients, terminal deduplication, lease replacement, stale writes, signer exclusivity, and reservation recovery.

The first milestone provides coordination only. It does not prove contract settlement or authorize signing.
