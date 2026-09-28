#!/usr/bin/env python3
"""Run solver commands with owner-only testnet secrets, without a shell."""

import os
from pathlib import Path
import stat
import subprocess
import sys

REQUIRED = frozenset({"LIFI_API_KEY", "POLYMER_API_KEY", "SOLVER_PRIVATE_KEY"})
COMMANDS = frozenset({"preflight", "proof-check", "register", "run", "withdraw", "publish", "status", "control"})


def read_secrets(path: Path) -> dict[str, str]:
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor, encoding="utf-8") as stream:
        metadata = os.fstat(stream.fileno())
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.getuid():
            raise ValueError("Secret file must be a regular file owned by this user")
        if metadata.st_mode & 0o077:
            raise ValueError("Secret file requires owner-only permissions (chmod 600)")
        contents = stream.read(16385)
    if len(contents) > 16384:
        raise ValueError("Secret file is too large")
    values: dict[str, str] = {}
    for line in contents.splitlines():
        if not line:
            continue
        key, separator, value = line.partition("=")
        if not separator or key not in REQUIRED or key in values:
            raise ValueError("Secret file has an unexpected or duplicate variable")
        if not value or any(character.isspace() for character in value):
            raise ValueError("Secret values must be nonempty and contain no whitespace")
        values[key] = value
    if values.keys() != REQUIRED:
        raise ValueError("All three testnet secrets are required")
    return values


def main() -> int:
    arguments = sys.argv[1:]
    if not arguments or arguments[0] not in COMMANDS:
        raise ValueError("Usage: scripts/testnet.py {preflight|proof-check|register|run|publish|withdraw|status|control} [solver flags]")
    if arguments[0] == "run" and not any(argument.split("=", 1)[0] in {"-order", "--order"} for argument in arguments):
        raise ValueError("This test runner requires -order for authorized single-order execution")
    root = Path(__file__).resolve().parent.parent
    binary = root / "bin" / "goif"
    binary.parent.mkdir(exist_ok=True)
    subprocess.run(["go", "build", "-o", str(binary), "./cmd/goif"], cwd=root, check=True)
    secret_file = Path.home() / ".config" / "goif-solver" / "testnet.env"
    environment = os.environ.copy()
    environment.update(read_secrets(secret_file))
    os.chdir(root)
    os.execve(binary, [str(binary), *arguments], environment)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        if isinstance(error, ValueError):
            print(str(error), file=sys.stderr)
        else:
            print("Could not build the solver or open its owner-only secret file", file=sys.stderr)
        raise SystemExit(1) from None
