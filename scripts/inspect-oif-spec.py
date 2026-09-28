#!/usr/bin/env python3
"""Inspect the pinned OIF contract; this is not a solver conformance test."""
import hashlib
import json
import re
import urllib.request

REVISION = "ba57c972e990b024f1ed4fd019649e27bf811795"
FILES = {
    "README.md": "94b09c0ab0c09ec5fce1b47d435b25105b5cb1f88635f899c97d4e613941014d",
    "specs/openapi.yaml": "82d802a1a2c938d73804e5bc73cdeb18265dca190ee819f6bd59919b570d1c5c",
    "schemas/typescript/types.ts": "5bd112370fb65dd69cabfe4e1a50fd094d0619280999bcfdd3691ea2f340d14b",
}


def inspect() -> dict:
    sources = {}
    for path, expected in FILES.items():
        url = f"https://raw.githubusercontent.com/openintentsframework/oif-specs/{REVISION}/{path}"
        with urllib.request.urlopen(url, timeout=20) as response:
            raw = response.read(1_000_001)
        if hashlib.sha256(raw).hexdigest() != expected:
            raise ValueError(f"Pinned source hash differs: {path}")
        sources[path] = raw.decode()
    openapi = sources["specs/openapi.yaml"]
    paths = openapi[openapi.index("\npaths:"):]
    network = openapi.split("    NetworkAssets:\n", 1)[1].split("    GetAssetsResponse:\n", 1)[0]
    types = sources["schemas/typescript/types.ts"]
    network_type = types.split("export interface NetworkAssets {", 1)[1].split("\n}", 1)[0]
    return {
        "revision": REVISION,
        "openapi_paths": re.findall(r"^  (/[^:]+):$", paths, re.MULTILINE),
        "openapi_has_notification_callbacks": bool(re.search(r"^\s+(webhooks|callbacks):", openapi, re.MULTILINE)),
        "readme_describes_api_tokens": "GET /api/tokens" in sources["README.md"],
        "openapi_network_uses_chain_id": "chain_id:" in network,
        "typescript_network_uses_caip_chain": "chain: Chain;" in network_type,
        "proves_runtime_compatibility": False,
    }


if __name__ == "__main__":
    print(json.dumps(inspect(), indent=2))
