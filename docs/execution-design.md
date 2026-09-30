# Execution ownership

Discovery sources can submit the same canonical intent to Redis. The insertion
script creates one durable record and queue entry, which any eligible process can
claim. Execution does not depend on the process that discovered the intent.

Each intent lease has a monotonically increasing fencing token. Every state
transition checks that token and the expected prior stage in one Lua script,
using Redis as the queue clock. An expired worker cannot update the record or
release a replacement lease. Completed records stay in Redis to suppress
rediscovery.

A signed EVM transaction can still execute after its lease expires. Before
broadcast, the sender must acquire both the intent lease and a chain/address
signer lease, reconcile the chain nonce, then atomically store the signed bytes
and reserve the signer. This reservation has no TTL. Recovery broadcasts the same
bytes, and the signer waits for the receipt to reach route-specific finality
before preparing another operation. Each chain/address therefore has at most
one pending transaction. Different signers and chains can proceed independently.

Anyone who can read the transaction journal can broadcast its signed transactions.
Protect Redis with private networking, ACLs, TLS, persistent storage, and restricted
backups. Keep coordination state while transactions may be pending. Asynchronous
Redis failover can lose acknowledged writes, including journal entries. A live
deployment must prevent that rollback or stop signing and reconcile after
failover. Fencing cannot recover lost history or protect against malicious
operators, reorgs beyond configured finality, or external use of the same signer.

Use dedicated solver accounts, and never send transactions from another wallet
process using them. Keep the reservation and reconcile when a network outcome is
uncertain. Reverted transactions also stay in the journal; the solver must not
automatically replace them with new signed bytes.

## Token allowance ownership

Before approving or filling, an intent reserves its destination allowance by
chain, signer, token, and spender. That reservation belongs to the intent and
survives a worker lease change. Other intents defer while the owner resumes on
any replica. Redis and memory release the reservation atomically when its intent
advances to `filled` or reaches a terminal state.

The signer journal reconciles every pending transaction before preparing another.
This includes an approval whose intent was rejected after signing. Allowance
reservations are part of the execution policy, which all replicas in a namespace
must enforce. Drain and reconcile before switching to an incompatible execution
profile. Redis ACLs must allow `HDEL` alongside the hash, sorted-set, and script
commands described in [Redis recovery](redis-recovery.md).

## Threat model and trust boundaries

The order server is an untrusted discovery source. Before spending, the solver
parses exact integers and addresses, pins the route, recomputes the identifier
through the configured escrow, and checks the deposit. API status alone proves
neither escrow nor settlement. The solver still trusts provider RPC responses,
verified contract bytecode, and the selected proof system; the development
service does not run independent consensus clients.

An expired intent lease cannot write a new stage. An expired signer lease cannot
prepare another transaction. Signed transactions outlive both, so the journal
covers the crash window between nonce selection and broadcast. Recovery must use
the recorded operation and bytes, inspect canonical receipts, and retain the
reservation until it has evidence to release it. Tests exercise this with real
Redis Lua/TTLs and the actual EVM sender.

Leases do not restrict an administrator who can write to Redis or a local process
that can use the signing key. Either can bypass the coordinator: the administrator
can change policy or journal entries, and the process can sign other transactions.
Limit key access to solver processes, use separate accounts for independent
deployments, secure Redis and its backups, and authenticate operational controls.

Waiting for confirmation depth serializes each signer/chain and trades transaction
throughput for recoverability. Exactly-once effects cannot be guaranteed after
Redis rollback, external signer reuse, a consensus reorg beyond that depth, or
malicious RPC responses. Public-chain transaction hashes and contract state are
the final evidence for reconciliation.

Remote proof responses are untrusted, although proof bytes are not signing
secrets. The solver bounds their size, sends them only to the configured oracle,
and settles only after that oracle reports the expected payload proven.
Governance fees must remain zero, and all transaction proposals are simulated
with configured gas caps. A mutable USDC implementation or blacklist change can
still make a later step fail. These failures retain durable recovery state.

The shared journal leaves nonce and transaction-validity rules to the delivery
adapter. That adapter supplies a versioned codec and metadata and verifies
terminal finality or expiry evidence. Both stores persist the evidence and release
the signer reservation in one fenced operation. Each later attempt uses a new
immutable operation key, keeping earlier bytes and outcomes for reconciliation.
