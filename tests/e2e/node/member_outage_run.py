#!/usr/bin/env python3
"""Ordinary MCP clients through writer 44 while former writer 100 goes offline and returns."""
import argparse
import asyncio
from contextlib import ExitStack, asynccontextmanager, contextmanager
from datetime import timedelta
import json
import os
from pathlib import Path
import pwd
import secrets
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

import httpx
from anyio import ClosedResourceError
from mcp import ClientSession, McpError
from mcp.client.streamable_http import streamable_http_client
from mcp.types import CONNECTION_CLOSED

SCRIPTS = Path(__file__).resolve().parents[3] / "scripts"
sys.path.insert(0, str(SCRIPTS))
from gptadmin_node_manage import http_json, perform_join


def loopback_ports(count):
    with ExitStack() as stack:
        sockets = [stack.enter_context(socket.socket()) for _ in range(count)]
        for sock in sockets:
            sock.bind(("127.0.0.1", 0))
        return [sock.getsockname()[1] for sock in sockets]


@contextmanager
def running(command, cwd, env=None, *, group=None):
    with (cwd / "process.log").open("ab") as log:
        process = subprocess.Popen(command, cwd=cwd, env=env, stdout=log, stderr=log, group=group)
        try:
            yield process
        finally:
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                raise RuntimeError(f"{cwd.name} failed to stop")


class Executor:
    def __init__(self, root, name):
        self.directory = root / name
        self.directory.mkdir(mode=0o700)
        self.name = name
        self.target = "shell:" + name
        self.expected = []
        self.env = dict(PATH=os.environ["PATH"], HOME=str(self.directory),
                        SSL_CERT_FILE=str(root / "cert.pem"), NO_PROXY="*",
                        SHELL_NAME=name,
                        SHELL_IDENTITY_DIR=str(self.directory), SHELL_SPOOL_DIR=str(self.directory / "spool"),
                        SHELL_DEFAULT_USER=pwd.getpwuid(os.getuid()).pw_name,
                        SHELL_DEFAULT_HOME=str(self.directory), SHELL_DEFAULT_CWD=str(self.directory),
                        SHELLMCP_MCP_CONFIG=str(self.directory / "no-children.json"))
        (self.directory / "executor-name").write_text(name)


class Node(Executor):
    def __init__(self, root, name, port, public_url, signer, reader=False):
        super().__init__(root, name)
        self.origin = f"http://127.0.0.1:{port}"
        self.control = secrets.token_urlsafe(32)
        self.env.update(GPTADMIN_CONFIG_DIR=str(self.directory), GPTADMIN_ROOT=str(self.directory),
                        GPTADMIN_ENV_FILE=str(self.directory / "no.env"),
                        GPTADMIN_HUB_HOST="127.0.0.1", GPTADMIN_HUB_PORT=str(port),
                        GPTADMIN_AUTH_MODE="reader" if reader else "writer",
                        GPTADMIN_NODE_PUBLIC_URL=self.origin if reader else public_url,
                        PUBLIC_ORIGIN=public_url, MCP_RESOURCE=public_url + "/mcp",
                        OAUTH_CLIENT_SECRET=signer, CTL_TOKEN=self.control, SHELL_TOKEN=secrets.token_urlsafe(32))

    def admin(self, path, body=None):
        status, value = http_json("GET" if body is None else "POST", self.origin + path,
                                  token=self.control, body=body, timeout=5)
        if status != 200:
            raise RuntimeError(f"{self.name} {path} returned HTTP {status}")
        return value

    def writer_id(self):
        mesh = self.admin("/admin/api/nodes")
        return next((row["node_id"] for row in mesh["nodes"] if row["name"] == self.target
                     and row["routing_ready"] and row["node_id"] == mesh["writer_id"]), None)


async def wait_for(label, process, check, timeout=70):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"{label}: process exited with {process.returncode}")
        try:
            value = check()
            if value:
                return value
        except (urllib.error.URLError, ConnectionError, TimeoutError):
            pass
        await asyncio.sleep(.25)
    raise RuntimeError(f"timed out waiting for {label}")


