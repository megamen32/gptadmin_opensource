#!/usr/bin/env python3
"""
Generic stdio MCP relay agent.

Runs any local stdio MCP server from an mcpServers JSON config and exposes it to
GPTAdmin hub_proxy through /mcp-relay long polling.

Example:
  python3 generic_stdio_mcp_relay.py --config page-agent.mcp.json --server page-agent
"""

from __future__ import annotations

import argparse
import json
import os
import signal
import subprocess
import sys
import threading
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from queue import Queue, Empty
from typing import Any, Dict, Optional

DEFAULT_HUB = "https://gptadminmcp.bezrabotnyi.com"
STOP = False


def _stop(*_: Any) -> None:
    global STOP
    STOP = True


signal.signal(signal.SIGINT, _stop)
signal.signal(signal.SIGTERM, _stop)


def http_json(method: str, url: str, token: str, data: Optional[Dict[str, Any]] = None, timeout: int = 70) -> Dict[str, Any]:
    body = None if data is None else json.dumps(data, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(url, data=body, method=method.upper())
    req.add_header("Authorization", f"Bearer {token}")
    if body is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8", "replace")
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        raw = e.read().decode("utf-8", "replace")
        raise RuntimeError(f"HTTP {e.code} {url}: {raw}") from e


class McpStdioClient:
    def __init__(self, command: str, args: list[str], env: Optional[Dict[str, str]] = None, cwd: Optional[str] = None, init_timeout: int = 180):
        full_env = os.environ.copy()
        if env:
            full_env.update({k: str(v) for k, v in env.items()})
        self.command_line = [command, *args]
        self.proc = subprocess.Popen(
            self.command_line,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            cwd=cwd,
            env=full_env,
            bufsize=0,
        )
        self._id = 0
        self._lock = threading.Lock()
        self._responses: Dict[int, Dict[str, Any]] = {}
        self._events: Queue[Dict[str, Any]] = Queue()
        self._stderr_q: Queue[str] = Queue()
        self._reader = threading.Thread(target=self._read_stdout_loop, daemon=True)
        self._stderr_reader = threading.Thread(target=self._read_stderr_loop, daemon=True)
        self._reader.start()
        self._stderr_reader.start()
        self._initialize(init_timeout=init_timeout)

    def close(self) -> None:
        try:
            if self.proc.poll() is None:
                self.proc.terminate()
                try:
                    self.proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.proc.kill()
        except Exception:
            pass

    def _read_stderr_loop(self) -> None:
        assert self.proc.stderr is not None
        for line in iter(self.proc.stderr.readline, b""):
            if not line:
                break
            self._stderr_q.put(line.decode("utf-8", "replace").rstrip())

    def _readline(self) -> bytes:
        assert self.proc.stdout is not None
        line = self.proc.stdout.readline()
        if line == b"":
            raise EOFError("MCP stdio server stdout closed")
        return line

    def _read_message(self) -> Dict[str, Any]:
        headers: Dict[str, str] = {}
        # MCP stdio framing: Content-Length headers, blank line, JSON body.
        while True:
            line = self._readline()
            stripped = line.strip()
            if not stripped:
                break
            if b":" in stripped:
                k, v = stripped.split(b":", 1)
                headers[k.decode("ascii", "ignore").lower()] = v.decode("ascii", "ignore").strip()
        length = int(headers.get("content-length", "0"))
        if length <= 0:
            raise ValueError(f"invalid MCP frame headers: {headers}")
        assert self.proc.stdout is not None
        body = self.proc.stdout.read(length)
        if len(body) != length:
            raise EOFError("short MCP frame body")
        return json.loads(body.decode("utf-8"))

    def _read_stdout_loop(self) -> None:
        while True:
            try:
                msg = self._read_message()
                mid = msg.get("id")
                if isinstance(mid, int):
                    self._responses[mid] = msg
                else:
                    self._events.put(msg)
            except Exception as e:
                self._events.put({"error": str(e), "traceback": traceback.format_exc()})
                break

    def _write_message(self, msg: Dict[str, Any]) -> None:
        raw = json.dumps(msg, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        frame = f"Content-Length: {len(raw)}\r\n\r\n".encode("ascii") + raw
        assert self.proc.stdin is not None
        self.proc.stdin.write(frame)
        self.proc.stdin.flush()

    def request(self, method: str, params: Optional[Dict[str, Any]] = None, timeout: int = 60) -> Dict[str, Any]:
        if self.proc.poll() is not None:
            stderr_tail = self.stderr_tail()
            raise RuntimeError(f"MCP stdio server exited with code {self.proc.returncode}. stderr={stderr_tail}")
        with self._lock:
            self._id += 1
            mid = self._id
            self._write_message({"jsonrpc": "2.0", "id": mid, "method": method, "params": params or {}})
        deadline = time.time() + timeout
        while time.time() < deadline:
            if mid in self._responses:
                msg = self._responses.pop(mid)
                if "error" in msg:
                    raise RuntimeError(json.dumps(msg["error"], ensure_ascii=False))
                return msg.get("result") or {}
            time.sleep(0.02)
        stderr_tail = self.stderr_tail()
        raise TimeoutError(f"MCP request timeout: {method}; command={self.command_line!r}; stderr_tail={stderr_tail}")

    def notify(self, method: str, params: Optional[Dict[str, Any]] = None) -> None:
        with self._lock:
            self._write_message({"jsonrpc": "2.0", "method": method, "params": params or {}})

    def stderr_tail(self, limit: int = 20) -> list[str]:
        lines: list[str] = []
        while True:
            try:
                lines.append(self._stderr_q.get_nowait())
            except Empty:
                break
        return lines[-limit:]

    def _initialize(self, init_timeout: int = 180) -> None:
        self.request("initialize", {
            "protocolVersion": "2025-06-18",
            "capabilities": {"roots": {"listChanged": False}, "sampling": {}},
            "clientInfo": {"name": "gptadmin-generic-stdio-relay", "version": "0.1.0"},
        }, timeout=init_timeout)
        self.notify("notifications/initialized", {})

    def tools_list(self) -> Dict[str, Any]:
        return self.request("tools/list", {}, timeout=60)

    def tools_call(self, name: str, arguments: Dict[str, Any], timeout: int = 120) -> Dict[str, Any]:
        return self.request("tools/call", {"name": name, "arguments": arguments or {}}, timeout=timeout)


class Relay:
    def __init__(self, hub: str, token: str, agent_id: str, name: str, client: McpStdioClient, server_spec: Dict[str, Any]):
        self.hub = hub.rstrip("/")
        self.token = token
        self.agent_id = agent_id
        self.name = name
        self.client = client
        self.server_spec = server_spec

    def register(self) -> None:
        payload = {
            "agent_id": self.agent_id,
            "name": self.name,
            "transport": "stdio",
            "capabilities": ["tools/list", "tools/call", "generic-stdio-mcp"],
            "meta": {
                "command": self.server_spec.get("command"),
                "args": self.server_spec.get("args", []),
            },
        }
        print(json.dumps(http_json("POST", f"{self.hub}/mcp-relay/register", self.token, payload), ensure_ascii=False), flush=True)

    def run(self) -> None:
        self.register()
        while not STOP:
            try:
                job = http_json("GET", f"{self.hub}/mcp-relay/poll/{urllib.parse.quote(self.agent_id)}?timeout=55", self.token, timeout=70)
                if not job:
                    continue
                job_id = job.get("id")
                try:
                    method = job.get("method")
                    params = job.get("params") or {}
                    if method == "tools/list":
                        result = self.client.tools_list()
                    elif method == "tools/call":
                        result = self.client.tools_call(params.get("name"), params.get("arguments") or {}, timeout=180)
                    else:
                        raise ValueError(f"unsupported relay MCP method: {method}")
                    payload = {"id": job_id, "ok": True, "result": result}
                except Exception as e:
                    payload = {"id": job_id, "ok": False, "error": {"message": str(e), "traceback": traceback.format_exc()[-4000:]}}
                http_json("POST", f"{self.hub}/mcp-relay/result/{urllib.parse.quote(self.agent_id)}", self.token, payload, timeout=30)
            except Exception as e:
                print(f"relay loop error: {e}", file=sys.stderr, flush=True)
                time.sleep(3)


def load_server(config_path: Path, server_name: str) -> Dict[str, Any]:
    cfg = json.loads(config_path.read_text())
    servers = cfg.get("mcpServers") or {}
    if server_name not in servers:
        raise SystemExit(f"server {server_name!r} not found in {config_path}; available={list(servers)}")
    spec = servers[server_name]
    if not spec.get("command"):
        raise SystemExit(f"server {server_name!r} has no command")
    return spec


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True, help="JSON file with mcpServers")
    parser.add_argument("--server", required=True, help="mcpServers key to launch")
    parser.add_argument("--hub", default=os.getenv("GPTADMIN_MCP_RELAY_HUB", DEFAULT_HUB))
    parser.add_argument("--token", default=os.getenv("GPTADMIN_MCP_RELAY_TOKEN"))
    parser.add_argument("--agent-id", default=os.getenv("GPTADMIN_MCP_RELAY_AGENT_ID"))
    parser.add_argument("--name", default=os.getenv("GPTADMIN_MCP_RELAY_NAME"))
    parser.add_argument("--init-timeout", type=int, default=int(os.getenv("GPTADMIN_MCP_INIT_TIMEOUT", "180")))
    parser.add_argument("--print-command", action="store_true", help="Print the resolved stdio MCP command before launch")
    args = parser.parse_args()

    if not args.token:
        raise SystemExit("set GPTADMIN_MCP_RELAY_TOKEN or pass --token")

    spec = load_server(Path(args.config), args.server)
    agent_id = args.agent_id or f"{os.uname().nodename}-{args.server}"
    name = args.name or f"{args.server} via {os.uname().nodename}"

    if args.print_command:
        safe_env = dict(spec.get("env") or {})
        for k in list(safe_env):
            if "KEY" in k.upper() or "TOKEN" in k.upper() or "SECRET" in k.upper():
                safe_env[k] = "***masked***"
        print(json.dumps({"command": spec["command"], "args": list(spec.get("args") or []), "env": safe_env, "cwd": spec.get("cwd")}, ensure_ascii=False), flush=True)

    client = McpStdioClient(
        command=spec["command"],
        args=list(spec.get("args") or []),
        env=spec.get("env") or {},
        cwd=spec.get("cwd"),
        init_timeout=args.init_timeout,
    )
    try:
        Relay(args.hub, args.token, agent_id, name, client, spec).run()
    finally:
        client.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
