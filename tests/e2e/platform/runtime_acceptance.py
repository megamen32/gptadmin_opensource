#!/usr/bin/env python3
"""Real cross-platform GPTAdmin runtime acceptance.

Run this on a trusted Hub host. It never prints credentials. The test proves both
client surfaces (Actions relay and native MCP) by executing uptime and a real
filesystem roundtrip on the selected physical ShellMCP target.
"""
from __future__ import annotations

import argparse
import base64
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import time
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[3]
CLI = ROOT / "cli.py"
DEFAULT_ENV = Path("/etc/gptadmin/gptadmin.env")
DEFAULT_BASE = "http://127.0.0.1:9001"


def load_cli():
    loader = importlib.machinery.SourceFileLoader("gptadmin_platform_acceptance_cli", str(CLI))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    if spec is None:
        raise RuntimeError("cannot load cli.py")
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


def read_env(path: Path) -> dict[str, str]:
    out: dict[str, str] = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        raw = raw.strip()
        if not raw or raw.startswith("#") or "=" not in raw:
            continue
        key, value = raw.split("=", 1)
        out[key] = value.strip().strip('"').strip("'")
    return out


def http_json(base: str, bearer: str, method: str, path: str, payload=None, timeout: float = 30):
    data = None if payload is None else json.dumps(payload).encode()
    request = urllib.request.Request(
        base.rstrip("/") + path,
        data=data,
        method=method,
        headers={
            "Authorization": "Bearer " + bearer,
            "Content-Type": "application/json",
            "Accept": "application/json",
        },
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return json.loads(response.read() or b"{}")


def await_job(base: str, bearer: str, value, timeout: float = 60):
    if not isinstance(value, dict):
        return value
    job_id = str(value.get("job_id") or value.get("task_id") or "")
    if not job_id:
        return value
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        current = http_json(base, bearer, "GET", "/mcp-relay/job/" + urllib.parse.quote(job_id, safe=""), timeout=8)
        status = str(current.get("status") or "") if isinstance(current, dict) else ""
        if status in {"completed", "success"}:
            return current
        if status in {"failed", "error", "cancelled"}:
            raise RuntimeError(f"job {job_id} failed: {current}")
        time.sleep(0.2)
    raise RuntimeError(f"job {job_id} timed out")


def contains(value, needle: str) -> bool:
    return needle in json.dumps(value, ensure_ascii=False)


def relay_call(base: str, bearer: str, target: str, tool: str, arguments: dict):
    return await_job(
        base,
        bearer,
        http_json(base, bearer, "POST", "/mcp-relay/call", {
            "target": target,
            "tool": tool,
            "arguments": arguments,
        }),
    )


def endpoint_path(server: dict, base: str) -> str:
    meta = server.get("meta") if isinstance(server.get("meta"), dict) else {}
    endpoint = str(meta.get("public_mcp_endpoint") or "")
    parsed_base = urllib.parse.urlsplit(base)
    if endpoint:
        parsed = urllib.parse.urlsplit(endpoint)
        if parsed.path:
            return parsed.path
    slug = str(meta.get("public_mcp_slug") or "")
    if slug:
        return f"/server/{slug}/mcp"
    raise RuntimeError(f"target {server.get('server_id')} has no public MCP endpoint")


def mcp_rpc(base: str, bearer: str, path: str, request_id: int, method: str, params: dict, *, async_ok: bool = False):
    payload = http_json(base, bearer, "POST", path, {
        "jsonrpc": "2.0",
        "id": request_id,
        "method": method,
        "params": params,
    })
    if isinstance(payload, dict) and payload.get("error"):
        error = payload["error"] if isinstance(payload.get("error"), dict) else {}
        data = error.get("data") if isinstance(error.get("data"), dict) else {}
        if async_ok and error.get("code") == -32001 and data.get("job_id"):
            return await_job(base, bearer, data)
        raise RuntimeError(f"MCP {method} failed: {payload}")
    if not isinstance(payload, dict) or "result" not in payload:
        raise RuntimeError(f"MCP {method} returned invalid payload: {payload}")
    return payload["result"]


def powershell_encoded(script: str) -> str:
    return base64.b64encode(script.encode("utf-16le")).decode()


def platform_spec(platform: str, marker: str) -> dict[str, str]:
    if platform == "windows":
        uptime_ps = r'''Add-Type -TypeDefinition @"
using System; using System.Runtime.InteropServices;
public static class K { [DllImport("kernel32.dll")] public static extern ulong GetTickCount64(); }
"@; Write-Output ('GPTADMIN_UPTIME_MS='+[K]::GetTickCount64())'''
        uptime = "powershell.exe -NoProfile -EncodedCommand " + powershell_encoded(uptime_ps)
        shell_path = rf"C:\Temp\gptadmin-{marker}-shell.txt"
        editor_path = rf"C:\Temp\gptadmin-{marker}-editor.txt"
        def read_cmd(path: str) -> str:
            return "powershell.exe -NoProfile -EncodedCommand " + powershell_encoded(
                f"Get-Content -Raw -LiteralPath '{path}'"
            )
        def shell_roundtrip(path: str, content: str) -> str:
            script = (
                f"$ErrorActionPreference='Stop'; $p='{path}'; "
                f"Set-Content -LiteralPath $p -Value '{content}' -NoNewline; "
                f"Write-Output ('GPTADMIN_FILE_CONTENT='+(Get-Content -Raw -LiteralPath $p)); "
                f"Remove-Item -Force -LiteralPath $p; if(Test-Path -LiteralPath $p){{throw 'delete failed'}}; "
                "Write-Output 'GPTADMIN_FILE_DELETE=passed'"
            )
            return "powershell.exe -NoProfile -EncodedCommand " + powershell_encoded(script)
        return {
            "uptime_cmd": uptime,
            "uptime_marker": "GPTADMIN_UPTIME_MS=",
            "shell_path": shell_path,
            "editor_path": editor_path,
            "read_editor_cmd": read_cmd(editor_path),
            "shell_roundtrip_cmd": shell_roundtrip(shell_path, marker + "-shell"),
            "shell_content": marker + "-shell",
        }
    if platform == "android":
        shell_path = f"/data/data/com.termux/files/usr/tmp/gptadmin-{marker}-shell.txt"
        editor_path = f"/data/data/com.termux/files/usr/tmp/gptadmin-{marker}-editor.txt"
        content = marker + "-shell"
        return {
            "uptime_cmd": "printf 'GPTADMIN_UPTIME:'; uptime",
            "uptime_marker": "GPTADMIN_UPTIME:",
            "shell_path": shell_path,
            "editor_path": editor_path,
            "read_editor_cmd": f"cat '{editor_path}'",
            "shell_roundtrip_cmd": (
                f"set -eu; p='{shell_path}'; printf '%s' '{content}' > \"$p\"; "
                f"printf 'GPTADMIN_FILE_CONTENT:'; cat \"$p\"; rm -f \"$p\"; "
                "test ! -e \"$p\"; echo; echo GPTADMIN_FILE_DELETE=passed"
            ),
            "shell_content": content,
        }
    raise ValueError(f"unsupported platform: {platform}")


def verify_server_pair(base: str, bearer: str, shell_id: str) -> tuple[dict, dict]:
    discovered = http_json(base, bearer, "GET", "/mcp-relay/servers?detail=full")
    rows = discovered.get("servers", []) if isinstance(discovered, dict) else []
    file_id = "file:" + shell_id.split(":", 1)[1]
    shell = next((row for row in rows if row.get("server_id") == shell_id), None)
    file_target = next((row for row in rows if row.get("server_id") == file_id), None)
    if not shell or shell.get("status") != "online":
        raise RuntimeError(f"shell target is not online: {shell_id}")
    if not file_target or file_target.get("status") != "online":
        raise RuntimeError(f"paired file target is not online: {file_id}")
    return shell, file_target


def check_actions(base: str, bearer: str, shell: dict, file_target: dict, spec: dict[str, str], marker: str) -> None:
    shell_id = str(shell["server_id"])
    file_id = str(file_target["server_id"])
    shell_tools = await_job(base, bearer, http_json(base, bearer, "POST", "/mcp-relay/tools", {"target": shell_id}))
    file_tools = await_job(base, bearer, http_json(base, bearer, "POST", "/mcp-relay/tools", {"target": file_id}))
    if not contains(shell_tools, "shell_exec") or contains(shell_tools, "file_editor"):
        raise RuntimeError("Actions shell target has wrong tool boundary")
    if not contains(file_tools, "file_editor") or contains(file_tools, "shell_exec"):
        raise RuntimeError("Actions file target has wrong tool boundary")

    uptime = relay_call(base, bearer, shell_id, "shell_exec", {"cmd": spec["uptime_cmd"]})
    if not contains(uptime, spec["uptime_marker"]):
        raise RuntimeError(f"Actions uptime marker missing: {uptime}")

    shell_file = relay_call(base, bearer, shell_id, "shell_exec", {"cmd": spec["shell_roundtrip_cmd"]})
    if not contains(shell_file, spec["shell_content"]) or not contains(shell_file, "GPTADMIN_FILE_DELETE=passed"):
        raise RuntimeError(f"Actions shell file roundtrip failed: {shell_file}")

    editor_path = spec["editor_path"]
    try:
        relay_call(base, bearer, file_id, "file_editor", {"action": "create", "path": editor_path, "content": marker + "-alpha\n"})
        read = relay_call(base, bearer, shell_id, "shell_exec", {"cmd": spec["read_editor_cmd"]})
        if not contains(read, marker + "-alpha"):
            raise RuntimeError(f"Actions file create not visible through shell: {read}")
        relay_call(base, bearer, file_id, "file_editor", {"action": "str_replace", "path": editor_path, "old_text": "alpha", "new_text": "beta"})
        read = relay_call(base, bearer, shell_id, "shell_exec", {"cmd": spec["read_editor_cmd"]})
        if not contains(read, marker + "-beta"):
            raise RuntimeError(f"Actions file edit not visible through shell: {read}")
        relay_call(base, bearer, file_id, "file_editor", {"action": "delete", "path": editor_path})
    finally:
        # Safe cleanup through shell in case file_editor failed before delete.
        if spec is not None:
            if "C:\\" in editor_path:
                cleanup = "powershell.exe -NoProfile -EncodedCommand " + powershell_encoded(f"Remove-Item -Force -ErrorAction SilentlyContinue -LiteralPath '{editor_path}'")
            else:
                cleanup = f"rm -f '{editor_path}'"
            try:
                relay_call(base, bearer, shell_id, "shell_exec", {"cmd": cleanup})
            except Exception:
                pass


def check_native(base: str, bearer: str, shell: dict, file_target: dict, spec: dict[str, str], marker: str) -> None:
    shell_path = endpoint_path(shell, base)
    file_path = endpoint_path(file_target, base)
    init = mcp_rpc(base, bearer, shell_path, 1, "initialize", {
        "protocolVersion": "2024-11-05",
        "capabilities": {},
        "clientInfo": {"name": "gptadmin-platform-acceptance", "version": "1"},
    })
    if not isinstance(init, dict) or not init.get("serverInfo"):
        raise RuntimeError(f"native shell initialize failed: {init}")
    shell_tools = mcp_rpc(base, bearer, shell_path, 2, "tools/list", {})
    if not contains(shell_tools, "shell_exec") or contains(shell_tools, "file_editor"):
        raise RuntimeError("native shell target has wrong tool boundary")

    uptime = mcp_rpc(base, bearer, shell_path, 3, "tools/call", {
        "name": "shell_exec", "arguments": {"cmd": spec["uptime_cmd"]},
    }, async_ok=True)
    if not contains(uptime, spec["uptime_marker"]):
        raise RuntimeError(f"native uptime marker missing: {uptime}")

    shell_file = mcp_rpc(base, bearer, shell_path, 4, "tools/call", {
        "name": "shell_exec", "arguments": {"cmd": spec["shell_roundtrip_cmd"]},
    }, async_ok=True)
    if not contains(shell_file, spec["shell_content"]) or not contains(shell_file, "GPTADMIN_FILE_DELETE=passed"):
        raise RuntimeError(f"native shell file roundtrip failed: {shell_file}")

    file_init = mcp_rpc(base, bearer, file_path, 11, "initialize", {
        "protocolVersion": "2024-11-05",
        "capabilities": {},
        "clientInfo": {"name": "gptadmin-platform-file-acceptance", "version": "1"},
    })
    if not isinstance(file_init, dict) or not file_init.get("serverInfo"):
        raise RuntimeError(f"native file initialize failed: {file_init}")
    file_tools = mcp_rpc(base, bearer, file_path, 12, "tools/list", {})
    if not contains(file_tools, "file_editor") or contains(file_tools, "shell_exec"):
        raise RuntimeError("native file target has wrong tool boundary")

    editor_path = spec["editor_path"]
    try:
        mcp_rpc(base, bearer, file_path, 13, "tools/call", {
            "name": "file_editor", "arguments": {"action": "create", "path": editor_path, "content": marker + "-native-alpha\n"},
        }, async_ok=True)
        read = mcp_rpc(base, bearer, shell_path, 14, "tools/call", {
            "name": "shell_exec", "arguments": {"cmd": spec["read_editor_cmd"]},
        }, async_ok=True)
        if not contains(read, marker + "-native-alpha"):
            raise RuntimeError(f"native file create not visible through shell: {read}")
        mcp_rpc(base, bearer, file_path, 15, "tools/call", {
            "name": "file_editor", "arguments": {"action": "str_replace", "path": editor_path, "old_text": "alpha", "new_text": "beta"},
        }, async_ok=True)
        read = mcp_rpc(base, bearer, shell_path, 16, "tools/call", {
            "name": "shell_exec", "arguments": {"cmd": spec["read_editor_cmd"]},
        }, async_ok=True)
        if not contains(read, marker + "-native-beta"):
            raise RuntimeError(f"native file edit not visible through shell: {read}")
        mcp_rpc(base, bearer, file_path, 17, "tools/call", {
            "name": "file_editor", "arguments": {"action": "delete", "path": editor_path},
        }, async_ok=True)
    finally:
        if "C:\\" in editor_path:
            cleanup = "powershell.exe -NoProfile -EncodedCommand " + powershell_encoded(f"Remove-Item -Force -ErrorAction SilentlyContinue -LiteralPath '{editor_path}'")
        else:
            cleanup = f"rm -f '{editor_path}'"
        try:
            mcp_rpc(base, bearer, shell_path, 18, "tools/call", {"name": "shell_exec", "arguments": {"cmd": cleanup}}, async_ok=True)
        except Exception:
            pass


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--target", required=True, help="Exact shell:<name> target")
    parser.add_argument("--platform", required=True, choices=("windows", "android"))
    parser.add_argument("--base", default=DEFAULT_BASE)
    parser.add_argument("--env-file", type=Path, default=DEFAULT_ENV)
    args = parser.parse_args()

    if not args.target.startswith("shell:"):
        raise SystemExit("--target must be an exact shell:<name> id")
    cli = load_cli()
    env = read_env(args.env_file)
    bearer = cli.make_mcp_bearer_token(env, "platform-runtime-acceptance", ttl_days=1, access_mode="full")
    marker = "gptadmin-" + uuid.uuid4().hex[:12]
    shell, file_target = verify_server_pair(args.base, bearer, args.target)
    spec = platform_spec(args.platform, marker)
    check_actions(args.base, bearer, shell, file_target, spec, marker)
    check_native(args.base, bearer, shell, file_target, spec, marker)
    print(json.dumps({
        "status": "passed",
        "platform": args.platform,
        "target": args.target,
        "actions": {"uptime": "passed", "shell_file_roundtrip": "passed", "file_editor_roundtrip": "passed"},
        "native_mcp": {"initialize": "passed", "uptime": "passed", "shell_file_roundtrip": "passed", "file_editor_roundtrip": "passed"},
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
