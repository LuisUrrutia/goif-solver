#!/usr/bin/env python3
"""Install official release binaries, verifying their published checksums."""
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import tarfile
import urllib.request
import zipfile

DEST = Path(__file__).resolve().parent.parent / "artifacts" / "tools"


def fetch(url):
    with urllib.request.urlopen(url, timeout=120) as response:
        return response.read()


def install(repo, version, archive, checksum, binary, member):
    target = DEST / binary
    marker = DEST / (binary + ".release")
    if target.exists() and marker.exists():
        data = json.loads(marker.read_text())
        if data.get("version") == version and data.get("sha256") == hashlib.sha256(target.read_bytes()).hexdigest():
            return
    base = f"https://github.com/{repo}/releases/download/{version}/"
    if checksum.startswith("sha256:"):
        expected = checksum.removeprefix("sha256:")
    else:
        checksums = fetch(base + checksum).decode()
        expected = next(line.split()[0] for line in checksums.splitlines() if line.split()[-1].lstrip("*") == archive)
    payload = fetch(base + archive)
    if hashlib.sha256(payload).hexdigest() != expected:
        raise SystemExit(f"Checksum mismatch: {archive}")
    if archive.endswith(".zip"):
        with zipfile.ZipFile(io.BytesIO(payload)) as package:
            data = package.read(member)
    else:
        with tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as package:
            stream = package.extractfile(member)
            if stream is None:
                raise SystemExit(f"Missing executable: {member}")
            data = stream.read()
    temporary = target.with_suffix(".tmp")
    temporary.write_bytes(data)
    temporary.chmod(0o755)
    os.replace(temporary, target)
    marker.write_text(json.dumps({"version": version, "sha256": hashlib.sha256(data).hexdigest()}))


def main():
    system = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
    arch = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}[platform.machine()]
    version = "2.14.0"
    stem = f"golangci-lint-{version}-{system}-{arch}"
    install("golangci/golangci-lint", "v" + version, stem + ".tar.gz", f"golangci-lint-{version}-checksums.txt", "golangci-lint", stem + "/golangci-lint")

    platforms = {
        ("linux", "amd64"): ("linux-x86_64", "bcb0d855e91f102f28a370e850f8566b3b44b79e6274d806ea5246837c0fd5ab"),
        ("linux", "arm64"): ("linux-aarch64", "0ef2ebf0b7e5a652b65c4cb96c6d9ffb3981a98547de3c764465bbf54a8d761a"),
        ("darwin", "arm64"): ("macos-aarch64", "92ff0889e16324801bc072692974bb67f8161e62010fc90f96c62a17f81f32c7"),
        ("darwin", "amd64"): ("macos-x86_64", "53c50a1605d0a6345d160a1a5a21db40bcf2bf9cd23c17f7c277a63a1bff3a7f"),
    }
    platform_name, digest = platforms[system, arch]
    install("JohnnyMorganz/StyLua", "v2.5.2", f"stylua-{platform_name}.zip", "sha256:" + digest, "stylua", "stylua")


if __name__ == "__main__":
    main()
