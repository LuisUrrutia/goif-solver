# Redis durability and recovery

Production uses one persistent Redis primary. Automatic Sentinel/Cluster promotion
is outside this deployment contract. Each pod receives the same approved Redis
`run_id` through the environment variable named by `storage.primary_run_id_env`.
The approval lives outside Redis and survives solver pod restarts.

The client checks every new physical connection against that identity. Startup
and the running service also check primary role, standalone mode, AOF write
health, `appendonly yes`, `appendfsync always`,
`no-appendfsync-on-rewrite no`, `aof-load-truncated no`, and
`maxmemory-policy noeviction`. The running service checks once per second;
readiness performs the same check. A detected identity or durability change
latches the client closed and stops the engine. It cannot resume because a later
check happens to succeed. Missing permission to inspect the configuration is
also a failure, not permission to execute without verification.

`deploy/redis.conf` is the required persistence profile. Use a durable volume
whose storage system honors fsync. Restrict the solver ACL to its namespace and
the commands used by the backend, including `INFO`, `CONFIG GET`, Lua execution,
and script loading. Queue wakeups also require `PUBLISH`, `SUBSCRIBE`, and
`UNSUBSCRIBE`, with channel access limited to the namespace's `{namespace}:wake`
channel. Add these permissions before rolling out workers that use notifications.
Do not grant `CONFIG SET`, `REPLICAOF`, `FLUSHALL`, `FLUSHDB`,
`RESTORE`, `SWAPDB`, or administrator credentials to solver pods. Use TLS and
private networking for remote access. Connect directly to the primary; do not
put a proxy that changes servers within an established connection in front of it.

The checks cannot detect arbitrary in-place history replacement by a Redis
administrator or storage that falsely acknowledges fsync. An in-place restore,
configuration change, or topology change requires stopping every writer first.
Signed transactions already released to an RPC can still execute after a
shutdown; cancellation cannot revoke signatures.

## Initial approval

1. Provision the primary, its persistent volume, ACL, and TLS endpoint. Inject
   `GOIF_REDIS_URL` through the deployment's secret mechanism.
2. Run `goif storage-check -config config/testnet.json`. It is read-only, checks
   the persistence profile, and prints only the public primary identity and
   whether it matches the currently injected approval. It never adopts an ID.
3. Review that this is the intended empty deployment or a reconciled existing
   deployment. Set `GOIF_REDIS_PRIMARY_RUN_ID` in the external configuration used
   by every pod, then start the solver. Do not automate this assignment on pod
   startup or place the approved value in Redis itself.

## Restart, restore, or primary replacement

1. Stop quote publishers, execution replicas, and administrative signing jobs.
   Keep their credentials disabled during the investigation. Retain the old
   namespace, journals, outcomes, configuration, and matching binary.
2. Restore the complete AOF and its manifest to the intended volume. Do not copy
   selected queue keys or counters and do not clear pending signer reservations.
   A normal Redis restart also changes `run_id` and requires this review.
3. Inspect each configured chain/account: mined and pending nonces, every saved
   signed attempt, canonical receipts at configured depth, fills, proofs, and
   escrow claims. Resolve uncertain broadcasts using the saved bytes and public
   chain evidence. Absence from a restored Redis snapshot does not establish
   that an operation never happened.
4. While execution remains stopped, use `storage-check` to inspect the new
   primary. A separate read-only inspection process may use its new identity to
   read restored records; keep the running deployment's approval unchanged.
5. Only after reconciliation, update the approved ID outside Redis and restart
   the fleet. Verify readiness, pending reservations, discovery catch-up, and
   quote renewal before admitting new funded work.

There is no automatic rollback repair or acknowledgement-bypass command. The
quality gate tests rejection of another primary by both an existing client and
a fresh pod, rejection of weakened persistence, and the latched stop behavior.
These are local tests against independent Redis processes, not evidence of a
deployed cluster's disks, restore procedure, or network policy.

Redis documents fsync-before-reply for `appendfsync always`:
https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/.
The process identity is documented at https://redis.io/docs/latest/commands/info/.
Replication acknowledgements alone do not provide strongly consistent failover:
https://redis.io/docs/latest/commands/wait/.
