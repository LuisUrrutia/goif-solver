#!/usr/bin/env python3
"""Reject protocol dependencies in generic packages, including transitive ones."""
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
    "solver": {"app", "config", "escrow", "evm", "lifi", "polymer", "preflight"},
    "intent": {"app", "config", "coordination", "escrow", "evm", "lifi", "polymer", "solver"},
    "quote": {"app", "config", "coordination", "escrow", "evm", "lifi", "polymer", "solver"},
    "evm": {"app", "lifi"},
    "preflight": {"app", "lifi"},
    "escrow": {"app", "lifi"},
}
for root, forbidden in rules.items():
    pending = [PREFIX + root]
    seen = set()
    while pending:
        path = pending.pop()
        if path in seen:
            continue
        seen.add(path)
        if path.removeprefix(PREFIX) in forbidden:
            raise SystemExit(f"Architecture violation: {root} depends on {path}")
        pending.extend(packages.get(path, []))
print("Protocol dependency boundaries passed.")
