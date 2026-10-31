#!/usr/bin/env python3
"""Native MCP 2026-07-28 canary for one pinned GPTAdmin ShellMCP target.

The bearer is read from stdin, NEVER from argv or a URL. Only a fresh, exact
stdout marker proves execution; non-200, approval, instructions errors and
indeterminate task delivery are failures, never 'successful' output.
"""
from __future__ import annotations

import json
import re
import secrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


PROTOCOL = "2026-07-28"
MAX_RESPONSE = 256 * 1024


class CanaryFailure(Exception):
    pass


def endpoint_for(origin: str, target: str) -> str:
    parts = urllib.parse.urlsplit(origin.strip().rstrip("/"))
    if parts.scheme != "https" and not (parts.scheme == "http" and parts.hostname in {"127.0.0.1", "localhost"}):
        raise CanaryFailure("public origin must use HTTPS")
    if not parts.netloc or parts.path not in ("", "/") or parts.query or parts.fragment:
        raise CanaryFailure("invalid public origin")
    if not re.fullmatch(r"shell:[A-Za-z0-9][A-Za-z0-9.-]{0,119}", target):
        raise CanaryFailure("invalid fixed shell target")
    slug = re.sub(r"[^a-z0-9-]+", "-", target.lower()).strip("-")
    return urllib.parse.urlunsplit((parts.scheme, parts.netloc, "/server/" + slug + "/mcp", "", ""))


def metadata(context: str = "") -> dict:
    meta = {
        "io.modelcontextprotocol/protocolVersion": PROTOCOL,
        "io.modelcontextprotocol/clientInfo": {"name": "gptadmin-live-canary", "version": "1"},
        "io.modelcontextprotocol/clientCapabilities": {
            "extensions": {"io.modelcontextprotocol/tasks": {}},
        },
    }
    if context:
        meta["io.gptadmin/context"] = context
    return meta


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, newurl):
        raise CanaryFailure("native MCP redirected")


