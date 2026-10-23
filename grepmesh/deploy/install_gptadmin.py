#!/usr/bin/env python3
"""Register the local GrepMesh endpoint in a GPTAdmin ShellMCP runtime."""

from __future__ import annotations

import argparse
import json
import shutil
from datetime import datetime, timezone
from pathlib import Path


BEGIN_MARKER = "<!-- grepmesh-routing:begin -->"
END_MARKER = "<!-- grepmesh-routing:end -->"
SECTION_HEADING = "## GrepMesh search routing"
INSTRUCTION = (
    "For a known local checkout or explicitly named local path, prefer "
    "bounded shell `rg`; it is faster and avoids unnecessary mesh fan-out. "
    "Use GrepMesh when locations are unknown, searches must span configured "
    "mesh scopes or hosts, or local search cannot express the query. Start "
    "with `search` or `find_paths`, then use `read_text` for exact result "
    "paths. Report partial host failures rather than treating incomplete "
    "results as complete."
)
BLOCK = f"{BEGIN_MARKER}\n{INSTRUCTION}\n{END_MARKER}"
SECTION = f"{SECTION_HEADING}\n\n{BLOCK}"
LEGACY_INSTRUCTIONS = (
    "ИСПОЛЬЗУЙ МЕНЯ ДЛЯ ПОИСКА. Use the GrepMesh MCP before shell find, "
    "grep, rg, or repository-wide scanning when files may be local or on "
    "another mesh host. Start with search_text or find_paths, then use "
    "read_text for the exact file.",
    "ИСПОЛЬЗУЙ МЕНЯ ДЛЯ ПОИСКА. Use the `grepmesh` MCP before shell `find`, "
    "`grep`, `rg`, or repository-wide scanning when files may be local or "
    "on another mesh host. Start with `search_text` or `find_paths`, then "
    "use `read_text` for the exact file.",
    "ИСПОЛЬЗУЙ МЕНЯ ДЛЯ ПОИСКА. Use the `grepmesh` MCP before shell "
    "`find`, `grep`, `rg`, or repository-wide scanning when files may be "
    "local or on another mesh host. Start with `search_text` or "
    "`find_paths`, then use `read_text` for the exact file. Use shell search "
    "only when GrepMesh is unavailable or cannot express the query.",
)


def replace_instruction_section(current: str) -> str:
    """Install or replace one GrepMesh routing block without touching peers."""
    marker_count = current.count(BEGIN_MARKER), current.count(END_MARKER)
    if marker_count != (0, 0):
        if marker_count != (1, 1):
            raise ValueError("malformed or duplicate GrepMesh routing markers")
        start = current.index(BEGIN_MARKER)
        end = current.index(END_MARKER, start) + len(END_MARKER)
        if current.find(BEGIN_MARKER, start + len(BEGIN_MARKER)) >= 0 or current.find(END_MARKER, end) >= 0:
            raise ValueError("malformed GrepMesh routing marker order")
        return current[:start] + BLOCK + current[end:]

    heading_count = current.count(SECTION_HEADING)
    if heading_count:
        if heading_count != 1:
            raise ValueError("duplicate unmarked GrepMesh routing sections")
        start = current.index(SECTION_HEADING)
        next_heading = current.find("\n## ", start + len(SECTION_HEADING))
        end = len(current) if next_heading < 0 else next_heading + 1
        old_section = current[start:end]
        legacy_matches = [legacy for legacy in LEGACY_INSTRUCTIONS if legacy in old_section]
        if not legacy_matches:
            raise ValueError("unrecognized or duplicate unmarked GrepMesh routing instruction")
        legacy = max(legacy_matches, key=len)
        if old_section.count(legacy) != 1:
            raise ValueError("unrecognized or duplicate unmarked GrepMesh routing instruction")
        instruction_start = start + old_section.index(legacy)
        return current[:instruction_start] + BLOCK + current[instruction_start + len(legacy):]

    if any(legacy in current for legacy in LEGACY_INSTRUCTIONS):
        raise ValueError("legacy GrepMesh instruction has no section heading")
    return current.rstrip() + ("\n\n" if current.strip() else "") + SECTION + "\n"


