#!/usr/bin/env python3
"""Install the reviewed transport through one supported ZCode MCP definition."""
import argparse
import hashlib
import json
import os
import re
import sys
import tempfile
from pathlib import Path
from urllib.parse import urlsplit


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def atomic(path, raw, mode=0o600):
    fd, name = tempfile.mkstemp(prefix=".mcp-recovery-", dir=path.parent)
    try:
        os.fchmod(fd, mode)
        with os.fdopen(fd, "wb") as out:
            out.write(raw)
            out.flush()
            os.fsync(out.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def install(config, name, state, helper, expected):
    config, state, helper = (Path(p).resolve() for p in (config, state, helper))
    if not re.fullmatch(r"[A-Za-z0-9_-]{1,80}", name):
        raise ValueError("Expected a single MCP definition name")
    raw = config.read_bytes()
    if sha(raw) != expected:
        raise RuntimeError("ZCode config changed since review; no config write")
    data = json.loads(raw)
    original = data["mcp"]["servers"][name]
    state.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(state, 0o700)
    executable, upstream = state / "managed_mcp_stdio.py", state / "upstream.json"
    entry = {"type": "stdio", "command": sys.executable,
             "args": [str(executable), "--upstream", str(upstream)],
             "enabled": original.get("enabled", True)}
    if original == entry:
        if not upstream.is_file() or executable.read_bytes() != helper.read_bytes():
            raise RuntimeError("Installed transport differs from reviewed artifact")
        return {"changed": False, "config": str(config), "server": name}
    url = urlsplit(original.get("url", ""))
    if (original.get("type") != "http" or url.scheme != "https" or not url.hostname
            or url.username or url.password or url.query or url.fragment
            or not re.fullmatch(r"/connection/[^/]+/mcp", url.path)):
        raise ValueError("Only the existing managed HTTPS binding can be installed")
    if upstream.exists() or executable.exists():
        raise RuntimeError("Private transport state already exists; reconcile before installing")
    backup = state / ("config-before-" + expected + ".json")
    atomic(backup, raw)
    atomic(upstream, json.dumps(original).encode())
    atomic(executable, helper.read_bytes(), 0o700)
    data["mcp"]["servers"][name] = entry
    proposed = (json.dumps(data, ensure_ascii=False, indent=2) + "\n").encode()
    if config.read_bytes() != raw:
        raise RuntimeError("ZCode config changed during staging; original config retained")
    atomic(config, proposed)
    receipt = {"changed": True, "config": str(config), "server": name,
               "before": expected, "after": sha(proposed), "helperSha256": sha(helper.read_bytes()),
               "backup": str(backup), "upstream": str(upstream)}
    atomic(state / "receipt.json", json.dumps(receipt, indent=2).encode())
    return receipt


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--config", type=Path, required=True)
    p.add_argument("--server", required=True)
    p.add_argument("--state-dir", type=Path, required=True)
    p.add_argument("--expected-sha256", required=True)
    args = p.parse_args()
    print(json.dumps(install(args.config, args.server, args.state_dir,
                             Path(__file__).with_name("managed_mcp_stdio.py"), args.expected_sha256)))


if __name__ == "__main__":
    main()
