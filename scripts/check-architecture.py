#!/usr/bin/env python3
"""Reject VM, provider, and storage implementations in generic packages."""
import json
import subprocess

PREFIX = "github.com/LuisUrrutia/goif-solver/internal/"
raw = subprocess.check_output(["go", "list", "-json", "./internal/..."], text=True)
decoder = json.JSONDecoder()
packages = {}
while raw.strip():
    package, end = decoder.raw_decode(raw.lstrip())
    raw = raw.lstrip()[end:]
    packages[package["ImportPath"]] = package.get("Imports", [])

rules = {
    "coordination": {"storage", "app", "config", "evm"},
    "solver": {"storage", "app", "config", "escrow", "evm", "lifi"},
    "intent": {"app", "config", "coordination", "escrow", "evm", "lifi", "solver"},
    "quote": {"app", "config", "coordination", "escrow", "evm", "lifi", "solver"},
    "evm": {"app", "lifi"},
    "preflight": {"app", "config", "escrow", "evm", "lifi", "solver"},
    "escrow": {"app", "lifi"},
    "settlement": {"app", "config", "escrow", "evm", "lifi", "preflight", "solver"},
    "settlement/polymer": {"app", "config", "escrow", "evm", "lifi", "preflight", "solver"},
}
vm_neutral = {"coordination", "solver", "intent", "quote", "preflight", "settlement", "settlement/polymer"}
for root, forbidden in rules.items():
    pending = [PREFIX + root]
    seen = set()
    while pending:
        path = pending.pop()
        if path in seen:
            continue
        seen.add(path)
        internal = path.removeprefix(PREFIX)
        adapter = path != PREFIX + root and path.startswith((PREFIX + "settlement/", PREFIX + "preflight/"))
        prohibited = any(internal == name or internal.startswith(name + "/") for name in forbidden)
        runtime = root in vm_neutral and path.startswith(("github.com/redis/", "github.com/ethereum/go-ethereum"))
        if adapter or prohibited or runtime:
            raise SystemExit(f"Architecture violation: {root} depends on {path}")
        pending.extend(packages.get(path, []))
print("Protocol dependency boundaries passed.")
