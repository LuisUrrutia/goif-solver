# Operations and configuration

## Configuration ownership

`config/sepolia.json` is public configuration. It contains addresses, route limits, RPC fallbacks, and environment-variable names. It contains no private keys or API credentials. The pilot signer address is public historical evidence; replace it with the intended dedicated account before using another key.

Each route selects a signer and exact input/output settlers, oracle pair, chain IDs, and tokens. Each signer has an explicit chain allowlist. The current execution strategy supports one six-decimal USDC input and one output, limit or exclusive-limit context, empty callbacks, and the verified escrow/Polymer contracts. It rejects Dutch auctions, Compact, zero or malformed amounts, foreign contracts/tokens, unsafe deadlines, nonzero current or scheduled governance fees, and amounts outside route limits.

The origin deposit must be present at the configured confirmation depth and still deposited at the latest block. The order's contract-computed identifier must match the advertised ID. There must be at least one hour between the fill deadline and expiry. A preflight compares settler/oracle runtimes against pinned hashes; changing addresses alone does not enable a different deployment.

`confirmations` is a route operator's block-depth policy, not a claim of consensus or economic finality. Ethereum and Base have different settlement/finality assumptions. The development values are 12 origin blocks and 20 destination blocks. Review them before any other deployment. A detected post-confirmation fill reorg stops the order for reconciliation.

The whole public startup policy, except the listen address, is hashed and bound to the Redis namespace. Nodes with different policies cannot join that namespace. In-flight orders retain the startup version. Drain orders and signer reservations before changing policy, increment the version, and use a new namespace. Never migrate a signer to a new namespace while its old namespace can still submit transactions. This conservative policy avoids silently moving an existing order to new contracts or a different signer.

## Secret injection

| Variable | Purpose | Needed for observation |
| --- | --- | --- |
| `GOIF_REDIS_URL` | Redis URL; use ACL/TLS for remote Redis | Yes |
| `SEPOLIA_RPC_URL` | Optional private origin RPC URL | No; public fallback |
| `BASE_SEPOLIA_RPC_URL` | Optional private destination RPC URL | No; public fallback |
| `GOIF_CONTROL_TOKEN` | Bearer token for read/control HTTP endpoints | Only for authenticated HTTP; required on non-loopback bind |
| `LIFI_API_KEY` | LI.FI solver API authentication | No for public reads |
| `POLYMER_API_KEY` | Polymer proof-service bearer token | No |
| `SOLVER_PRIVATE_KEY` | Local testnet signer | No |

Inject secrets through the process environment or a secret manager. Additional signer definitions can refer to different environment variables. Do not put secret values in command arguments, JSON config, shell history, checked-in files, screenshots, or issue comments. The service never prints effective secret configuration. Upstream response bodies and credential-bearing RPC URLs are omitted from errors.

The HTTP control token should contain at least 32 random characters. Keep the HTTP service on a private network; the application does not terminate TLS. Health and metrics are unauthenticated, while operational controls and order reads require `Authorization: Bearer ...`.

## Commands and signing authorization

`goif preflight -config config/sepolia.json` is read-only and needs no signing key. It checks current catalog membership, RPC chain IDs, bytecode hashes, token decimals, zero current/pending governance fees, and account balances.

`goif register -config config/sepolia.json -authorize-registration` signs the server-issued identity challenge for each missing account, merges the configured settlers into the account's registered sets, and reads identities/contracts back. It does not create an on-chain transaction. Run registration administratively, with no concurrent supported-contract editor; that API replaces whole sets and has no conditional-write version.

`goif run -config config/sepolia.json -node worker-1` observes. Adding `-execute-testnet` authorizes unattended signing for all discovered orders admitted by that configuration. Adding `-publish-quotes` also authorizes standing quote publication and renewal. For a single funded test, pass `-order` with the exact funded order ID, or set `order_allowlist` in configuration. The allowlist applies both at discovery and execution and participates in the fleet policy digest. The process checks API identity and contract registration before execution starts. These flags are deliberately absent from the local development scripts and Kubernetes example.

