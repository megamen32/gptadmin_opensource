#!/usr/bin/env python3
from __future__ import annotations

import base64
import hashlib
import hmac
import json
import os
import socket
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

ENV_FILE = Path(os.environ.get("GPTADMIN_ENV_FILE", "/etc/gptadmin/gptadmin.env"))
BASE = os.environ.get("GPTADMIN_ACCEPTANCE_BASE", "http://127.0.0.1:9001").rstrip("/")
MARKER = os.environ.get("GPTADMIN_ACCEPTANCE_MARKER", "gptadmin-runtime-acceptance")


def read_env() -> dict[str, str]:
    out: dict[str, str] = {}
    for raw in ENV_FILE.read_text(encoding="utf-8").splitlines():
        raw = raw.strip()
        if not raw or raw.startswith("#") or "=" not in raw:
            continue
        key, value = raw.split("=", 1)
        out[key] = value.strip().strip('"').strip("'")
    return out


def b64url(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def token(env: dict[str, str]) -> str:
    secret = env.get("OAUTH_CLIENT_SECRET")
    if not secret:
        raise RuntimeError("OAUTH_CLIENT_SECRET missing after setup")
    origin = (env.get("PUBLIC_ORIGIN") or env.get("MCP_RESOURCE") or env.get("HUB_PUBLIC_URL") or env.get("HUB_URL") or BASE).rstrip("/")
    now = int(time.time())
    header = {"alg": "HS256", "typ": "JWT"}
    payload = {
        "sub": "admin",
        "scope": "gptadmin.read gptadmin.exec",
        "access_mode": "full",
        "client_id": "runtime-acceptance",
        "iss": origin,
        "aud": origin,
        "resource": origin,
        "iat": now,
        "exp": now + 1800,
        "kid": env.get("GPTADMIN_JWT_KEY_ID", "gptadmin-hs256-v1"),
    }
    unsigned = b64url(json.dumps(header, separators=(",", ":")).encode()) + "." + b64url(json.dumps(payload, separators=(",", ":")).encode())
    sig = hmac.new(secret.encode(), unsigned.encode(), hashlib.sha256).digest()
    return unsigned + "." + b64url(sig)


def request(method: str, path: str, bearer: str, payload=None, timeout=20):
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(
        BASE + path,
        data=data,
        method=method,
        headers={
            "Authorization": "Bearer " + bearer,
            "Content-Type": "application/json",
            "Accept": "application/json, text/yaml, */*",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read()
            ctype = resp.headers.get("Content-Type", "")
            return json.loads(body) if "json" in ctype else body.decode()
    except urllib.error.HTTPError as exc:
        body = exc.read().decode(errors="replace")
        raise RuntimeError(f"{method} {path} -> HTTP {exc.code}: {body[:1200]}") from exc


def wait_for_shell(bearer: str) -> dict:
    deadline = time.monotonic() + 35
    expected = os.environ.get("GPTADMIN_ACCEPTANCE_SHELL") or ("shell:" + socket.gethostname())
    last = None
    while time.monotonic() < deadline:
        try:
            payload = request("GET", "/mcp-relay/servers?detail=full", bearer, timeout=4)
            rows = payload.get("servers", []) if isinstance(payload, dict) else []
            online = [row for row in rows if str(row.get("server_id") or "").startswith("shell:") and row.get("status") == "online"]
            for row in online:
                if str(row.get("server_id") or "") == expected:
                    return row
            if len(online) == 1:
                return online[0]
            last = {"expected": expected, "online_shells": [row.get("server_id") for row in online]}
        except Exception as exc:
            last = repr(exc)
        time.sleep(0.5)
    raise RuntimeError(f"local online ShellMCP target {expected} not found after install: {last!r}")


def wait_for_file(shell_id: str, bearer: str) -> dict:
    expected = "file:" + shell_id.split(":", 1)[1]
    deadline = time.monotonic() + 35
    last = None
    while time.monotonic() < deadline:
        payload = request("GET", "/mcp-relay/servers?detail=full", bearer, timeout=4)
        rows = payload.get("servers", []) if isinstance(payload, dict) else []
        for row in rows:
            if str(row.get("server_id") or "") == expected and row.get("status") == "online":
                return row
        last = payload
        time.sleep(0.5)
    raise RuntimeError(f"paired file target {expected} did not become online: {last!r}")


def recursive_contains(value, needle: str) -> bool:
    if isinstance(value, str):
        return needle in value
    if isinstance(value, dict):
        return any(recursive_contains(v, needle) for v in value.values())
    if isinstance(value, list):
        return any(recursive_contains(v, needle) for v in value)
    return False


def recursive_find_key(value, key: str):
    if isinstance(value, dict):
        if key in value:
            return value[key]
        for child in value.values():
            found = recursive_find_key(child, key)
            if found not in (None, ''):
                return found
    elif isinstance(value, list):
        for child in value:
            found = recursive_find_key(child, key)
            if found not in (None, ''):
                return found
    return None


def await_job(result, bearer: str):
    if not isinstance(result, dict):
        return result
    job_id = str(result.get("job_id") or "")
    if not job_id:
        return result
    deadline = time.monotonic() + 35
    current = result
    while time.monotonic() < deadline:
        current = request("GET", "/mcp-relay/job/" + urllib.parse.quote(job_id, safe=""), bearer, timeout=6)
        status = str(current.get("status") or "") if isinstance(current, dict) else ""
        if status in {"completed", "success"}:
            return current
        if status in {"failed", "error", "cancelled"}:
            raise RuntimeError(f"job {job_id} failed: {current}")
        time.sleep(0.25)
    raise RuntimeError(f"job {job_id} timed out; last={current}")


def assert_ram_probe_root() -> Path:
    root = Path('/dev/shm')
    if not root.is_dir():
        raise RuntimeError('/dev/shm is unavailable; refusing disk-backed acceptance file churn')
    mounts = Path('/proc/mounts').read_text(encoding='utf-8', errors='replace').splitlines()
    if not any(line.split()[1:3] == ['/dev/shm', 'tmpfs'] for line in mounts if len(line.split()) >= 3):
        raise RuntimeError('/dev/shm is not tmpfs; refusing disk-backed acceptance file churn')
    return root


def ram_probe_path(suffix: str) -> Path:
    safe = ''.join(ch if ch.isalnum() or ch in '-_' else '-' for ch in MARKER)
    return assert_ram_probe_root() / f'gptadmin-{safe}-{os.getpid()}-{suffix}.txt'


def custom_gpt_bash_ram_roundtrip(shell_id: str, bearer: str) -> None:
    """Create/read/delete a tmpfs file through real shell_exec calls."""
    probe = ram_probe_path('custom-gpt-bash')
    content = MARKER + '-custom-gpt-bash-file'
    probe.unlink(missing_ok=True)
    try:
        created = await_job(request('POST', '/mcp-relay/call', bearer, {
            'target': shell_id,
            'tool': 'shell_exec',
            'arguments': {
                'cmd': f"set -eu; printf '%s\\n' '{content}' > '{probe}'; stat -c '%s' '{probe}'",
                'run_as_user': 'root',
            },
        }, timeout=30), bearer)
        if not probe.is_file() or probe.read_text(encoding='utf-8') != content + '\n':
            raise RuntimeError(f'Custom GPT bash RAM create failed: {created}')

        read_back = await_job(request('POST', '/mcp-relay/call', bearer, {
            'target': shell_id,
            'tool': 'shell_exec',
            'arguments': {'cmd': f"cat '{probe}'", 'run_as_user': 'root'},
        }, timeout=30), bearer)
        if not recursive_contains(read_back, content):
            raise RuntimeError(f'Custom GPT bash RAM read failed: {read_back}')

        removed = await_job(request('POST', '/mcp-relay/call', bearer, {
            'target': shell_id,
            'tool': 'shell_exec',
            'arguments': {
                'cmd': f"rm -f '{probe}'; test ! -e '{probe}'; printf removed",
                'run_as_user': 'root',
            },
        }, timeout=30), bearer)
        if probe.exists() or not recursive_contains(removed, 'removed'):
            raise RuntimeError(f'Custom GPT bash RAM delete failed: {removed}')
    finally:
        probe.unlink(missing_ok=True)


def custom_gpt_runtime_probe(shell_id: str, file_id: str, bearer: str) -> None:
    shell_schema = await_job(request('POST', '/mcp-relay/tools', bearer, {'target': shell_id}), bearer)
    if not recursive_contains(shell_schema, 'shell_exec'):
        raise RuntimeError('Custom GPT shell schema did not expose shell_exec')
    if recursive_contains(shell_schema, 'file_editor'):
        raise RuntimeError('file_editor leaked into shell target')

    file_schema = await_job(request('POST', '/mcp-relay/tools', bearer, {'target': file_id}), bearer)
    if not recursive_contains(file_schema, 'file_editor'):
        raise RuntimeError('paired file target did not expose file_editor')
    if recursive_contains(file_schema, 'shell_exec'):
        raise RuntimeError('shell_exec leaked into file target')

    uptime_marker = MARKER + '-custom-gpt-uptime'
    uptime_result = await_job(
        request(
            'POST', '/mcp-relay/call', bearer,
            {
                'target': shell_id,
                'tool': 'shell_exec',
                'arguments': {'cmd': f"printf '{uptime_marker}:'; uptime", 'run_as_user': 'root'},
            },
            timeout=30,
        ),
        bearer,
    )
    if not recursive_contains(uptime_result, uptime_marker) or not recursive_contains(uptime_result, 'load average'):
        raise RuntimeError(f'Custom GPT uptime execution did not return real uptime output: {uptime_result}')

    custom_gpt_bash_ram_roundtrip(shell_id, bearer)

    probe = ram_probe_path('custom-gpt')
    probe.unlink(missing_ok=True)
    try:
        created = await_job(request('POST', '/mcp-relay/call', bearer, {
            'target': file_id, 'tool': 'file_editor',
            'arguments': {'action': 'create', 'path': str(probe), 'content': 'alpha\n'},
        }), bearer)
        if not probe.is_file() or probe.read_text(encoding='utf-8') != 'alpha\n':
            raise RuntimeError(f'Custom GPT RAM file create failed: {created}')

        edited = await_job(request('POST', '/mcp-relay/call', bearer, {
            'target': file_id, 'tool': 'file_editor',
            'arguments': {'action': 'str_replace', 'path': str(probe), 'old_text': 'alpha', 'new_text': 'beta'},
        }), bearer)
        if probe.read_text(encoding='utf-8') != 'beta\n' or not recursive_contains(edited, 'beta'):
            raise RuntimeError(f'Custom GPT RAM file edit failed: {edited}')

        deleted = await_job(request('POST', '/mcp-relay/call', bearer, {
            'target': file_id, 'tool': 'file_editor',
            'arguments': {'action': 'delete', 'path': str(probe)},
        }), bearer)
        if probe.exists():
            raise RuntimeError(f'Custom GPT RAM file delete failed: {deleted}')
    finally:
        probe.unlink(missing_ok=True)


def mcp_endpoint_path(server: dict) -> str:
    meta = server.get('meta') if isinstance(server.get('meta'), dict) else {}
    endpoint = str(meta.get('public_mcp_endpoint') or '')
    if endpoint.startswith(BASE):
        return endpoint[len(BASE):]
    if endpoint.startswith('http://') or endpoint.startswith('https://'):
        return urllib.parse.urlsplit(endpoint).path
    slug = str(meta.get('public_mcp_slug') or '')
    if not slug:
        raise RuntimeError(f'discover did not expose MCP slug/endpoint: {server}')
    return f'/server/{slug}/mcp'


def mcp_rpc(path: str, request_id: int, method: str, params: dict, bearer: str, *, allow_async: bool = False):
    payload = request(
        'POST', path, bearer,
        {'jsonrpc': '2.0', 'id': request_id, 'method': method, 'params': params},
        timeout=30,
    )
    if isinstance(payload, dict) and payload.get('error'):
        error = payload.get('error') if isinstance(payload.get('error'), dict) else {}
        data = error.get('data') if isinstance(error.get('data'), dict) else {}
        if allow_async and error.get('code') == -32001 and data.get('job_id'):
            return await_job(data, bearer)
        raise RuntimeError(f'MCP {method} failed: {payload}')
    if not isinstance(payload, dict) or 'result' not in payload:
        raise RuntimeError(f'MCP {method} returned invalid response: {payload}')
    return payload['result']


def native_mcp_bash_ram_roundtrip(shell_path: str, bearer: str) -> None:
    """Exercise tmpfs create/read/delete through native MCP shell_exec."""
    probe = ram_probe_path('native-mcp-bash')
    content = MARKER + '-native-mcp-bash-file'
    probe.unlink(missing_ok=True)
    try:
        created = mcp_rpc(shell_path, 31, 'tools/call', {
            'name': 'shell_exec',
            'arguments': {
                'cmd': f"set -eu; printf '%s\\n' '{content}' > '{probe}'; stat -c '%s' '{probe}'",
                'run_as_user': 'root',
            },
        }, bearer, allow_async=True)
        if not probe.is_file() or probe.read_text(encoding='utf-8') != content + '\n':
            raise RuntimeError(f'native MCP bash RAM create failed: {created}')

        read_back = mcp_rpc(shell_path, 32, 'tools/call', {
            'name': 'shell_exec',
            'arguments': {'cmd': f"cat '{probe}'", 'run_as_user': 'root'},
        }, bearer, allow_async=True)
        if not recursive_contains(read_back, content):
            raise RuntimeError(f'native MCP bash RAM read failed: {read_back}')

        removed = mcp_rpc(shell_path, 33, 'tools/call', {
            'name': 'shell_exec',
            'arguments': {
                'cmd': f"rm -f '{probe}'; test ! -e '{probe}'; printf removed",
                'run_as_user': 'root',
            },
        }, bearer, allow_async=True)
        if probe.exists() or not recursive_contains(removed, 'removed'):
            raise RuntimeError(f'native MCP bash RAM delete failed: {removed}')
    finally:
        probe.unlink(missing_ok=True)


def native_mcp_runtime_probe(shell: dict, file_target: dict, bearer: str) -> None:
    shell_path = mcp_endpoint_path(shell)
    initialized = mcp_rpc(shell_path, 1, 'initialize', {
        'protocolVersion': '2024-11-05',
        'capabilities': {},
        'clientInfo': {'name': 'gptadmin-runtime-acceptance', 'version': '1'},
    }, bearer)
    if not isinstance(initialized, dict) or not initialized.get('serverInfo'):
        raise RuntimeError(f'MCP shell initialize invalid: {initialized}')
    listed = mcp_rpc(shell_path, 2, 'tools/list', {}, bearer)
    if not recursive_contains(listed, 'shell_exec'):
        raise RuntimeError('native MCP shell tools/list did not expose shell_exec')

    uptime_marker = MARKER + '-native-mcp-uptime'
    uptime_called = mcp_rpc(shell_path, 3, 'tools/call', {
        'name': 'shell_exec',
        'arguments': {'cmd': f"printf '{uptime_marker}:'; uptime", 'run_as_user': 'root'},
    }, bearer, allow_async=True)
    if not recursive_contains(uptime_called, uptime_marker) or not recursive_contains(uptime_called, 'load average'):
        raise RuntimeError(f'native MCP uptime execution did not return real uptime output: {uptime_called}')

    native_mcp_bash_ram_roundtrip(shell_path, bearer)

    file_path = mcp_endpoint_path(file_target)
    file_initialized = mcp_rpc(file_path, 11, 'initialize', {
        'protocolVersion': '2024-11-05',
        'capabilities': {},
        'clientInfo': {'name': 'gptadmin-runtime-file-acceptance', 'version': '1'},
    }, bearer)
    if not isinstance(file_initialized, dict) or not file_initialized.get('serverInfo'):
        raise RuntimeError(f'MCP file initialize invalid: {file_initialized}')
    file_tools = mcp_rpc(file_path, 12, 'tools/list', {}, bearer)
    if not recursive_contains(file_tools, 'file_editor'):
        raise RuntimeError('native MCP file tools/list did not expose file_editor')
    if recursive_contains(file_tools, 'shell_exec'):
        raise RuntimeError('native MCP file target exposed shell_exec')

    probe = ram_probe_path('native-mcp')
    probe.unlink(missing_ok=True)
    try:
        created = mcp_rpc(file_path, 13, 'tools/call', {
            'name': 'file_editor',
            'arguments': {'action': 'create', 'path': str(probe), 'content': 'one\n'},
        }, bearer, allow_async=True)
        if not probe.is_file() or probe.read_text(encoding='utf-8') != 'one\n':
            raise RuntimeError(f'native MCP RAM file create failed: {created}')

        edited = mcp_rpc(file_path, 14, 'tools/call', {
            'name': 'file_editor',
            'arguments': {'action': 'str_replace', 'path': str(probe), 'old_text': 'one', 'new_text': 'two'},
        }, bearer, allow_async=True)
        if probe.read_text(encoding='utf-8') != 'two\n' or not recursive_contains(edited, 'two'):
            raise RuntimeError(f'native MCP RAM file edit failed: {edited}')

        deleted = mcp_rpc(file_path, 15, 'tools/call', {
            'name': 'file_editor',
            'arguments': {'action': 'delete', 'path': str(probe)},
        }, bearer, allow_async=True)
        if probe.exists():
            raise RuntimeError(f'native MCP RAM file delete failed: {deleted}')
    finally:
        probe.unlink(missing_ok=True)

    # System installs run the ShellMCP runtime as root so the paired file target
    # can edit system-owned files, while shell_exec still defaults to the configured
    # operator account. Verify ownership/mode preservation and reversible restore.
    privileged = ram_probe_path('root-owned')
    privileged.unlink(missing_ok=True)
    checkpoint_id = None
    safety_id = None
    try:
        privileged.write_text('root-alpha\n', encoding='utf-8')
        os.chown(privileged, 0, 0)
        os.chmod(privileged, 0o640)
        before = privileged.stat()

        viewed = mcp_rpc(file_path, 21, 'tools/call', {
            'name': 'file_editor',
            'arguments': {'action': 'view', 'path': str(privileged), 'start_line': 1, 'end_line': 1},
        }, bearer, allow_async=True)
        if not recursive_contains(viewed, 'root-alpha') or not recursive_contains(viewed, '1:'):
            raise RuntimeError(f'privileged file view did not return line ids: {viewed}')

        edited_root = mcp_rpc(file_path, 22, 'tools/call', {
            'name': 'file_editor',
            'arguments': {'action': 'str_replace', 'path': str(privileged), 'old_text': 'root-alpha', 'new_text': 'root-beta'},
        }, bearer, allow_async=True)
        after_edit = privileged.stat()
        if privileged.read_text(encoding='utf-8') != 'root-beta\n':
            raise RuntimeError(f'privileged file edit failed: {edited_root}')
        if (after_edit.st_uid, after_edit.st_gid, after_edit.st_mode & 0o777) != (before.st_uid, before.st_gid, before.st_mode & 0o777):
            raise RuntimeError(f'privileged edit changed metadata: before={before} after={after_edit}')

        checkpoint = mcp_rpc(file_path, 23, 'tools/call', {
            'name': 'file_checkpoint',
            'arguments': {'action': 'create', 'path': str(privileged), 'name': f'{MARKER}-root-{os.getpid()}', 'ttl_days': 1},
        }, bearer, allow_async=True)
        checkpoint_id = recursive_find_key(checkpoint, 'checkpoint_id')
        if not checkpoint_id:
            raise RuntimeError(f'checkpoint id missing: {checkpoint}')

        privileged.write_text('outside-change\n', encoding='utf-8')
        os.chmod(privileged, 0o600)
        restored = mcp_rpc(file_path, 24, 'tools/call', {
            'name': 'file_checkpoint',
            'arguments': {'action': 'restore', 'checkpoint_id': checkpoint_id},
        }, bearer, allow_async=True)
        safety_id = recursive_find_key(restored, 'safety_checkpoint_id')
        after_restore = privileged.stat()
        if privileged.read_text(encoding='utf-8') != 'root-beta\n':
            raise RuntimeError(f'checkpoint restore did not restore content: {restored}')
        if (after_restore.st_uid, after_restore.st_gid, after_restore.st_mode & 0o777) != (0, 0, 0o640):
            raise RuntimeError(f'checkpoint restore did not restore root metadata: uid={after_restore.st_uid} gid={after_restore.st_gid} mode={oct(after_restore.st_mode & 0o777)}')
    finally:
        privileged.unlink(missing_ok=True)
        for rid, cp in ((25, checkpoint_id), (26, safety_id)):
            if cp:
                try:
                    mcp_rpc(file_path, rid, 'tools/call', {
                        'name': 'file_checkpoint', 'arguments': {'action': 'delete', 'checkpoint_id': cp},
                    }, bearer, allow_async=True)
                except Exception:
                    pass

def main() -> int:
    env = read_env()
    bearer = token(env)
    # The document imported by Custom GPT must come from the installed candidate.
    schema_text = request("GET", "/actions/openapi.yaml", bearer)
    if not isinstance(schema_text, str) or "operationId: discover" not in schema_text or "operationId: execute" not in schema_text:
        raise RuntimeError("installed OpenAPI contract is incomplete")
    shell = wait_for_shell(bearer)
    shell_id = str(shell["server_id"])
    file_target = wait_for_file(shell_id, bearer)
    file_id = str(file_target["server_id"])
    custom_gpt_runtime_probe(shell_id, file_id, bearer)
    native_mcp_runtime_probe(shell, file_target, bearer)
    print(json.dumps({
        "status": "passed",
        "shell": shell_id,
        "file": file_id,
        "custom_gpt_uptime": "passed",
        "custom_gpt_bash_ram_file_create_read_delete": "passed",
        "custom_gpt_ram_file_create_edit_delete": "passed",
        "native_mcp_uptime": "passed",
        "native_mcp_bash_ram_file_create_read_delete": "passed",
        "native_mcp_ram_file_create_edit_delete": "passed",
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