def enroll(writer, member):
    grant = writer.admin("/admin/api/nodes/grant", {"name": member.target, "ttl_minutes": 5})
    joined = perform_join(writer.origin, grant["grant"], member.directory / "node-identity.json",
                          member.name, [member.origin], None, None)
    member.env["GPTADMIN_AUTH_SOURCE_ID"] = joined["writer_id"]


async def approve_polling_executor(writer, executor, process):
    def registration(status):
        return next((row for row in writer.admin("/servers")["servers"]
                     if row["server_id"] == executor.target and row["status"] == status), None)

    pending = await wait_for(f"{executor.name} pending approval", process, lambda: registration("awaiting_approval"))
    identity = json.loads((executor.directory / "shellmcp_identity.json").read_text())
    if (pending["mode"] != "long_poll" or pending["meta"].get("poll_heartbeat") is not True
            or pending["meta"].get("fingerprint") != identity["fingerprint"]):
        raise RuntimeError("pending registration does not match the standalone polling executor")
    context = writer.admin("/mcp-relay/servers").get("context", "")
    approval = writer.admin("/mcp-relay/call", {
        "target": "hub", "tool": "approve_pending_server", "arguments": {"server_id": executor.target},
        "context": context, "detail": "full"})
    receipt = approval["response"]
    if receipt.get("ok") is not True or receipt.get("server_id") != executor.target:
        raise RuntimeError("Hub did not approve the pending standalone executor")
    online = await wait_for(f"{executor.name} online", process, lambda: registration("online"))
    if online["meta"].get("approved") is not True:
        raise RuntimeError("standalone executor has no approval in the registry")
    print(f"{executor.name}: long_poll identity matched, awaiting_approval -> online", flush=True)


def enable_https(root, port, writer_port):
    cert, key = root / "cert.pem", root / "key.pem"
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                    "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost",
                    "-keyout", str(key), "-out", str(cert)], check=True, capture_output=True)
    pem = root / "proxy.pem"
    pem.write_bytes(cert.read_bytes() + key.read_bytes())
    config = root / "haproxy.cfg"
    config.write_text(f"""global
    nbthread 1
defaults
    mode http
    timeout connect 3s
    timeout client 60s
    timeout server 60s
listen client
    bind 127.0.0.1:{port} ssl crt {pem}
    server writer 127.0.0.1:{writer_port}
""")
    return running([shutil.which("haproxy") or "/usr/sbin/haproxy", "-db", "-f", str(config)], root)


def check_reader_route(origin, cert, reader_env, expected, phase):
    # The deployed wrapper execs the repair command with the selected address.
    result = subprocess.run(
        [SCRIPTS / "gptadmin_reader_route.sh", "printenv", "GPTADMIN_AUTOHEAL_WRITER_IP"],
        env={"PATH": os.environ["PATH"], "GPTADMIN_WRITER_ORIGIN": origin,
             "CURL_CA_BUNDLE": str(cert), "GPTADMIN_READER_ENV_FILE": str(reader_env)},
        check=True, capture_output=True, text=True, timeout=10)
    if result.stdout.strip() != expected:
        raise RuntimeError(f"{phase}: reader route {result.stdout.strip()!r}, expected {expected}")
    print(f"{phase}: repair command received {expected}", flush=True)


