#!/usr/bin/env python3
"""Install the local GrepMesh endpoint into supported agent runtimes."""

from __future__ import annotations

import argparse
import json
import shutil
from datetime import datetime, timezone
from pathlib import Path

import yaml

from install_gptadmin import BEGIN_MARKER, END_MARKER, INSTRUCTION, replace_instruction_section

CLIENT_LEGACY_INSTRUCTION = (
    "ИСПОЛЬЗУЙ МЕНЯ ДЛЯ ПОИСКА. Use the `grepmesh` MCP before shell "
    "`find`, `grep`, `rg`, or repository-wide scanning when files may be "
    "local or on another mesh host. Start with `search_text` or "
    "`find_paths`, then use `read_text` for the exact file. Use shell search "
    "only when GrepMesh is unavailable or cannot express the query."
)
PROMPT_SECTION = f"{BEGIN_MARKER}\n{INSTRUCTION}\n{END_MARKER}"


def replace_prompt_section(current: str) -> str:
    """Replace the legacy bare prompt or one marked block, preserving peers."""
    if CLIENT_LEGACY_INSTRUCTION in current:
        if current.count(CLIENT_LEGACY_INSTRUCTION) != 1:
            raise ValueError("duplicate legacy GrepMesh prompt instructions")
        current = current.replace(CLIENT_LEGACY_INSTRUCTION, PROMPT_SECTION, 1)
    elif BEGIN_MARKER not in current and END_MARKER not in current:
        return current.rstrip() + ("\n\n" if current.strip() else "") + PROMPT_SECTION + "\n"

    begin_count, end_count = current.count(BEGIN_MARKER), current.count(END_MARKER)
    if (begin_count, end_count) != (1, 1):
        raise ValueError("malformed or duplicate GrepMesh prompt markers")
    start = current.index(BEGIN_MARKER)
    end = current.index(END_MARKER, start) + len(END_MARKER)
    return current[:start] + PROMPT_SECTION + current[end:]


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
    updated = replace_prompt_section(current)
    if updated != current:
        agent["system_prompt"] = updated
    path.write_text(yaml.safe_dump(data, sort_keys=False, allow_unicode=True), encoding="utf-8")
    return True


def install_instruction(path: Path, stamp: str) -> bool:
    if not path.exists():
        return False
    text = path.read_text(encoding="utf-8")
    updated = replace_instruction_section(text)
    if updated != text:
        backup(path, stamp)
        path.write_text(updated, encoding="utf-8")
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
