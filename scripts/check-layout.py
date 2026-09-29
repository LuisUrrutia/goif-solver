#!/usr/bin/env python3
"""Check production structs; preserve positional Solidity tuple layouts."""
import json
from pathlib import Path
import subprocess

TOOL = "golang.org/x/tools/go/analysis/passes/fieldalignment/cmd/fieldalignment@v0.50.0"
# ABI conversion is positional. Reordering these fields changes decoded values.
ABI_TUPLES = {("internal/protocol/escrow/order.go", "StandardOrder"),
              ("internal/protocol/escrow/order.go", "Output")}


def main():
    result = subprocess.run(["go", "run", TOOL, "-json", "./internal/..."],
                            capture_output=True, text=True, check=False)
    if result.returncode:
        raise SystemExit(result.stderr or result.stdout)
    findings = set()
    root = Path.cwd()
    for analyzers in json.loads(result.stdout).values():
        for diagnostic in analyzers.get("fieldalignment", []):
            filename, line, _ = diagnostic["posn"].rsplit(":", 2)
            path = str(Path(filename).relative_to(root))
            name = diagnostic["message"].split(" has ", 1)[0]
            if path.endswith("_test.go") or (path, name) in ABI_TUPLES:
                continue
            findings.add(f"{path}:{line}: {diagnostic['message']}")
    if findings:
        raise SystemExit("\n".join(sorted(findings)))
    print("Production struct layout passed; positional ABI tuples preserved.")


if __name__ == "__main__":
    main()