@asynccontextmanager
async def mcp_client(origin, tls, token, target, *, request_id=None):
    # Each connection starts with its own HTTP pool, SDK transport and initialize/search.
    headers = {"Authorization": "Bearer " + token}
    if request_id:
        headers["X-Request-ID"] = request_id
    failed_notification = None

    async def observe_notification(response):
        nonlocal failed_notification
        if (response.is_error and response.request.method == "POST"
                and json.loads(response.request.content).get("method") == "notifications/initialized"):
            failed_notification = response

    async with httpx.AsyncClient(verify=tls, trust_env=False, headers=headers,
                                 timeout=httpx.Timeout(60, connect=5)) as client:
        client.event_hooks["response"].append(observe_notification)
        async with streamable_http_client(origin + "/mcp", http_client=client) as (read, write, _):
            async with ClientSession(read, write, read_timeout_seconds=timedelta(seconds=60)) as session:
                try:
                    await session.initialize()
                    found = await session.call_tool("search", {"target": target, "query": "shell_exec"})
                except (ClosedResourceError, McpError) as error:
                    # The SDK logs a rejected initialized notification in its writer, then closes the channel.
                    if failed_notification is not None and (isinstance(error, ClosedResourceError)
                                                            or error.error.code == CONNECTION_CLOSED):
                        failed_notification.raise_for_status()
                    raise
                if found.isError or not found.structuredContent:
                    raise RuntimeError("MCP search for shell_exec failed")
                client.event_hooks["response"].remove(observe_notification)
                yield session, found.structuredContent.get("context", "")


async def execute(session, context, phase, node):
    nonce = secrets.token_hex(12)
    # Read the executor's local identity marker and leave a separately verifiable side effect.
    call = {"target": node.target, "tool": "shell_exec", "idempotency_key": nonce,
            "args": {"cmd": f"printf '%s\\n' {nonce} >> invocations && printf '%s ' {nonce} && cat executor-name",
                     "timeout": 5}}
    arguments = {"code": "return await mcp.call(" + json.dumps(call) + ");"}
    if context:
        arguments["context"] = context
    result = await session.call_tool("exec", arguments)
    detail = result.structuredContent or {}
    # A new owner may deliver its own instructions on first use. The sole
    # nested call may be repeated only on this explicit pre-dispatch refusal.
    refusal = (detail.get("error") or {}).get("receipt", {})
    if (refusal.get("http_status") == 428 and refusal.get("executed") is False
            and refusal.get("owner_target") == node.target and refusal.get("instructions") and refusal.get("context")):
        arguments["context"] = refusal["context"]
        result = await session.call_tool("exec", arguments)
        detail = result.structuredContent or {}
    receipt = detail.get("result", {})
    error = detail.get("error") or {}
    if error.get("code") == "nonterminal":
        job = error["receipt"]["job_id"]
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            reply = await session.call_tool("job", {"context": context, "id": job, "owner_target": node.target})
            receipt = reply.structuredContent or {}
            if receipt.get("status") not in ("queued", "running", "nonterminal"):
                break
            await asyncio.sleep(.2)
    elif result.isError or detail.get("status") != "completed":
        raise RuntimeError(f"{phase} {node.name}: exec {detail.get('status')} ({error.get('code')})")
    if receipt.get("stdout") != f"{nonce} {node.name}" or receipt.get("returncode") != 0:
        raise RuntimeError(f"{phase} {node.name}: incorrect executor output or incomplete receipt")
    node.expected.append(nonce)
    if (node.directory / "invocations").read_text().splitlines() != node.expected:
        raise RuntimeError(f"{phase} {node.name}: command executed on the wrong node or more than once")
    print(f"{phase}: {node.target} stdout={receipt['stdout']!r} returncode=0 marker_count=1", flush=True)


async def task_reply(session, context, code, *, target=None):
    arguments = {"code": code, "context": context, "timeout_ms": 12000}
    reply = await session.call_tool("exec", arguments)
    detail = reply.structuredContent or {}
    refusal = (detail.get("error") or {}).get("receipt", {})
    if (target and refusal.get("http_status") == 428 and refusal.get("executed") is False
            and refusal.get("owner_target") == target and refusal.get("instructions") and refusal.get("context")):
        arguments["context"] = refusal["context"]
        reply = await session.call_tool("exec", arguments)
    return reply


class TaskCallError(RuntimeError):
    def __init__(self, detail):
        error = detail.get("error") or {}
        self.code = error.get("code")
        receipt = error.get("receipt") or {}
        status = receipt.get("http_status")
        self.awaiting_executor = (
            status == 503 and receipt.get("executed") is False and receipt.get("retryable") is True
            and receipt.get("detail") == "enrolled executor has not advertised a bound quorum queue"
        )
        reason = ", awaiting executor enrollment" if self.awaiting_executor else ""
        super().__init__(f"MCP task call {detail.get('status')} ({self.code}, HTTP {status}{reason})")


