# Deployment and monitoring

Use a shared Redis namespace for replicas with the same execution policy. Each
pod needs a distinct node ID; the Kubernetes example uses the pod name.
Independent fleets need separate namespaces and signer accounts.

## How replicas share work

Each configured discovery source has one owner, chosen through a renewable lease.
Another replica can take over after shutdown or lease expiry. If the source
supports replay, the new owner resumes from its saved checkpoint.

Any eligible replica can accept an authenticated OIF HTTP submission. It
acknowledges the request only after durable insertion. Duplicate HTTP submissions,
WebSocket messages, and chain events all resolve to one intent record.

Execution workers claim intents with fenced leases. Signed transactions and signer
reservations are journaled before broadcast so another worker can recover them.
Observation replicas run intake without execution or signer recovery.

Quote publication has its own lease per binding. Pod shutdown releases ownership
without withdrawing the shared offer; global pause requests withdrawal. See
[execution ownership](execution-design.md) for transaction recovery and
[operational controls](operations.md#cluster-controls) for pause behavior.

Delivery is at least once. Fencing protects durable transitions; it cannot
guarantee exactly-once external effects if storage history is lost. The supported
Redis profile uses one persistent primary. The solver stops if that primary is
replaced without approval.
Follow [Redis recovery](redis-recovery.md) for initial approval, maintenance,
restores, and primary changes.

## Deployment resources

Build the solver image with `docker build -t goif-solver:dev .`.

`deploy/kubernetes.yaml` keeps the example in observation mode. Its two replicas
use a zero-unavailable rolling update with one surge pod, a disruption budget,
and hostname/zone spread preferences. The ingress policy admits only same-namespace
pods labelled `goif-solver-access: "true"`. Apply that label to intended monitoring
or gateway pods, or review the selectors for a gateway in another namespace.
The policies require a CNI that enforces NetworkPolicy. The HTTP service uses an
internal ClusterIP. To expose a public OIF endpoint, configure a TLS gateway.

`deploy/redis.yaml` supplies an optional single-primary StatefulSet, a retained
10 GiB PVC, internal TLS service, disruption budget, and solver-only ingress.
`OnDelete` prevents a Redis template edit from automatically restarting the
approved primary. A Redis restart is an outage until identity review and
reconciliation finish. Its disruption budget deliberately blocks routine node
draining until an operator performs that maintenance procedure.

Provide these resources through the cluster's configuration and secret manager:

| Resource | Required content |
| --- | --- |
| `goif-solver-config` ConfigMap | Reviewed `testnet.json`, with `listen: "0.0.0.0:8080"` |
| `goif-solver-secrets` Secret | Redis URL and approved run ID, control token, and only credentials needed by enabled adapters |
| `goif-redis-config` ConfigMap | `redis.conf` from `deploy/redis.conf` |
| `goif-redis-auth` Secret | `users.acl`, with anonymous access disabled and a password-protected solver user restricted as described in the recovery procedure |
| `goif-redis-tls` Secret | `tls.crt`, `tls.key`, `ca.crt`; the server certificate must cover the hostname in the Redis URL |

Use a `rediss://` URL with the ACL account and approved primary hostname. Only
the public CA is projected into solver pods; the Redis TLS private key is not.
The image retains its normal public CA bundle for RPC and provider HTTPS calls.
Choose a volume/storage class that provides the required fsync behavior and
capacity. The template's memory and disk sizes are starting values; measure your
deployment's needs before relying on them.
Pin the solver image digest before deployment. The quality gate validates the
manifests but does not deploy them.

## Monitoring and capacity

| Metric | Interpretation |
| --- | --- |
| `goif_queue_outstanding` | Shared nonterminal records, including delayed retries |
| `goif_queue_due` | Available entries whose scheduled time has passed; active leases are excluded |
| `goif_queue_oldest_due_seconds` | Age of the oldest available scheduling time |
| `goif_pending_signers` | Shared outstanding transaction reservations |
| `goif_oldest_pending_seconds` | Age since the oldest reservation was prepared; retries do not reset it |
| `goif_source_owners`, `goif_quote_owners` | Local renewable owners; sum across pods to compare with configured bindings |
| `goif_source_reconnects_total` | Local source exits that require reconnect; ownership contention is excluded |
| `goif_intents_discovered_total` | Local successful new durable insertions, including OIF intake; duplicates are excluded |

Queue and signer gauges describe the same namespace on every replica: aggregate
them with `max`, not `sum`. Use counter rates for process-local errors and progress.
Alert when due age rises without progress, a signer remains pending beyond its
chain's expected finality window, a configured source or publication has no owner,
or reconnects remain elevated. Owner gauges report leases; they do not show
whether a provider is connected or a chain cursor is current. Readiness does not
check upstream RPC or proof-service health either.

Request budgets remain per process and per configured endpoint. Account for all
replicas, the surge pod, and administrative callers when dividing an upstream
allowance. The example has no HPA. Review those budgets and the workload before
enabling automatic scaling. Each account/network keeps one pending reservation
until verified finality, so adding replicas cannot make that account submit
transactions faster. Separate configured signers can process independent work.

Records, fence counters, transaction bytes and outcomes remain durable. Monitor
Redis memory, disk, AOF health and queue growth; keep headroom for transitions and
recovery. No TTL or eviction policy may discard authorization history. There is
no automatic archival/deletion job or aggregate capital reservation. Keep route
admission bounded and plan capacity. Fixed testnet quotes do not provide
production pricing or capital allocation.

Measure the complete Redis instance, including history: an empty work queue does
not imply low storage use. Collect `used_memory`, `used_memory_rss`, and
`maxmemory` from `INFO MEMORY`, and `aof_current_size`, `aof_base_size`,
`aof_last_write_status`, and `aof_last_bgrewrite_status` from `INFO PERSISTENCE`.
Monitor free bytes on the persistent volume separately.

Start with an alert at 70% of `maxmemory` and treat 85% as urgent. For disk, alert
before free space falls below the observed AOF rewrite peak plus normal growth
during operator response. These thresholds reserve headroom; they are not
measured production capacity limits.

Estimate how long memory headroom will last by measuring the increase in
`used_memory` per completed intent, including actual proof sizes, journals, and
fence keys. Divide the memory remaining below the 70% threshold by that increase
and the expected completion rate. Use a representative load window, and budget
disk separately from AOF measurements.

Raise capacity or pause intake before the remaining time falls below the operator
response window. Keep enough headroom to finish already accepted work. Do not
respond to a capacity incident by deleting history, using `FLUSHDB`, or changing
eviction or TTL rules. Archival needs a separate recovery design.
