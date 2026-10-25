#!/usr/bin/env python3
"""Installed readback of the Notice Place MCP point of every AI client.

For each client the readback takes the registration exactly as the harness uses
it (same URL, same headers, same command) and performs a real MCP handshake:
`initialize` plus `tools/list`. Only `initialize` and `tools/list` are sent -
nothing is notified, asked or changed.

Output is credential-redacted: server names, transports, tool counts and
config paths only. No bearer value, hash or fragment is printed.

Usage:
    python3 scripts/notice_place_readback.py            # human readable
    python3 scripts/notice_place_readback.py --json     # machine readable
"""

from __future__ import annotations

import argparse
import asyncio
import datetime
import json
import os
from pathlib import Path
import sys

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client

URL_MARK = 'notify.bezrabotnyi.com'
URL_PATH = '/mcp'
COMMANDS = ('ask-human-mcp', 'notify_mcp.py')
PLUGIN_ROOTS = {
    'opencode': ('.config/opencode/plugins/noticeplace', 'opencode.json', ('mcp',), None),
    'codex': ('.codex/plugins/cache/noticeplace-local/noticeplace', 'mcp.codex.json', ('mcpServers',),
              '.codex-plugin/plugin.json'),
    'zcode': ('.zcode/cli/plugins/cache/noticeplace-local/noticeplace', 'mcp.codex.json', ('mcpServers',),
              '.zcode-plugin/plugin.json'),
}


def load(path: Path):
    try:
        text = path.read_text(encoding='utf-8')
    except Exception:
        return None
    if path.suffix in {'.yaml', '.yml'}:
        try:
            import yaml
            data = yaml.safe_load(text)
        except Exception:
            return None
        return data if isinstance(data, dict) else None
    try:
        data = json.loads(text)
    except Exception:
        return None
    return data if isinstance(data, dict) else None


def is_notice_place(spec) -> bool:
    """True when an MCP entry points at the Notice Place backend."""
    if not isinstance(spec, dict):
        return False
    url = str(spec.get('url') or spec.get('target') or '').strip()
    if url:
        parsed = url.split('://', 1)[-1]
        host = parsed.split('/')[0].split(':')[0]
        path = '/' + parsed.split('/', 1)[1] if '/' in parsed else '/'
        if host == URL_MARK and path.rstrip('/').startswith(URL_PATH):
            return True
    command = spec.get('command')
    parts = [command] if isinstance(command, str) else list(command or []) if isinstance(command, list) else []
    return any(str(part).rsplit('/', 1)[-1] in COMMANDS for part in parts)


def codex_entries(path: Path) -> dict:
    """Read the Codex MCP table, sub-tables folded into their server."""
    entries: dict[str, dict] = {}
    if not path.is_file():
        return entries
    current = ''
    current_sub = ''
    for line in path.read_text(encoding='utf-8').splitlines():
        stripped = line.strip()
        if stripped.startswith('[') and stripped.endswith(']'):
            header = stripped[1:-1].strip()
            if header.startswith('mcp_servers.'):
                rest = header[len('mcp_servers.'):]
                if '.' in rest:
                    owner, _, sub = rest.partition('.')
                    current = owner.strip('"')
                    current_sub = sub.strip('"')
                    entries.setdefault(current, {}).setdefault(current_sub, {})
                else:
                    current = rest.strip('"')
                    current_sub = ''
                    entries.setdefault(current, {})
                continue
            current = ''
            current_sub = ''
            continue
        if not current or '=' not in stripped:
            continue
        key, _, value = stripped.partition('=')
        value = value.strip().strip('"')
        if current_sub:
            entries[current][current_sub][key.strip()] = value
        else:
            entries[current][key.strip()] = value
    return entries