For a funded test, use a separately reviewed configuration, dedicated namespace, injected credentials, and explicit authorization for the funded order flow. The authorized development run completed; see [verification.md](verification.md) for its exact commands and evidence. Run the authenticated proof check for each intended account before funding a new test.

`goif proof-check -config config/sepolia.json -order 0x98441c442077615b279a788283ecb399cb3bbb1e7e86103d375e5b64c9172bb3` requests and polls a Polymer proof for the already settled pilot. This is an authenticated proof-service request, not an on-chain transaction; it checks account/method compatibility before the ten-minute funded-order window begins.

`goif publish -config config/sepolia.json -publish-quotes` is a single-process preparation command. It checks inventory and maintains the 60-second standing quote every 15 seconds while waiting for the user to create the short-lived order. It has no execution workers and never loads a signing key. Stop it once the order ID is known; shutdown withdraws the quote. Do not run this preparation command alongside another quote publisher. Start the execution process with `-execute-testnet -order` and that exact ID.

The task's credential wizard writes `~/.config/goif-solver/testnet.env` with mode `0600`. `python3 scripts/testnet.py` runs solver commands with those values, parses the file as literal assignments rather than shell code, rejects symlinks/shared permissions, and never prints secret values. Its `run` command requires an exact `-order` argument. Build operations occur before injecting secrets into the solver process.

`goif withdraw -config config/sepolia.json` submits every configured route with a future expiry and `ranges: []`, then checks the route's quote list. It only needs the LI.FI key. It does not cancel existing on-chain orders. Pause the fleet before a manual withdrawal; otherwise a running publisher can renew the route on its next cycle.

`goif status -config config/sepolia.json -order 0x98441c442077615b279a788283ecb399cb3bbb1e7e86103d375e5b64c9172bb3` reads that order's local durable record. A historical public order is absent unless this deployment discovered it. The record contains its stage and persisted fill/proof coordinates; settled records also contain observed origin/destination USDC balances. These snapshots are not attributed balance deltas when other orders share the account.

## Quote and capital policy

Each route advertises one fixed-size input ticket equal to `max_input`. The output is the smaller of `max_output` and `max_input - min_margin`. The exchange rate is truncated to 18 decimal places; integer token arithmetic avoids floating-point errors. Quotes expire after 60 seconds and are renewed by a fleet-wide quote lease. Low destination inventory causes a route withdrawal.

`min_margin` is an operator-supplied USDC cost reserve for this development route. It does not fetch native-token exchange rates or prove profitability. EIP-1559 gas limits and fee caps bound each transaction, and simulation/native-balance checks run before signing. Base's additional L1 data fee is not converted into the USDC quote. There is no automatic rebalancer, dynamic market pricing, price feed, cumulative spending budget, or token inventory reservation across multiple accepted orders. Do not describe this policy as production pricing. Actual fill simulation and token balances stop spending beyond available inventory, but a quote is not a guarantee of available capital for unlimited simultaneous requests.

Quote publication uses the primary LI.FI API. Its API does not accept Redis fencing tokens, so lease fencing cannot revoke an HTTP request already sent by a former publisher. Requests have bounded deadlines and quotes have short expiry; a global pause stops new worker steps and causes withdrawal on the next quote cycle. A pause cannot retract an already signed transaction. Stop publication and wait for withdrawal/expiry before treating liquidity as unavailable to new users.

## Cluster controls

`goif control -config config/sepolia.json` reads the current operational control document. Version zero is the default, with discovery/execution enabled by process mode and no node overrides.

`goif control -config config/sepolia.json -control-file config/control-paused.json` applies the example version-one global pause. A stale version is rejected. Create each subsequent document with exactly the current version plus one.

The authenticated `GET /control` and `PUT /control` endpoints expose the same state. PUT takes `{ "expected_version": 0, "control": { "version": 1, "paused": true, "nodes": {} } }`. Each node override has `paused` and `workers`, keyed by its exact node ID. Precedence is global pause, then node pause/concurrency reduction, then the process's startup worker limit. An override cannot increase the configured maximum of 32 workers.

