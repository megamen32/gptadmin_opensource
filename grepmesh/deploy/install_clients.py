#!/usr/bin/env python3
"""Install the local GrepMesh endpoint into supported agent runtimes."""

from __future__ import annotations

import argparse
import json
import shutil
from datetime import datetime, timezone
from pathlib import Path

import yaml


INSTRUCTION = (
    "ИСПОЛЬЗУЙ МЕНЯ ДЛЯ ПОИСКА. Use the `grepmesh` MCP before shell "
    "`find`, `grep`, `rg`, or repository-wide scanning when files may be "
    "local or on another mesh host. Start with `search_text` or "
    "`find_paths`, then use `read_text` for the exact file. Use shell search "
    "only when GrepMesh is unavailable or cannot express the query."
)
SECTION = f"\n\n## GrepMesh search routing\n\n{INSTRUCTION}\n"


def backup(path: Path, stamp: str) -> None:
    if path.exists():
        shutil.copy2(path, path.with_name(f"{path.name}.bak.grepmesh-{stamp}"))


def install_opencode(home: Path, stamp: str) -> bool:
    path = home / ".config/opencode/opencode.json"
    if not path.exists():
        return False
    backup(path, stamp)
    data = json.loads(path.read_text(encoding="utf-8"))
    data.setdefault("mcp", {})["grepmesh"] = {
        "type": "remote",
        "url": "http://127.0.0.1:9419/mcp",
        "enabled": True,
    }
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return True


def install_codex(home: Path, stamp: str) -> bool:
    path = home / ".codex/config.toml"
    if not path.exists():
        return False
    text = path.read_text(encoding="utf-8")
    if "[mcp_servers.grepmesh]" not in text:
        backup(path, stamp)
        text = text.rstrip() + (
            "\n\n[mcp_servers.grepmesh]\n"
            'url = "http://127.0.0.1:9419/mcp"\n'
            "enabled = true\n"
        )
        path.write_text(text, encoding="utf-8")
    return True


def install_hermes(home: Path, stamp: str) -> bool:
    path = home / ".hermes/config.yaml"
    if not path.exists():
        return False
    backup(path, stamp)
    data = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    data.setdefault("mcp_servers", {})["grepmesh"] = {
        "url": "http://127.0.0.1:9419/mcp",
        "enabled": True,
        "connect_timeout": 10,
    }
    agent = data.setdefault("agent", {})
    current = agent.get("system_prompt") or ""
    if INSTRUCTION not in current:
        agent["system_prompt"] = (current.rstrip() + "\n\n" + INSTRUCTION).strip()
    path.write_text(yaml.safe_dump(data, sort_keys=False, allow_unicode=True), encoding="utf-8")
    return True


def install_instruction(path: Path, stamp: str) -> bool:
    if not path.exists():
        return False
    text = path.read_text(encoding="utf-8")
    if INSTRUCTION not in text:
        backup(path, stamp)
        path.write_text(text.rstrip() + SECTION, encoding="utf-8")
    return True


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--home", type=Path, default=Path.home())
    args = parser.parse_args()
    home = args.home.expanduser().resolve()
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    installed = {
        "opencode": install_opencode(home, stamp),
        "codex": install_codex(home, stamp),
        "hermes": install_hermes(home, stamp),
        "opencode_instruction": install_instruction(home / ".config/opencode/AGENTS.md", stamp),
        "codex_instruction": install_instruction(home / ".codex/AGENTS.md", stamp),
    }
    print(json.dumps(installed, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
