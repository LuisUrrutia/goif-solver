#!/usr/bin/env python3
"""Reject VM, provider, and storage implementations in generic packages."""
import json
import re
from pathlib import Path
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
    "oif": {"evm", "protocol", "escrow", "lifi", "solver", "app", "config", "storage"},
    "config": {"app", "evm", "protocol", "lifi", "storage", "solver"},
    "coordination": {"storage", "app", "config", "evm"},
    "solver": {"transport", "storage", "app", "config", "escrow", "evm", "lifi"},
    "intent": {"app", "config", "coordination", "escrow", "evm", "lifi", "solver"},
    "quote": {"app", "config", "coordination", "escrow", "evm", "lifi", "solver"},
    "evm": {"app", "lifi", "protocol", "quote", "settlement"},
    "preflight": {"app", "config", "escrow", "evm", "lifi", "solver"},
    "escrow": {"app", "config", "lifi"},
    "settlement": {"app", "config", "escrow", "evm", "lifi", "preflight", "solver"},
    "settlement/polymer": {"app", "config", "escrow", "evm", "lifi", "preflight", "solver"},
}
vm_neutral = {"oif", "config", "coordination", "solver", "intent", "quote", "preflight", "settlement", "settlement/polymer"}
for root in vm_neutral:
    rules[root].add("protocol")
for root, forbidden in rules.items():
    pending = [PREFIX + root]
    seen = set()
    while pending:
        path = pending.pop()
        if path in seen:
            continue
        seen.add(path)
        internal = path.removeprefix(PREFIX)
        adapter = path != PREFIX + root and path.startswith((PREFIX + "settlement/", PREFIX + "preflight/", PREFIX + "oif/"))
        prohibited = any(internal == name or internal.startswith(name + "/") for name in forbidden)
        runtime = root in vm_neutral and path.startswith(("github.com/redis/", "github.com/ethereum/go-ethereum"))
        if adapter or prohibited or runtime:
            raise SystemExit(f"Architecture violation: {root} depends on {path}")
        pending.extend(packages.get(path, []))
print("Protocol dependency boundaries passed.")

for name in ("internal/app/service.go", "internal/app/quotes.go", "internal/app/runtime.go",
             "internal/app/preflight.go", "internal/app/publication.go", "internal/app/audit.go",
             "cmd/goif/main.go"):
    imports = re.findall(r'"([^"\n]+)"', Path(name).read_text())
    for path in imports:
        if path.startswith(tuple(PREFIX + p for p in ("evm", "lifi", "protocol/", "settlement/", "preflight/", "oif/"))):
            raise SystemExit(f"Architecture violation: {name} imports {path}")
print("Application entry points are adapter-neutral.")
