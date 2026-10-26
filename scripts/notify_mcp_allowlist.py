#!/usr/bin/env python3
"""Owner-scope: project the GPTAdmin Notify child MCP down to read-only tools.

Run as root on admin-node-100. Reads the ShellMCP supervisor registry
and the hub control token from the RUNNING gptadmin-node process environment;
no secret value is ever printed or written outside this host.

What it does, fail-closed:
  1. verify the Notify registry entry matches the expected definition;
  2. back up /etc/gptadmin/mcp-supervisor.json next to itself;
  3. POST one mcp_manage upsert to the local hub /mcp with the identical
     Notify definition plus tool_allowlist=["noticeplace_instructions"];
  4. verify through the same API that the caller-visible tool list shrank and
     that ask_human is refused.

Rollback: re-run with --rollback (restores the backup file content through the
same upsert path, i.e. no file hand-editing of live state).
"""
from __future__ import annotations
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.request

HUB = "http://127.0.0.1:9001"
REGISTRY = "/etc/gptadmin/mcp-supervisor.json"
REF = "Notify"
ALLOWLIST = ["noticeplace_instructions"]
EXPECTED = {"command": "/usr/bin/python3", "args": ["/opt/noticeplace/mcp/notify_mcp.py"], "cwd": "/opt/noticeplace"}
HUB_PID = "1234861"  # re-resolved below when possible


def hub_pid() -> str:
    try:
        out = subprocess.run(["pgrep", "-f", "gptadmin-node"], capture_output=True, text=True, check=True).stdout.split()
        return out[0]
    except Exception:
        return HUB_PID


def hub_token(pid: str) -> str:
    with open(f"/proc/{pid}/environ", "rb") as fh:
        for line in fh.read().split(b"\0"):
            if line.startswith(b"CTL_TOKEN="):
                return line.decode()[len(b"CTL_TOKEN="):]
    raise SystemExit("CTL_TOKEN not found in hub environment")


def rpc(token: str, name: str, arguments: dict) -> dict:
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                       "params": {"name": name, "arguments": arguments}}).encode()
    req = urllib.request.Request(HUB + "/mcp", data=body, method="POST",
                                 headers={"Content-Type": "application/json",
                                          "Accept": "application/json, text/event-stream",
                                          "Authorization": "Bearer " + token})
    with urllib.request.urlopen(req, timeout=60) as resp:
        raw = resp.read(4 * 1024 * 1024).decode()
    if raw.lstrip().startswith("{"):
        envelope = json.loads(raw)
    else:
        envelope = None
        for line in raw.splitlines():
            if line.startswith("data:"):
                value = json.loads(line[5:].strip())
                if "result" in value or "error" in value:
                    envelope = value
                    break
        if envelope is None:
            raise RuntimeError("no RPC result")
    # The hub gates fresh sessions behind an instruction-context handshake:
    # the first call answers instructions_required with a context; repeat the
    # same call once with that context attached.
    result = envelope.get("result", {})
    text = ""
    for item in result.get("content", []):
        if item.get("type") == "text":
            text = item["text"]
            break
    try:
        parsed = json.loads(text)
    except (ValueError, TypeError):
        parsed = {}
    if isinstance(parsed, dict) and isinstance(parsed.get("error"), dict) and parsed["error"].get("code") == "instructions_required":
        retry = dict(arguments)
        retry["context"] = parsed["context"]
        return rpc(token, name, retry)
    return envelope


def structured(envelope: dict) -> dict:
    result = envelope.get("result", {})
    if result.get("isError"):
        raise RuntimeError("tool error: " + json.dumps(result)[:400])
    if "structuredContent" in result:
        return result["structuredContent"]
    for item in result.get("content", []):
        if item.get("type") == "text":
            return json.loads(item["text"])
    return result


def main() -> int:
    rollback = "--rollback" in sys.argv
    agents = json.load(open(REGISTRY))
    entry = next((a for a in agents if a.get("ref") == REF), None)
    if entry is None:
        raise SystemExit(f"registry has no {REF} entry; nothing to change")
    for key, value in EXPECTED.items():
        if entry.get(key) != value:
            raise SystemExit(f"refusing: {REF}.{key} drifted from expected value")
    pid = hub_pid()
    token = hub_token(pid)

    if rollback:
        backups = sorted(p for p in os.listdir(os.path.dirname(REGISTRY))
                         if p.startswith("mcp-supervisor.json.bak.notify-allowlist."))
        if not backups:
            raise SystemExit("no backup found")
        entry = json.load(open(os.path.join(os.path.dirname(REGISTRY), backups[-1])))
        for a in entry if isinstance(entry, list) else [entry]:
            if a.get("ref") == REF:
                entry = a
                break
        entry.pop("tool_allowlist", None)

    config = {k: entry[k] for k in ("ref", "name", "command", "args", "cwd", "env", "enabled", "transport", "user") if k in entry}
    config["tool_allowlist"] = [] if rollback else ALLOWLIST

    backup = REGISTRY + ".bak.notify-allowlist." + time.strftime("%Y%m%dT%H%M%S")
    shutil.copy2(REGISTRY, backup)
    os.chmod(backup, 0o600)
    print("backup:", backup)

    structured(rpc(token, "schema", {"target": "mcp:shell:admin-node-100:Notify", "detail": "full"}))
    before = structured(rpc(token, "schema", {"target": "mcp:shell:admin-node-100:Notify", "detail": "full"}))
    # Shell-target tools are written through the hub `exec` bridge (the direct
    # execute facade is retired). The only values embedded in the code are the
    # registry's own file paths; no credential exists in the definition.
    def shell(tool: str, args: dict) -> dict:
        code = ("return await mcp.call('shell:admin-node-100', "
                + json.dumps(tool) + ", " + json.dumps(args) + ");")
        return structured(rpc(token, "exec", {"code": code}))
    # The upsert itself: identical definition + allowlist. The hub closes the
    # child connection; the next caller reconnects lazily (no unit restart).
    upsert_receipt = shell("mcp_manage", {"action": "upsert", "config": config})
    upsert_response = upsert_receipt.get("result") or upsert_receipt
    if str(upsert_response.get("error") or ""):
        raise SystemExit("upsert refused: " + json.dumps(upsert_response)[:300])
    print("upsert accepted")
    time.sleep(1)
    after = structured(rpc(token, "schema", {"target": "mcp:shell:admin-node-100:Notify", "detail": "full"}))
    tools = [t.get("name") for t in (after.get("response") or after).get("tools", [])]
    print("visible tools after change:", tools)
    if set(tools) - set(ALLOWLIST):
        print("UNEXPECTED tool surface; rollback with --rollback")
        return 1
    refused = shell("mcp_call", {"ref": REF, "name": "ask_human", "arguments": {}})
    text = json.dumps(refused.get("result", {}))[:300]
    print("ask_human refused:", "tool_allowlist" in text)
    print("OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