async def task_call(session, context, code, *, target=None):
    reply = await task_reply(session, context, code, target=target)
    detail = reply.structuredContent or {}
    if reply.isError or detail.get("status") != "completed":
        raise TaskCallError(detail)
    return detail["result"]


async def check_writer_receipt(session, context, writer, task_id, marker, phase):
    code = f"return await mcp.tasks.wait({json.dumps(task_id)}, {{timeout_ms: 8000}});"
    task = await task_call(session, context, code)
    if task.get("taskId") != task_id or task.get("status") != "completed":
        raise RuntimeError(f"{phase}: missing or nonterminal writer receipt for {task_id}")
    job = task["result"]["structuredContent"]
    if job.get("server_id") != writer.target or job.get("job_id") != task_id:
        raise RuntimeError(f"{phase}: receipt belongs to a different executor or job")
    command = job["response"]["structuredContent"]["result"]
    if command.get("returncode") != 0 or command.get("stdout") != f"{marker} {writer.name}":
        raise RuntimeError(f"{phase}: writer receipt returned unexpected stdout or exit code")
    if (writer.directory / "receipt-invocations").read_text().splitlines() != [marker]:
        raise RuntimeError(f"{phase}: expected exactly one invocation of the retained writer task")
    print(f"{phase}: taskId={task_id} owner={job['server_id']} stdout={command['stdout']!r} "
          "returncode=0 disk_entries=1", flush=True)


