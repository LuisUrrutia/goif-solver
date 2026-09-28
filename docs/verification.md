# Verification record

Date: 2026-09-28. Local environment: Go 1.27.1, Docker 29.4.0, macOS arm64. Redis tests use `redis:8.10.2-alpine`.

| Command | Outcome |
| --- | --- |
| `bash scripts/check.sh` | Passed formatting, real Redis behavioral tests, build, vet, race detector, Python secret-loader tests, and shell checks |
| `go test ./...` within the check script | Passed with `TEST_REDIS_ADDR` set by the script |
| `go build ./...` | Passed |
| `go vet ./...` | Passed |
| `go test -race ./...` within the check script | Passed with real Redis and HTTP/RPC test servers |
| `go run ./cmd/goif preflight -config config/sepolia.json` | Passed against public Ethereum Sepolia and Base Sepolia RPCs and the current LI.FI development catalog |
| `bash scripts/preflight.sh` | Passed repeatable historical pilot audit: escrow status 2, global log index 2, matching prefixed hash, `isProven=true` |
| `bash scripts/smoke.sh` | Passed foreground observation startup, health, metrics, unauthenticated-control rejection, and CLI control read; process stopped |
| `go run github.com/yannh/kubeconform/cmd/kubeconform@v0.8.0 -strict -summary deploy/kubernetes.yaml` | Passed: 2 valid resources, 0 errors |
| `docker build -t goif-solver:dev .` | Passed Linux image build with CGO disabled and non-root scratch runtime |

Behavioral coverage includes concurrent duplicate discovery, independent discovery/execution clients, terminal deduplication, lease expiry/replacement, rejection of stale writes, one signer nonce owner, durable reservations after lease loss, signed-byte replay after uncertain broadcast, and control-version conflicts/node precedence. The full route test recreates the engine between persistent steps and observes exactly one destination fill and one origin claim.

Protocol tests use sanitized public fixtures. They verify the deployed batch-fill selector `0x7e7fc653`, full `OutputFilled` mandate decoding, block-global log index, and deployed prefixed Polymer hash `0x55253189e1a56e006fcbc7c0a7033109f5e332f2ba92c4914a0d99fb2b575a4c`. The prior manual order is historical evidence, not an unattended-run result.

The read-only preflight observed the configured public account with native currency and USDC on both chains. It neither proves key access nor authorizes spending. At the time of this verification, no private key, LI.FI API key, or Polymer API key had been injected. The user requested a hidden-entry wizard; it saves owner-only credentials outside the repository for the next phase. No registration signature, quote publication, approval, fill, proof request using paid credentials, relay, or claim was sent to a live service by this implementation session.

## Remaining funded verification

The user authorized one new 1-USDC test during this session, covering registration if needed, quote publication/withdrawal, fill, proof relay, and claim. Execution still needs the approved secret-injection mechanism, the configured funded solver key, and the funded order ID/source. Restrict the run to that order with `-order`; broader signing remains unauthorized. Do not recover credentials from previous chats, local notes, or browser state. The live run must verify authenticated registration and supported-contract readback, quote publication and withdrawal, actual proof-method compatibility, one fill, proof relay, final claim, and final status/balances. Keep its transaction hashes and order ID as evidence.

`kubectl create --dry-run=client --validate=false -f deploy/kubernetes.yaml -o name` could not run because no Kubernetes API server is configured (`localhost:8080` refused the connection). Offline strict schema validation passed with kubeconform. The manifest is an example, not a tested cluster deployment. Production pricing, deep-reorg recovery, transaction fee replacement, automatic Redis failover safety, and additional network/custody/storage adapters remain outside the implemented development support matrix.