def backup(path: Path, stamp: str) -> None:
    if path.exists():
        shutil.copy2(path, path.with_name(f"{path.name}.bak.grepmesh-{stamp}"))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config-dir", type=Path, default=Path("/etc/gptadmin"))
    parser.add_argument("--runtime-home", type=Path, default=Path("/home/admin"))
    args = parser.parse_args()
    root = args.config_dir.resolve()
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")

    supervisor_path = root / "mcp-supervisor.json"
    backup(supervisor_path, stamp)
    supervisor = json.loads(supervisor_path.read_text(encoding="utf-8"))
    supervisor = [entry for entry in supervisor if entry.get("ref") != "GrepMesh"]
    supervisor.append(
        {
            "ref": "GrepMesh",
            "name": "grepmesh",
            "command": "/usr/local/bin/npx",
            "args": ["-y", "mcp-remote", "http://127.0.0.1:9419/mcp"],
            "env": {},
            "cwd": "/home/admin",
            "user": "admin",
            "enabled": True,
            "url": "http://127.0.0.1:9419/mcp",
            "transport": "streamable-http",
        }
    )
    supervisor_path.write_text(json.dumps(supervisor, indent=2) + "\n", encoding="utf-8")

    mcp_path = root / "mcp.json"
    backup(mcp_path, stamp)
    mcp = json.loads(mcp_path.read_text(encoding="utf-8"))
    mcp.setdefault("mcpServers", {})["grepmesh"] = {
        "command": "/usr/local/bin/npx",
        "args": ["-y", "mcp-remote", "http://127.0.0.1:9419/mcp"],
        "env": {},
        "cwd": "/home/admin",
        "stdio_format": "ndjson",
        "transport": "streamable-http",
        "enabled": True,
        "agent_id": "GrepMesh",
        "run_as_user": "admin",
        "url": "http://127.0.0.1:9419/mcp",
    }
    mcp_path.write_text(json.dumps(mcp, indent=2) + "\n", encoding="utf-8")

    agents_dir = root / "mcp-agents.d"
    agents_dir.mkdir(parents=True, exist_ok=True)
    agent_path = agents_dir / "grepmesh.json"
    backup(agent_path, stamp)
    agent = {
        "agent_id": "GrepMesh",
        "name": "grepmesh via admin-node-100",
        "hub_url": "http://127.0.0.1:9001",
        "token_file": str(root / "mcp-relay.token"),
        "command": "/usr/local/bin/npx",
        "args": ["-y", "mcp-remote", "http://127.0.0.1:9419/mcp"],
        "cwd": "/home/admin",
        "stdio_format": "ndjson",
        "run_as_user": "admin",
        "auto_start": True,
        "mode": "agent-config",
    }
    agent_path.write_text(json.dumps(agent, indent=2) + "\n", encoding="utf-8")
    agent_path.chmod(0o644)

    runtime_agents_dir = args.runtime_home.resolve() / ".config/gptadmin/mcp-agents.d"
    runtime_agents_dir.mkdir(parents=True, exist_ok=True)
    runtime_agent_path = runtime_agents_dir / "grepmesh.json"
    runtime_agent = dict(agent)
    runtime_agent["token_file"] = str(args.runtime_home.resolve() / ".config/gptadmin/mcp-relay.token")
    backup(runtime_agent_path, stamp)
    runtime_agent_path.write_text(json.dumps(runtime_agent, indent=2) + "\n", encoding="utf-8")
    runtime_agent_path.chmod(0o644)

    instructions_path = root / "startup_instructions.md"
    current = instructions_path.read_text(encoding="utf-8") if instructions_path.exists() else ""
    updated = replace_instruction_section(current)
    if updated != current:
        backup(instructions_path, stamp)
        instructions_path.write_text(updated, encoding="utf-8")
    print(json.dumps({"registry": "grepmesh", "agent_config": True, "instructions": True}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