def collect() -> list[tuple[str, str, str, dict]]:
    """(client, config path, server name, registration) of every Notice Place entry."""
    home = Path(os.path.expanduser('~'))
    found: list[tuple[str, str, str, dict]] = []
    plans = [
        ('opencode', home / '.config/opencode/opencode.json', ('mcp',)),
        ('opencode', home / '.config/opencode/opencode.jsonc', ('mcp',)),
        ('hermes', home / '.hermes/config.yaml', ('mcp_servers',)),
        ('zcode', home / '.zcode/cli/config.json', ('mcp', 'servers')),
        ('fast-agent', home / '.fast-agent/fast-agent.yaml', ('mcp', 'servers')),
    ]
    for client, path, container in plans:
        data = load(path)
        node = data
        for key in container:
            node = node.get(key) if isinstance(node, dict) else None
        if not isinstance(node, dict):
            continue
        for name, spec in node.items():
            if is_notice_place(spec):
                found.append((client, str(path), name, spec))
    codex = home / '.codex/config.toml'
    for name, spec in codex_entries(codex).items():
        if is_notice_place(spec):
            spec = dict(spec)
            spec['enabled'] = str(spec.get('enabled', 'true')).strip().strip('"').lower() not in {'false', '0'}
            found.append(('codex', str(codex), name, spec))
    for client, (root, filename, container, manifest) in PLUGIN_ROOTS.items():
        base = home / root
        if not base.is_dir():
            continue
        for version in sorted(base.glob('*')):
            projection = version / filename
            if not projection.is_file():
                continue
            if manifest:
                declared = load(version / manifest) or {}
                if not declared.get('mcpServers'):
                    continue
            data = load(projection)
            node = data
            for key in container:
                node = node.get(key) if isinstance(node, dict) else None
            if not isinstance(node, dict):
                continue
            for name, spec in node.items():
                if name.lower() == 'noticeplace' and is_notice_place(spec):
                    found.append((client + '+plugin', str(projection), name, spec))
    return found


async def tools_of(spec) -> dict:
    """Real initialize + tools/list against the registration as configured."""
    command = spec.get('command')
    parts = [command] if isinstance(command, str) else list(command or []) if isinstance(command, list) else []
    if parts:
        env = dict(os.environ)
        for key, value in (spec.get('env') or spec.get('environment') or {}).items():
            env[str(key)] = str(value)
        params = StdioServerParameters(command=str(parts[0]), args=[str(p) for p in parts[1:]], env=env)
        async with stdio_client(params) as (read, write):
            async with ClientSession(read, write) as session:
                info = await session.initialize()
                tools = await session.list_tools()
        return {'transport': 'stdio', 'server': info.serverInfo.name, 'version': info.serverInfo.version,
                'tool_count': len(tools.tools), 'tools': sorted(t.name for t in tools.tools)}
    url = str(spec.get('url') or spec.get('target') or '').strip()
    headers = {str(k): str(v) for k, v in (spec.get('headers') or spec.get('http_headers') or {}).items()}
    from mcp.client.streamable_http import streamablehttp_client
    async with streamablehttp_client(url, headers=headers) as (read, write, _):
        async with ClientSession(read, write) as session:
            info = await session.initialize()
            tools = await session.list_tools()
    return {'transport': 'streamable-http', 'server': info.serverInfo.name, 'version': info.serverInfo.version,
            'tool_count': len(tools.tools), 'tools': sorted(t.name for t in tools.tools)}


async def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--json', action='store_true')
    args = parser.parse_args()
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    rows = []
    for client, path, name, spec in collect():
        row = {'client': client, 'config': path, 'server_name': name,
               'enabled': spec.get('enabled', True) is not False}
        if row['enabled']:
            try:
                row.update(await tools_of(spec))
                row['live'] = True
            except Exception as exc:
                row['live'] = False
                row['error'] = type(exc).__name__
        else:
            row['live'] = False
        rows.append(row)
    if args.json:
        print(json.dumps({'readback_utc': stamp, 'host': os.uname().nodename, 'rows': rows},
                         ensure_ascii=False, indent=2))
        return
    print(f'host={os.uname().nodename} readback_utc={stamp}')
    for row in rows:
        state = 'live' if row.get('live') else 'off'
        detail = f"tools={row.get('tool_count')} transport={row.get('transport')} server={row.get('server')}" \
            if row.get('live') else row.get('error', 'disabled')
        print(f"  {row['client']:14s} {row['server_name']:12s} {state:5s} {detail}  <- {row['config']}")


if __name__ == '__main__':
    asyncio.run(main())
