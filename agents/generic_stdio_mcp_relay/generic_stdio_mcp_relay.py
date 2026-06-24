#!/usr/bin/env python3
"""
Generic stdio MCP relay agent.

Runs any local stdio MCP server from an mcpServers JSON config and exposes it to
GPTAdmin hub_proxy through /mcp-relay long polling.

Supported stdio wire formats:
  - ndjson: one JSON-RPC message per line. This is what @page-agent/mcp and
    @playwright/mcp currently use in the setups tested here.
  - framed: Content-Length framed messages.
  - auto: command-name heuristic, with config/CLI override support.

Useful debug flags:
  --verbose     high-level relay/MCP lifecycle logs
  --trace-json  log full JSON-RPC payloads; use carefully, arguments may contain sensitive data

Examples:
  # Compact/manual mode: run command directly. Everything after relay options is the MCP command.
  python3 generic_stdio_mcp_relay.py npx -y @playwright/mcp@latest --extension
  python3 generic_stdio_mcp_relay.py --stdio-format framed npx -y mcp-remote https://example.com/mcp

  # Service/managed mode: use one GPTAdmin agent JSON config.
  python3 generic_stdio_mcp_relay.py --agent-config /etc/gptadmin/mcp-agents.d/gptadminmcp.json

  # Claude-style mcpServers JSON compatibility mode.
  python3 generic_stdio_mcp_relay.py --config page-agent.mcp.json --server page-agent
"""

from __future__ import annotations

import argparse
import json
import os
import re
import signal
import subprocess
import sys
import threading
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
from collections import deque
from pathlib import Path
from queue import Empty, Queue
from typing import Any, Deque, Dict, Iterable, Literal, Optional

DEFAULT_HUB = "https://gptadminmcp.bezrabotnyi.com"
STOP = False

StdioFormat = Literal["auto", "framed", "ndjson"]
EffectiveStdioFormat = Literal["framed", "ndjson"]


def now_ts() -> str:
    return time.strftime("%Y-%m-%d %H:%M:%S")


def log(msg: str, *, enabled: bool = True) -> None:
    if enabled:
        print(f"[{now_ts()}] {msg}", file=sys.stderr, flush=True)


def short_json(value: Any, limit: int = 1200) -> str:
    try:
        s = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    except Exception:
        s = repr(value)
    if len(s) > limit:
        return s[:limit] + f"...<truncated {len(s) - limit} chars>"
    return s


def summarize_mcp_message(msg: Dict[str, Any]) -> str:
    if "method" in msg and "id" in msg:
        return f"request id={msg.get('id')} method={msg.get('method')}"
    if "method" in msg:
        return f"notification method={msg.get('method')}"
    if "id" in msg and "error" in msg:
        return f"response id={msg.get('id')} error={short_json(msg.get('error'), 300)}"
    if "id" in msg:
        result = msg.get("result")
        if isinstance(result, dict):
            return f"response id={msg.get('id')} result_keys={list(result.keys())}"
        return f"response id={msg.get('id')} result_type={type(result).__name__}"
    return f"event keys={list(msg.keys())}"


def jsonrpc_error(code: int, message: str) -> Dict[str, Any]:
    return {"code": code, "message": message}


def _stop(*_: Any) -> None:
    global STOP
    STOP = True


signal.signal(signal.SIGINT, _stop)
signal.signal(signal.SIGTERM, _stop)


def http_json(
    method: str,
    url: str,
    token: str,
    data: Optional[Dict[str, Any]] = None,
    timeout: int = 70,
) -> Dict[str, Any]:
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


def normalize_stdio_format(value: Optional[Any], source: str) -> Optional[StdioFormat]:
    if value is None or value == "":
        return None
    fmt = str(value).strip().lower()
    aliases = {
        "jsonl": "ndjson",
        "json-lines": "ndjson",
        "json_lines": "ndjson",
        "line": "ndjson",
        "lines": "ndjson",
        "content-length": "framed",
        "content_length": "framed",
        "mcp": "framed",
    }
    fmt = aliases.get(fmt, fmt)
    if fmt not in {"auto", "framed", "ndjson"}:
        raise ValueError(f"unsupported stdio_format from {source}: {value!r}; expected auto, framed, or ndjson")
    return fmt  # type: ignore[return-value]