Workers read control before each scheduling cycle. Updates do not cancel an already running step. The signer-recovery loop continues reconciling previously authorized transactions during a pause, so their reservations do not remain stranded. Discovery can occur on every node; Redis records, not the discoverer's memory, own the work. Test `TestDuplicateDiscoveryAndIndependentExecutor` demonstrates that a separate client can execute a discovered order.

Endpoints:

| Endpoint | Access | Meaning |
| --- | --- | --- |
| `GET /healthz` | Public | HTTP process is alive |
| `GET /readyz` | Public | Redis is reachable after startup preflight |
| `GET /metrics` | Public | Discovery, successful-step, and cycle-error counters |
| `GET /control`, `PUT /control` | Bearer | Versioned fleet and node controls |
| `GET /orders/{id}` | Bearer | One durable order record |

Readiness does not continuously attest to RPC, API, or proof-service health. Structured logs expose deferred orders and cycle failures. Order IDs correlate worker logs; transaction-preparation logs include the operation, transaction hash, chain, and nonce. Do not put order IDs into metric labels.

## Kubernetes

Build the image with `docker build -t goif-solver:dev .`. `deploy/kubernetes.yaml` is an observation-mode example with two replicas, a non-root user, a read-only filesystem, bounded resources, health probes, and no Kubernetes API token. It has not been deployed to a cluster by this task.

Provide a `goif-solver-config` ConfigMap whose `sepolia.json` key contains the reviewed configuration. Set `listen` to `0.0.0.0:8080` for pod probes. Provide `goif-solver-secrets` through your cluster's secret mechanism; include `GOIF_REDIS_URL` and a sufficiently long `GOIF_CONTROL_TOKEN`. The manifest intentionally contains no secret values. Publish the image through your own authorized registry workflow and pin its digest before deployment.

Use a private, persistent Redis deployment with ACLs, TLS, backups, and a durability/failover policy that does not roll back acknowledged transaction-journal writes. AOF every-second persistence or ordinary asynchronous replica promotion alone is insufficient for the one-time execution invariant. Following any possible Redis rollback, stop signing and reconcile accounts, receipts, and records before restarting. The namespace uses one Redis hash slot; the current client supports a single endpoint, not automatic Redis Cluster/Sentinel topology discovery.

Pods use their Kubernetes names as node IDs. API/RPC request budgets are per process; divide the provider's fleet allowance across replicas. The quote lease and signer reservations are cluster-wide. Give different independent fleets different namespaces **and different signer accounts**. Never let independent namespaces share a signer.

## Recovery and extension

The state sequence is `discovered → validated → approved → filled → proof-requested → proof-ready → proven → settled`. Policy rejections are terminal. Network/proof/transaction uncertainty leaves the record durable and schedules a bounded-backoff retry. No replacement transaction is signed automatically. A reverted transaction, changed mandate, conflicting immutable discovery, corrupt journal, or deep reorg requires diagnosis.

Redis order state, signer reservations, and signed transaction bytes must survive restarts together. A sender with an outstanding operation cannot prepare another operation until a canonical receipt reaches configured depth. A separate loop recovers reservations even when an order expires or workers are paused. Proof-request interruption before job persistence can create another provider job on retry; it cannot create another fill. The proof provider offers no verified idempotency token for that call.

New custody providers implement the `evm.Signer` contract while retaining the same journal and nonce coordinator. New order adapters should normalize canonical orders before `Enqueue`, and preserve explicit route selection. The LI.FI REST adapter is implemented. Optional `order_sources` entries add additional compatible servers, each with its own `url` and optional `key_env`; the primary `order_api` remains the quote/registration authority. Every source is polled and failures are isolated within the cycle. Canonical encoding makes duplicate discovery converge. A different wire protocol implements the consumer-owned `solver.OrderSource` interface. New settlement strategies must validate their own contracts, encoding, finality, token behavior, and proof semantics; SVM and TVM cannot reuse the EVM path by changing chain IDs. Redis operations implement `coordination.Backend`. A future backend must preserve its atomic fencing, persistent reservation, and journal invariants; matching method signatures alone does not establish safety. SQLite, memory, and file implementations are future work, not placeholders.
