#!/usr/bin/env python3
"""Endpoint probe for the canonical Notice Place MCP point of every AI client.

For each client the probe takes the registration exactly as the harness uses it
(the same URL and the same headers) and performs a real Streamable HTTP MCP
handshake: `initialize`, the `initialized` notification and `tools/list`. The
session id returned by the server is carried into the follow-up request, so the
probe follows the same session semantics a real client uses.

Only `initialize` and `tools/list` are sent: nothing is notified, asked or
changed. Output is credential-redacted - server names, transports, tool counts
and config paths only. No bearer value, hash or fragment is printed.

This is an endpoint probe, not a harness readback: it proves that the
configured registration answers with its tool list. The effective tool list of a
harness itself is proven by that harness (for example `hermes mcp test
<server>` or a session that calls the tool).

Only HTTP registrations are probed. A stdio registration is reported as
NOT-PROBED: the canonical point is the shared HTTP endpoint, so a stdio entry
means the environment still points at a per-client process.

Usage:
    .venv/bin/python scripts/notice_place_endpoint_probe.py
    .venv/bin/python scripts/notice_place_endpoint_probe.py --json
"""

from __future__ import annotations

import argparse
import datetime
import json
import os
from pathlib import Path
import sys

import httpx

URL_MARK = 'notify.bezrabotnyi.com'
URL_PATH = '/mcp'
COMMANDS = ('ask-human-mcp', 'notify_mcp.py')
PROTOCOL_VERSION = '2024-11-05'
SESSION_HEADER = 'mcp-session-id'
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


def parse_payload(text: str) -> dict:
    """Read a Streamable HTTP response body: plain JSON or an SSE frame."""
    text = (text or '').strip()
    if not text:
        return {}
    if text.startswith('{') or text.startswith('['):
        try:
            return json.loads(text)
        except Exception:
            return {}
    chunks = [line[5:].strip() for line in text.splitlines() if line.startswith('data:')]
    for chunk in reversed(chunks):
        try:
            return json.loads(chunk)
        except Exception:
            continue
    return {}


def probe_registration(url: str, headers: dict, timeout: float = 20.0) -> dict:
    """Real Streamable HTTP session: initialize, initialized, tools/list."""
    request_headers = {
        'Content-Type': 'application/json',
        'Accept': 'application/json, text/event-stream',
    }
    request_headers.update(headers)
    with httpx.Client(timeout=timeout, follow_redirects=True) as client:
        init = client.post(url, headers=request_headers, json={
            'jsonrpc': '2.0', 'id': 1, 'method': 'initialize',
            'params': {'protocolVersion': PROTOCOL_VERSION, 'capabilities': {},
                       'clientInfo': {'name': 'gptadmin-notice-place-probe', 'version': '1'}},
        })
        init.raise_for_status()
        session_id = init.headers.get(SESSION_HEADER)
        body = parse_payload(init.text)
        if 'error' in body:
            raise RuntimeError('initialize returned an rpc error')
        info = (body.get('result') or {}).get('serverInfo') or {}
        if session_id:
            request_headers[SESSION_HEADER] = session_id
        try:
            client.post(url, headers=request_headers, json={
                'jsonrpc': '2.0', 'method': 'notifications/initialized'})
        except Exception:
            pass
        listed = client.post(url, headers=request_headers, json={
            'jsonrpc': '2.0', 'id': 2, 'method': 'tools/list', 'params': {}})
        listed.raise_for_status()
        payload = parse_payload(listed.text)
        if 'error' in payload:
            raise RuntimeError('tools/list returned an rpc error')
        tools = (payload.get('result') or {}).get('tools') or []
        return {'transport': 'streamable-http', 'server': info.get('name'), 'version': info.get('version'),
                'tool_count': len(tools), 'tools': sorted(str(tool.get('name')) for tool in tools),
                'session_id': bool(session_id)}


def probe_entry(spec: dict) -> dict:
    """Probe one registration; a stdio registration is reported, not probed."""
    command = spec.get('command')
    parts = [command] if isinstance(command, str) else list(command or []) if isinstance(command, list) else []
    if parts:
        return {'transport': 'stdio', 'probed': False,
                'error': 'stdio registration is not probed: the canonical point is the shared HTTP endpoint'}
    url = str(spec.get('url') or spec.get('target') or '').strip()
    if not url:
        return {'transport': 'none', 'probed': False, 'error': 'no url and no command'}
    headers = {str(k): str(v) for k, v in (spec.get('headers') or spec.get('http_headers') or {}).items()}
    result = probe_registration(url, headers)
    result['probed'] = True
    return result


def run() -> list[dict]:
    """Probe every Notice Place registration of this host."""
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    rows = []
    for client, path, name, spec in collect():
        row = {'client': client, 'config': path, 'server_name': name,
               'enabled': spec.get('enabled', True) is not False}
        if not row['enabled']:
            row['live'] = False
            row['probed'] = False
            row['error'] = 'disabled'
        else:
            try:
                row.update(probe_entry(spec))
                row['live'] = bool(row.get('probed'))
            except Exception as exc:
                row['live'] = False
                row['probed'] = False
                row['error'] = type(exc).__name__
        rows.append(row)
    return [{'readback_utc': stamp, 'host': os.uname().nodename, 'rows': rows}]


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--json', action='store_true')
    args = parser.parse_args()
    report = run()[0]
    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return
    print(f"host={report['host']} probe_utc={report['readback_utc']}")
    for row in report['rows']:
        if row.get('live'):
            detail = (f"tools={row.get('tool_count')} transport={row.get('transport')} "
                      f"server={row.get('server')} session={row.get('session_id')}")
        else:
            detail = row.get('error', 'not probed')
        print(f"  {row['client']:14s} {row['server_name']:12s} {'live' if row.get('live') else 'off':5s} {detail}"
              f"  <- {row['config']}")


if __name__ == '__main__':
    main()