def resolve_stdio_format(command_line: Iterable[str], requested: StdioFormat) -> EffectiveStdioFormat:
    if requested in {"framed", "ndjson"}:
        return requested

    joined = " ".join(command_line).lower()
    if any(marker in joined for marker in (
        "@page-agent/mcp",
        "page-agent",
        "@playwright/mcp",
        "playwright-mcp",
        "playwright",
    )):
        return "ndjson"
    return "framed"


def tail_queue(q: Queue[str], limit: int = 20) -> list[str]:
    lines: list[str] = []
    while True:
        try:
            lines.append(q.get_nowait())
        except Empty:
            break
    return lines[-limit:]


class McpStdioClient:
    def __init__(
        self,
        command: str,
        args: list[str],
        env: Optional[Dict[str, str]] = None,
        cwd: Optional[str] = None,
        init_timeout: int = 180,
        requested_stdio_format: StdioFormat = "auto",
        verbose: bool = False,
        trace_json: bool = False,
    ):
        self.verbose = verbose
        self.trace_json = trace_json

        full_env = os.environ.copy()
        if env:
            full_env.update({k: str(v) for k, v in env.items()})

        self.command_line = [command, *args]
        self.requested_stdio_format = requested_stdio_format
        self.stdio_format = resolve_stdio_format(self.command_line, requested_stdio_format)

        log(
            f"mcp spawn command={self.command_line!r} cwd={cwd!r} requested_stdio_format={requested_stdio_format} effective_stdio_format={self.stdio_format}",
            enabled=self.verbose,
        )
        self.proc = subprocess.Popen(
            self.command_line,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            cwd=cwd,
            env=full_env,
            bufsize=0,
        )
        log(f"mcp spawned pid={self.proc.pid}", enabled=self.verbose)

        self._id = 0
        self._write_lock = threading.Lock()
        self._responses_lock = threading.Lock()
        self._responses: Dict[int, Dict[str, Any]] = {}
        self._events: Queue[Dict[str, Any]] = Queue()
        self._stderr_q: Queue[str] = Queue()
        self._stdout_noise: Deque[str] = deque(maxlen=30)

        self._reader = threading.Thread(target=self._read_stdout_loop, daemon=True)
        self._stderr_reader = threading.Thread(target=self._read_stderr_loop, daemon=True)
        self._reader.start()
        self._stderr_reader.start()

        self._initialize(init_timeout=init_timeout)

    def close(self) -> None:
        try:
            if self.proc.poll() is None:
                log(f"mcp terminate pid={self.proc.pid}", enabled=self.verbose)
                self.proc.terminate()
                try:
                    self.proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    log(f"mcp kill pid={self.proc.pid}", enabled=self.verbose)
                    self.proc.kill()
        except Exception as e:
            log(f"mcp close error: {e}", enabled=self.verbose)

    def _read_stderr_loop(self) -> None:
        assert self.proc.stderr is not None
        for line in iter(self.proc.stderr.readline, b""):
            if not line:
                break
            text = line.decode("utf-8", "replace").rstrip()
            self._stderr_q.put(text)
            log(f"mcp stderr: {text}", enabled=self.verbose)

    def _readline(self) -> bytes:
        assert self.proc.stdout is not None
        line = self.proc.stdout.readline()
        if line == b"":
            raise EOFError("MCP stdio server stdout closed")
        return line

    def _read_exact(self, length: int) -> bytes:
        assert self.proc.stdout is not None
        chunks: list[bytes] = []
        remaining = length
        while remaining > 0:
            chunk = self.proc.stdout.read(remaining)
            if not chunk:
                raise EOFError(f"short MCP frame body; missing={remaining}")
            chunks.append(chunk)
            remaining -= len(chunk)
        return b"".join(chunks)

    def _read_message(self) -> Dict[str, Any]:
        while True:
            first = self._readline()
            stripped_first = first.strip()
            if not stripped_first:
                continue
            if stripped_first.startswith(b"{"):
                return json.loads(stripped_first.decode("utf-8"))
            if b":" in stripped_first:
                headers: Dict[str, str] = {}
                line = first
                while True:
                    stripped = line.strip()
                    if not stripped:
                        break
                    if b":" in stripped:
                        k, v = stripped.split(b":", 1)
                        headers[k.decode("ascii", "ignore").lower()] = v.decode("ascii", "ignore").strip()
                    line = self._readline()
                length = int(headers.get("content-length", "0"))
                if length <= 0:
                    raise ValueError(f"invalid MCP frame headers: {headers}; first_line={stripped_first[:160]!r}")
                body = self._read_exact(length)
                return json.loads(body.decode("utf-8"))
            noise = stripped_first.decode("utf-8", "replace")
            self._stdout_noise.append(noise)
            raise ValueError(f"unexpected non-MCP stdout line: {noise[:200]!r}")

    def _read_stdout_loop(self) -> None:
        while True:
            try:
                msg = self._read_message()
                log(f"mcp recv {summarize_mcp_message(msg)}", enabled=self.verbose)
                if self.trace_json:
                    log(f"mcp recv json={short_json(msg, 5000)}", enabled=True)

                mid = msg.get("id")
                has_method = "method" in msg
                if has_method and mid is not None:
                    self._handle_server_request(msg)
                    continue
                if has_method:
                    self._events.put(msg)
                    continue
                if isinstance(mid, int):
                    with self._responses_lock:
                        self._responses[mid] = msg
                else:
                    self._events.put(msg)
            except Exception as e:
                event = {"error": str(e), "traceback": traceback.format_exc()}
                log(f"mcp stdout reader failed: {e}", enabled=True)
                self._events.put(event)
                break

    def _handle_server_request(self, msg: Dict[str, Any]) -> None:
        mid = msg.get("id")
        method = msg.get("method")
        try:
            if method == "roots/list":
                response = {"jsonrpc": "2.0", "id": mid, "result": {"roots": []}}
            elif method == "sampling/createMessage":
                response = {
                    "jsonrpc": "2.0",
                    "id": mid,
                    "error": jsonrpc_error(-32601, "sampling/createMessage is not supported by this relay"),
                }
            else:
                response = {
                    "jsonrpc": "2.0",
                    "id": mid,
                    "error": jsonrpc_error(-32601, f"client method not supported: {method}"),
                }
            log(f"mcp reply to server request id={mid} method={method}", enabled=self.verbose)
            self._write_message(response)
        except Exception as e:
            log(f"mcp failed replying to server request id={mid} method={method}: {e}", enabled=True)
            raise

    def _write_message(self, msg: Dict[str, Any]) -> None:
        raw = json.dumps(msg, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        if self.stdio_format == "ndjson":
            frame = raw + b"\n"
        else:
            frame = f"Content-Length: {len(raw)}\r\n\r\n".encode("ascii") + raw
        log(
            f"mcp send {summarize_mcp_message(msg)} bytes={len(frame)} format={self.stdio_format}",
            enabled=self.verbose,
        )
        if self.trace_json:
            log(f"mcp send json={short_json(msg, 5000)}", enabled=True)
        assert self.proc.stdin is not None
        self.proc.stdin.write(frame)
        self.proc.stdin.flush()

    def _pop_response(self, mid: int) -> Optional[Dict[str, Any]]:
        with self._responses_lock:
            return self._responses.pop(mid, None)

    def request(self, method: str, params: Optional[Dict[str, Any]] = None, timeout: int = 60) -> Dict[str, Any]:
        if self.proc.poll() is not None:
            raise RuntimeError(
                f"MCP stdio server exited with code {self.proc.returncode}. "
                f"stderr_tail={self.stderr_tail()} stdout_noise={self.stdout_noise_tail()}"
            )

        started = time.time()
        with self._write_lock:
            self._id += 1
            mid = self._id
            log(f"mcp request start id={mid} method={method} timeout={timeout}s", enabled=self.verbose)
            self._write_message({"jsonrpc": "2.0", "id": mid, "method": method, "params": params or {}})

        deadline = time.time() + timeout
        last_wait_log = started
        while time.time() < deadline:
            if self.proc.poll() is not None:
                raise RuntimeError(
                    f"MCP stdio server exited with code {self.proc.returncode} while waiting for {method}. "
                    f"stderr_tail={self.stderr_tail()} stdout_noise={self.stdout_noise_tail()}"
                )
            msg = self._pop_response(mid)
            if msg is not None:
                elapsed = time.time() - started
                log(f"mcp request done id={mid} method={method} elapsed={elapsed:.3f}s", enabled=self.verbose)
                if "error" in msg:
                    raise RuntimeError(json.dumps(msg["error"], ensure_ascii=False))
                result = msg.get("result")
                return result if isinstance(result, dict) else {"value": result}
            try:
                event = self._events.get_nowait()
            except Empty:
                event = None
            if isinstance(event, dict) and "error" in event:
                raise RuntimeError(
                    f"MCP stdout reader failed while waiting for {method}: {event.get('error')}; "
                    f"stderr_tail={self.stderr_tail()} stdout_noise={self.stdout_noise_tail()}"
                )
            if self.verbose and time.time() - last_wait_log >= 10:
                last_wait_log = time.time()
                log(
                    f"mcp request waiting id={mid} method={method} elapsed={time.time() - started:.1f}s stderr_tail={self.stderr_tail(5)}",
                    enabled=True,
                )
            time.sleep(0.02)

        raise TimeoutError(
            f"MCP request timeout: {method}; "
            f"command={self.command_line!r}; "
            f"requested_stdio_format={self.requested_stdio_format!r}; "
            f"effective_stdio_format={self.stdio_format!r}; "
            f"stderr_tail={self.stderr_tail()}; "
            f"stdout_noise={self.stdout_noise_tail()}"
        )

    def notify(self, method: str, params: Optional[Dict[str, Any]] = None) -> None:
        with self._write_lock:
            self._write_message({"jsonrpc": "2.0", "method": method, "params": params or {}})

    def stderr_tail(self, limit: int = 20) -> list[str]:
        return tail_queue(self._stderr_q, limit=limit)

    def stdout_noise_tail(self) -> list[str]:
        return list(self._stdout_noise)

    def _initialize(self, init_timeout: int = 180) -> None:
        log("mcp initialize begin", enabled=self.verbose)
        self.request(
            "initialize",
            {
                "protocolVersion": "2025-03-26",
                "capabilities": {"roots": {"listChanged": False}, "sampling": {}},
                "clientInfo": {"name": "gptadmin-generic-stdio-relay", "version": "0.3.0"},
            },
            timeout=init_timeout,
        )
        self.notify("notifications/initialized", {})
        log("mcp initialize complete", enabled=self.verbose)

    def tools_list(self) -> Dict[str, Any]:
        return self.request("tools/list", {}, timeout=60)

    def tools_call(self, name: str, arguments: Dict[str, Any], timeout: int = 120) -> Dict[str, Any]:
        if not name:
            raise ValueError("tools/call requires params.name")
        return self.request("tools/call", {"name": name, "arguments": arguments or {}}, timeout=timeout)

    def mcp_request(self, method: str, params: Dict[str, Any], timeout: int = 120) -> Dict[str, Any]:
        if not method:
            raise ValueError("MCP method is required")
        if not isinstance(params, dict):
            params = {}
        return self.request(method, params, timeout=timeout)


class Relay:
    def __init__(
        self,
        hub: str,
        token: str,
        agent_id: str,
        name: str,
        client: McpStdioClient,
        server_spec: Dict[str, Any],
        verbose: bool = False,
    ):
        self.hub = hub.rstrip("/")
        self.token = token
        self.agent_id = agent_id
        self.name = name
        self.client = client
        self.server_spec = server_spec
        self.verbose = verbose

    def register(self) -> None:
        payload = {
            "agent_id": self.agent_id,
            "name": self.name,
            "transport": "stdio",
            "capabilities": ["tools/list", "tools/call", "resources/list", "resources/read", "prompts/list", "prompts/get", "generic-stdio-mcp", "generic-mcp-request"],
            "meta": {
                "command": self.server_spec.get("command"),
                "args": self.server_spec.get("args", []),
                "requested_stdio_format": self.client.requested_stdio_format,
                "effective_stdio_format": self.client.stdio_format,
            },
        }
        log(f"relay register agent_id={self.agent_id} hub={self.hub}", enabled=self.verbose)
        result = http_json("POST", f"{self.hub}/mcp-relay/register", self.token, payload)
        print(json.dumps(result, ensure_ascii=False), flush=True)
        log(f"relay register ok result={short_json(result, 800)}", enabled=self.verbose)

    def run(self) -> None:
        self.register()
        log(f"relay poll loop start agent_id={self.agent_id}", enabled=self.verbose)
        while not STOP:
            try:
                job = http_json(
                    "GET",
                    f"{self.hub}/mcp-relay/poll/{urllib.parse.quote(self.agent_id)}?timeout=55",
                    self.token,
                    timeout=70,
                )
                if not job:
                    log("relay poll timeout/no job", enabled=self.verbose)
                    continue
                job_id = job.get("id")
                method = job.get("method")
                params = job.get("params") or {}
                log(f"relay job start id={job_id} method={method} params={short_json(params, 1000)}", enabled=self.verbose)
                started = time.time()
                try:
                    if method == "tools/list":
                        result = self.client.tools_list()
                    elif method == "tools/call":
                        result = self.client.tools_call(params.get("name"), params.get("arguments") or {}, timeout=180)
                    elif isinstance(method, str) and method:
                        # Forward other MCP request methods, e.g. resources/list,
                        # resources/read, prompts/list, prompts/get. Unsupported
                        # methods are reported by the underlying MCP server and
                        # posted back to the hub instead of leaving jobs stuck.
                        result = self.client.mcp_request(method, params, timeout=180)
                    else:
                        raise ValueError(f"unsupported relay MCP method: {method}")
                    elapsed = time.time() - started
                    log(f"relay job ok id={job_id} method={method} elapsed={elapsed:.3f}s result_keys={list(result.keys()) if isinstance(result, dict) else type(result).__name__}", enabled=self.verbose)
                    payload = {"id": job_id, "ok": True, "result": result}
                except Exception as e:
                    elapsed = time.time() - started
                    log(f"relay job error id={job_id} method={method} elapsed={elapsed:.3f}s error={e}", enabled=True)
                    payload = {
                        "id": job_id,
                        "ok": False,
                        "error": {"message": str(e), "traceback": traceback.format_exc()[-4000:]},
                    }
                http_json("POST", f"{self.hub}/mcp-relay/result/{urllib.parse.quote(self.agent_id)}", self.token, payload, timeout=30)
                log(f"relay job result posted id={job_id} ok={payload.get('ok')}", enabled=self.verbose)
            except Exception as e:
                log(f"relay loop error: {e}", enabled=True)
                time.sleep(3)
        log("relay stop requested", enabled=self.verbose)


def load_server(config_path: Path, server_name: str) -> Dict[str, Any]:
    cfg = json.loads(config_path.read_text(encoding="utf-8"))
    servers = cfg.get("mcpServers") or {}
    if server_name not in servers:
        raise SystemExit(f"server {server_name!r} not found in {config_path}; available={list(servers)}")
    spec = servers[server_name]
    if not isinstance(spec, dict):
        raise SystemExit(f"server {server_name!r} must be an object")
    if not spec.get("command"):
        raise SystemExit(f"server {server_name!r} has no command")
    return spec


def load_agent_config(config_path: Path) -> Dict[str, Any]:
    cfg = json.loads(config_path.read_text(encoding="utf-8"))
    if not isinstance(cfg, dict):
        raise SystemExit(f"agent config must be an object: {config_path}")
    if not cfg.get("command"):
        raise SystemExit(f"agent config has no command: {config_path}")
    args = cfg.get("args", [])
    if args is None:
        cfg["args"] = []
    elif not isinstance(args, list):
        raise SystemExit(f"agent config args must be a list: {config_path}")
    env = cfg.get("env", {})
    if env is None:
        cfg["env"] = {}
    elif not isinstance(env, dict):
        raise SystemExit(f"agent config env must be an object: {config_path}")
    return cfg


def slugify(value: str, default: str = "mcp") -> str:
    value = re.sub(r"^@", "", value.strip().lower())
    value = value.replace("@", "-").replace("/", "-")
    value = re.sub(r"[^a-z0-9_.-]+", "-", value).strip("-._")
    return value or default


def infer_short_server_name(command: str, args: list[str]) -> str:
    all_parts = [command, *args]
    joined = " ".join(all_parts).lower()
    if "@playwright/mcp" in joined:
        return "playwright-browser"
    if "@page-agent/mcp" in joined or "page-agent" in joined:
        return "page-agent"
    if "@peakmojo/applescript-mcp" in joined or "applescript-mcp" in joined:
        return "macos-applescript"
    if "macos-automator-mcp" in joined:
        return "macos-automator"

    # For npx/uvx/npmx wrappers, use the first package-ish argument as the server name.
    for part in args:
        if part in {"-y", "--yes", "--package", "-p"}:
            continue
        if part.startswith("-"):
            continue
        return slugify(part)
    return slugify(Path(command).name)


def resolve_server_spec(args: argparse.Namespace) -> tuple[Dict[str, Any], str]:
    has_agent_config = bool(args.agent_config)
    has_mcpservers_config = bool(args.config or args.server)
    has_compact_cmd = bool(args.cmd)
    modes = sum(1 for v in (has_agent_config, has_mcpservers_config, has_compact_cmd) if v)
    if modes != 1:
        raise SystemExit("use exactly one mode: --agent-config FILE, --config/--server, or compact COMMAND [ARGS...]")

    if has_agent_config:
        spec = load_agent_config(Path(args.agent_config))
        command = str(spec["command"])
        command_args = [str(x) for x in list(spec.get("args") or [])]
        server_name = str(
            spec.get("server")
            or spec.get("server_name")
            or spec.get("agent_id")
            or infer_short_server_name(command, command_args)
        )
        return spec, server_name

    if has_mcpservers_config:
        if not args.config or not args.server:
            raise SystemExit("config compatibility mode requires both --config and --server")
        return load_server(Path(args.config), args.server), args.server

    command = str(args.cmd[0])
    command_args = [str(x) for x in args.cmd[1:]]
    server_name = infer_short_server_name(command, command_args)
    return {"command": command, "args": command_args, "env": {}}, server_name


def resolve_requested_stdio_format(args: argparse.Namespace, spec: Dict[str, Any]) -> StdioFormat:
    cli_fmt = normalize_stdio_format(args.stdio_format, "--stdio-format")
    cfg_fmt = normalize_stdio_format(spec.get("stdio_format"), "config stdio_format")
    env_fmt = normalize_stdio_format(os.getenv("GPTADMIN_MCP_STDIO_FORMAT"), "GPTADMIN_MCP_STDIO_FORMAT")
    return cli_fmt or cfg_fmt or env_fmt or "auto"


def masked_env(env: Dict[str, Any]) -> Dict[str, Any]:
    safe = dict(env or {})
    for key in list(safe):
        upper = key.upper()
        if any(marker in upper for marker in ("KEY", "TOKEN", "SECRET", "PASSWORD")):
            safe[key] = "***masked***"
    return safe


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Relay a local stdio MCP server to GPTAdmin. Use --agent-config for services and compact command mode for manual starts."
    )
    parser.add_argument("--agent-config", help="GPTAdmin MCP agent JSON config. Preferred for services/managed installs.")
    parser.add_argument("--config", help="Claude-style JSON file with mcpServers. Requires --server.")
    parser.add_argument("--server", help="mcpServers key to launch. Requires --config.")
    parser.add_argument("--hub", default=os.getenv("GPTADMIN_MCP_RELAY_HUB", DEFAULT_HUB))
    parser.add_argument("--token", default=os.getenv("GPTADMIN_MCP_RELAY_TOKEN"))
    parser.add_argument("--agent-id", default=os.getenv("GPTADMIN_MCP_RELAY_AGENT_ID"))
    parser.add_argument("--name", default=os.getenv("GPTADMIN_MCP_RELAY_NAME"))
    parser.add_argument("--init-timeout", type=int, default=int(os.getenv("GPTADMIN_MCP_INIT_TIMEOUT", "180")))
    parser.add_argument("--print-command", action="store_true", help="Print the resolved stdio MCP command before launch")
    parser.add_argument("--verbose", action="store_true", help="Print high-level relay/MCP debug logs to stderr")
    parser.add_argument("--trace-json", action="store_true", help="Print full JSON-RPC payloads to stderr. May expose sensitive tool arguments.")
    parser.add_argument(
        "--stdio-format",
        choices=["auto", "framed", "ndjson", "jsonl", "content-length"],
        default=None,
        help="stdio message format. Precedence: CLI > config stdio_format > env > auto",
    )
    parser.add_argument(
        "cmd",
        nargs=argparse.REMAINDER,
        help="Compact/manual mode MCP command and args, e.g. npx -y @playwright/mcp@latest --extension",
    )
    args = parser.parse_args()

    spec, server_name = resolve_server_spec(args)
    if args.agent_config:
        args.hub = str(spec.get("hub_url") or spec.get("hub") or args.hub)
        args.agent_id = str(spec.get("agent_id") or args.agent_id or "")
        args.name = str(spec.get("name") or args.name or "") or None
        if not args.token and spec.get("token"):
            args.token = str(spec.get("token"))
        if not args.token and spec.get("token_file"):
            args.token = Path(str(spec.get("token_file"))).read_text(encoding="utf-8").strip()
        if not args.stdio_format and spec.get("stdio_format"):
            args.stdio_format = str(spec.get("stdio_format"))
        if not args.verbose and spec.get("verbose"):
            args.verbose = True
        if not args.trace_json and spec.get("trace_json"):
            args.trace_json = True
        if spec.get("init_timeout") and args.init_timeout == int(os.getenv("GPTADMIN_MCP_INIT_TIMEOUT", "180")):
            args.init_timeout = int(spec.get("init_timeout"))

    if not args.token:
        raise SystemExit("set GPTADMIN_MCP_RELAY_TOKEN, pass --token, or set token/token_file in --agent-config")

    command = str(spec["command"])
    command_args = [str(x) for x in list(spec.get("args") or [])]
    requested_stdio_format = resolve_requested_stdio_format(args, spec)
    effective_stdio_format = resolve_stdio_format([command, *command_args], requested_stdio_format)

    agent_id = args.agent_id or f"{os.uname().nodename}-{server_name}"
    name = args.name or f"{server_name} via {os.uname().nodename}"

    if args.print_command:
        print(
            json.dumps(
                {
                    "command": command,
                    "args": command_args,
                    "env": masked_env(spec.get("env") or {}),
                    "cwd": spec.get("cwd"),
                    "requested_stdio_format": requested_stdio_format,
                    "effective_stdio_format": effective_stdio_format,
                    "agent_id": agent_id,
                    "hub": args.hub,
                    "verbose": args.verbose,
                    "trace_json": args.trace_json,
                },
                ensure_ascii=False,
            ),
            flush=True,
        )
        return 0

    client = McpStdioClient(
        command=command,
        args=command_args,
        env=spec.get("env") or {},
        cwd=spec.get("cwd"),
        init_timeout=args.init_timeout,
        requested_stdio_format=requested_stdio_format,
        verbose=args.verbose,
        trace_json=args.trace_json,
    )
    try:
        Relay(args.hub, args.token, agent_id, name, client, spec, verbose=args.verbose).run()
    finally:
        client.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