def native_rpc(url: str, bearer: str, method: str, params: dict, request_id: int) -> dict:
    headers = {
        "Authorization": "Bearer " + bearer,
        "Content-Type": "application/json",
        "Accept": "application/json",
        "User-Agent": "gptadmin-live-canary-native/1",
        "MCP-Protocol-Version": PROTOCOL,
        "Mcp-Method": method,
    }
    if method == "tools/call":
        headers["Mcp-Name"] = str(params["name"])
    elif method == "tasks/get":
        headers["Mcp-Name"] = str(params["taskId"])
    body = json.dumps({"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}).encode()
    request = urllib.request.Request(url, data=body, headers=headers, method="POST")
    try:
        with urllib.request.build_opener(NoRedirect()).open(request, timeout=12) as response:
            if response.status != 200:
                raise CanaryFailure("native MCP HTTP " + str(response.status))
            raw = response.read(MAX_RESPONSE + 1)
    except urllib.error.HTTPError as exc:
        raise CanaryFailure("native MCP HTTP " + str(exc.code)) from None
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        raise CanaryFailure("native MCP transport " + type(exc).__name__) from None
    if len(raw) > MAX_RESPONSE:
        raise CanaryFailure("oversized native MCP response")
    try:
        rpc = json.loads(raw)
    except (json.JSONDecodeError, UnicodeError):
        raise CanaryFailure("invalid native MCP JSON") from None
    if not isinstance(rpc, dict) or rpc.get("jsonrpc") != "2.0" or rpc.get("id") != request_id:
        raise CanaryFailure("invalid native MCP response identity")
    if rpc.get("error"):
        code = rpc["error"].get("code") if isinstance(rpc["error"], dict) else None
        raise CanaryFailure("native MCP RPC failure " + str(code))
    if not isinstance(rpc.get("result"), dict):
        raise CanaryFailure("native MCP result missing")
    return rpc["result"]


def exact_stdout(payload, *, depth: int = 0) -> str | None:
    """Only a structured shell stdout with a successful return code counts."""
    if depth > 9:
        return None
    if isinstance(payload, dict):
        stdout = payload.get("stdout")
        if isinstance(stdout, str):
            rc = payload.get("returncode", payload.get("exit_code"))
            if rc is None or rc == 0:
                return stdout
            return None
        for key in ("result", "structuredContent", "response", "content", "output"):
            if key in payload:
                found = exact_stdout(payload[key], depth=depth + 1)
                if found is not None:
                    return found
        if isinstance(payload.get("text"), str) and payload["text"].lstrip().startswith("{"):
            try:
                return exact_stdout(json.loads(payload["text"]), depth=depth + 1)
            except (ValueError, TypeError):
                pass
    elif isinstance(payload, list):
        for item in payload[:10]:
            found = exact_stdout(item, depth=depth + 1)
            if found is not None:
                return found
    return None


def unexecuted_context(result: dict) -> str | None:
    body = result.get("structuredContent")
    if not isinstance(body, dict):
        return None
    error = body.get("error")
    if (body.get("status") == "failed" and body.get("executed") is False
            and isinstance(error, dict) and error.get("code") == "instructions_required"
            and isinstance(body.get("instructions"), str) and body["instructions"].strip()
            and isinstance(body.get("context"), str) and body["context"]):
        return body["context"]
    return None


def prove_execution(url: str, bearer: str, rpc=native_rpc) -> str:
    marker = "gptadmin-native-canary-" + secrets.token_hex(10)
    params = {"_meta": metadata()}
    listed = rpc(url, bearer, "tools/list", params, 1)
    tools = listed.get("tools")
    if not isinstance(tools, list) or not any(isinstance(tool, dict) and tool.get("name") == "shell_exec" for tool in tools):
        raise CanaryFailure("shell_exec is not exposed by the fixed MCP target")

    call = {
        "name": "shell_exec",
        "arguments": {"cmd": "printf '" + marker + "'", "timeout": 12},
        "_meta": metadata(),
    }
    outcome = rpc(url, bearer, "tools/call", call, 2)
    context = unexecuted_context(outcome)
    if context:
        # Only an explicit executed=false refusal permits a second submission.
        # A timeout, ambiguous result or failed job MUST NOT be replayed.
        call["_meta"] = metadata(context)
        outcome = rpc(url, bearer, "tools/call", call, 3)

    if outcome.get("isError"):
        raise CanaryFailure("native shell tool returned isError")
    if outcome.get("resultType") == "task":
        task_id = outcome.get("taskId")
        if not isinstance(task_id, str) or not task_id or len(task_id) > 256:
            raise CanaryFailure("native MCP Tasks returned invalid taskId")
        deadline = time.monotonic() + 28
        request_id = 4
        while time.monotonic() < deadline:
            task = rpc(url, bearer, "tasks/get", {"taskId": task_id, "_meta": metadata()}, request_id)
            request_id += 1
            if task.get("resultType") != "complete" or task.get("taskId") != task_id:
                raise CanaryFailure("native MCP task identity mismatch")
            status = task.get("status")
            if status == "completed":
                outcome = task.get("result")
                break
            if status in ("failed", "cancelled", "input_required"):
                raise CanaryFailure("native MCP task " + status)
            if status != "working":
                raise CanaryFailure("native MCP task status unknown")
            delay = task.get("pollIntervalMs", 1000)
            if not isinstance(delay, int) or delay <= 0:
                delay = 1000
            time.sleep(min(max(delay / 1000, 0.25), 2.0))
        else:
            raise CanaryFailure("native MCP task timed out; do not resubmit")
    elif outcome.get("resultType") != "complete":
        raise CanaryFailure("native MCP tool returned no completed result or task")

    if exact_stdout(outcome) != marker:
        raise CanaryFailure("native MCP shell output did not match the fresh marker")
    return marker


def main(argv: list[str]) -> int:
    if len(argv) != 3:
        print("FAIL native MCP canary: expected origin and target", file=sys.stderr)
        return 2
    bearer_bytes = sys.stdin.buffer.read(16385)
    if len(bearer_bytes) > 16384:
        print("FAIL native MCP canary: credential length invalid", file=sys.stderr)
        return 2
    bearer = bearer_bytes.decode("utf-8", errors="replace").strip()
    if not bearer or any(c.isspace() for c in bearer):
        print("FAIL native MCP canary: credential missing", file=sys.stderr)
        return 2
    try:
        url = endpoint_for(argv[1], argv[2])
        print(prove_execution(url, bearer))
        return 0
    except CanaryFailure as exc:
        print("FAIL native MCP canary: " + str(exc)[:160], file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