async def run(binary, shell_binary, root):
    client_port, writer_port, former_port, other_port = loopback_ports(4)
    origin = f"https://localhost:{client_port}"
    signer = secrets.token_urlsafe(32)
    writer = Node(root, "node44", writer_port, origin, signer)
    former = Node(root, "node100", former_port, origin, signer, reader=True)
    other = Node(root, "node88", other_port, origin, signer, reader=True)
    polling = Executor(root, "termux-poller")
    executors = (writer, former, other, polling)
    # Keep 100's physical executor and on-disk state when enrolling it under 44.
    with running([binary], former.directory, {**former.env, "GPTADMIN_AUTH_MODE": "writer"}) as initial:
        previous_writer_id = await wait_for("100 as the initial writer", initial, former.writer_id)

    cert = root / "cert.pem"
    ingress = enable_https(root, client_port, writer_port)
    tls = ssl.create_default_context(cafile=str(cert))
    saved_ip = "127.0.0.2"
    reader_env = former.directory / "reader.env"
    reader_env.write_text(f'GPTADMIN_NODE_WRITER_CONNECT_TO="{saved_ip}"\n')
    check_reader_route(origin, cert, reader_env, saved_ip, "ingress stopped")
    # Reproduce the original failure: TCP connects, but no TLS handshake completes.
    with socket.socket() as stalled:
        stalled.bind(("127.0.0.1", client_port))
        stalled.listen()
        check_reader_route(origin, cert, reader_env, saved_ip, "TLS handshake stalled")

    with ExitStack() as stack:
        writer_process = stack.enter_context(running([binary], writer.directory, writer.env))
        current_writer_id = await wait_for("44 as the current writer", writer_process, writer.writer_id)
        assert current_writer_id != previous_writer_id, "writer identity did not change"
        proxy = stack.enter_context(ingress)

        def https_ready():
            with urllib.request.urlopen(origin + "/healthz", context=tls, timeout=2) as response:
                return json.load(response)["ok"]

        await wait_for("writer HTTPS ingress", proxy, https_ready)
        check_reader_route(origin, cert, reader_env, "127.0.0.1", "verified writer HTTPS")
        connect = writer.admin("/admin/api/agents/connect-info")
        if connect["hub_url"] != origin or not connect["shell_token"]:
            raise RuntimeError("unexpected standalone executor connection details")
        polling.env.update(HUB_URL=origin, SHELLMCP_TOKEN=connect["shell_token"], SHELLMCP_MODE="long_poll",
                           SHELLMCP_QUEUE="1", SHELLMCP_HEARTBEAT="0", SHELLMCP_SELF_REPAIR_DISABLE="1")
        shell_process = stack.enter_context(running([shell_binary], polling.directory, polling.env))
        await approve_polling_executor(writer, polling, shell_process)
        issued = writer.admin("/admin/api/mcp/issue-token", {
            "client_id": "member-outage", "role": "client", "access_mode": "full", "ttl_days": 1})
        assert issued["role"] == "client" and issued["access_token"] != writer.control
        for member in (former, other):
            enroll(writer, member)
        former_run = stack.enter_context(ExitStack())
        former_process = former_run.enter_context(running([binary], former.directory, former.env))
        stack.enter_context(running([binary], other.directory, other.env))

        def routes_match(former_online=True):
            mesh = writer.admin("/admin/api/nodes")
            if mesh["writer_id"] != current_writer_id:
                raise RuntimeError("writer changed during reader outage test")
            by_name = {row["name"]: row for row in mesh["nodes"]}
            for member, online in ((former, former_online), (other, True)):
                route = by_name.get(member.target, {})
                if (route.get("endpoints") != [member.origin] or route.get("routing_ready") != online
                        or route.get("state") != ("ready" if online else "offline")):
                    return False
            return True

        await wait_for("both enrolled reader routes at 44", writer_process, routes_match)
        assert former.admin("/admin/api/nodes")["writer_id"] == current_writer_id
        assert json.loads((former.directory / "shellmcp_identity.json").read_text())["server_id"] == previous_writer_id
        print("node100 retained its executor identity and joined writer node44 as a reader", flush=True)

        token = issued["access_token"]
        async with mcp_client(origin, tls, token, writer.target) as (session, context):
            for node in executors:
                await execute(session, context, "before outage", node)
            marker = secrets.token_hex(12)
            record = """printf '%s\\n' "$1" >> receipt-invocations && printf '%s ' "$1" && cat executor-name"""
            command = {"argv": ["sh", "-c", record, "receipt-check", marker], "timeout": 5}
            start = f"return await mcp.tasks.start({json.dumps(writer.target)}, 'shell_exec', {json.dumps(command)});"
            submitted = await task_call(session, context, start)
            task_id = submitted["taskId"]
            if not isinstance(task_id, str) or not task_id:
                raise RuntimeError("writer returned no durable task ID")
            await check_writer_receipt(session, context, writer, task_id, marker, "before stopping node100")
            former_run.close()
            assert former_process.returncode == 0, "node100 did not shut down cleanly"
            for node in (writer, other):
                await execute(session, context, "original client, node100 stopped", node)

            # The writer expires the 90-second member lease on its 30-second heartbeat tick.
            await wait_for("44 to withdraw the route to 100", writer_process, lambda: routes_match(False), timeout=140)
            print("node44 withdrew the route to node100; node88 remains ready", flush=True)
            async with mcp_client(origin, tls, token, polling.target) as (fresh, fresh_context):
                await check_writer_receipt(fresh, fresh_context, writer, task_id, marker, "fresh client after expiry")
                for node in (writer, other, polling):
                    await execute(fresh, fresh_context, "fresh client, node100 offline", node)

            restarted = stack.enter_context(running([binary], former.directory, former.env))
            await wait_for("restored node100 route at 44", restarted, routes_match)
            print("node44 restored the route to node100", flush=True)
            for node in executors:
                await execute(session, context, "original client after recovery", node)
            async with mcp_client(origin, tls, token, writer.target) as (fresh, fresh_context):
                await check_writer_receipt(fresh, fresh_context, writer, task_id, marker, "fresh client after recovery")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--shell-binary", type=Path,
                        help="standalone gptadmin-shellmcp (defaults to the Node binary's directory)")
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    shell_binary = (args.shell_binary or binary.with_name("gptadmin-shellmcp")).resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="gptadmin-member-outage-") as directory:
        asyncio.run(run(binary, shell_binary, Path(directory)))
