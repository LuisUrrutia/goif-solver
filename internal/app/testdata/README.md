# Escrow runtime fixtures

`escrow-runtimes.json` contains deployed bytecode captured during the authorized
Sepolia/Base Sepolia audit on 2026-09-29. `input` and `output` match the hashes in
`internal/protocol/escrow/abi/provenance.json`; `oracle` matches
`internal/settlement/polymer/evm/provenance.json`.

The quote failover test passes these bytes through the production runtime
checks. Its corrupted-runtime case verifies that failover cannot bypass them.
The fixture contains no wallet material or API credentials.
