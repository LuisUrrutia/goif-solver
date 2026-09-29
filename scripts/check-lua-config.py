#!/usr/bin/env python3
"""Keep Redis globals consistent between LuaLS and luacheck."""
import json
from pathlib import Path
import re

root = Path(__file__).resolve().parent.parent
settings = json.loads((root / ".luarc.json").read_text())
lint = (root / ".luacheckrc").read_text()
match = re.search(r"globals\s*=\s*\{([^}]+)\}", lint)
assert match is not None
assert set(settings["diagnostics.globals"]) == set(re.findall(r'"([^"]+)"', match.group(1))) == {"redis", "KEYS", "ARGV", "cjson"}
assert settings["runtime.version"] == "Lua 5.1"
assert not settings.get("diagnostics.disable"), "Keep undefined-global diagnostics enabled"
print("LuaLS and luacheck agree on the Redis script environment.")
