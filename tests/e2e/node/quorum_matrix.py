"""Public-client acceptance against three real Raft voters in an isolated Linux netns."""
import asyncio
import csv
from contextlib import AsyncExitStack, ExitStack, asynccontextmanager, closing, contextmanager
from dataclasses import dataclass, field
import base64
import hashlib
import http.client
import json
import os
import secrets
import socket
import sqlite3
import ssl
import subprocess
import sys
import time
from types import SimpleNamespace
from urllib.parse import urlencode, urlsplit

import httpx

import quorum_storage as storage
from member_outage_run import (
    SCRIPTS, Executor, TaskCallError, approve_polling_executor, loopback_ports, mcp_client, running, task_call, task_reply,
)


POLLING_GID = 31003


def executor(node):
    name = node["dir"].name
    return SimpleNamespace(name=name, target="shell:" + name, directory=node["dir"])


@dataclass
class Command:
    executor: object
    request_id: str = field(default_factory=lambda: secrets.token_hex(16))
    task_id: str = ""
    keyed: bool = True
    output: str = field(default="", repr=False)
    payload_tag: str = field(default_factory=lambda: secrets.token_hex(24), repr=False)
    retry_unavailable: bool = True

    @property
    def marker(self):
        return self.executor.directory / ("receipt-" + self.request_id)

    def start_code(self):
        script = r"""printf '%s\n' "$1" >> "$2" && """
        argv = [self.request_id, self.marker.name, self.payload_tag]
        if self.output:
            script += 'cat "$4"'
            argv.append("stdout-" + self.request_id)
        else:
            script += """printf '%s ' "$1" && cat executor-name"""
        cmd = {"argv": ["sh", "-c", script, "quorum-ci", *argv], "timeout": 5}
        options = {"idempotency_key": self.request_id} if self.keyed else {}
        return (f"const t = await mcp.tasks.start({json.dumps(self.executor.target)}, 'shell_exec', "
                f"{json.dumps(cmd)}, {json.dumps(options)}); return {{taskId:t.taskId}};")

    async def admit(self, origin, tls, token, *, trace=None):
        if self.output:
            (self.executor.directory / ("stdout-" + self.request_id)).write_text(self.output)
        deadline = time.monotonic() + 45
        while True:
            try:
                async with fresh_client(origin, tls, token, self.executor.target, retry=self.retry_unavailable,
                                        request_id=trace or self.request_id) as (session, context):
                    result = await task_call(session, context, self.start_code(), target=self.executor.target)
                break
            except Exception as error:
                if (not self.retry_unavailable or not self.keyed or not read_unavailable(error, admission_only=True)
                        or time.monotonic() >= deadline):
                    raise
                await asyncio.sleep(.5)
        handle = result.get("taskId")
        if not isinstance(handle, str) or not handle or (self.task_id and handle != self.task_id):
            raise RuntimeError("same request returned a different or missing task handle")
        self.task_id = handle

    async def submit(self, origin, tls, token, *, trace=None):
        await self.admit(origin, tls, token, trace=trace)
        await self.read_at(origin, tls, token)

    def check_effect(self):
        locations = list(self.executor.directory.parent.glob("*/" + self.marker.name))
        if locations != [self.marker] or self.marker.read_text().splitlines() != [self.request_id]:
            raise RuntimeError(f"{self.task_id} expected exactly one append on {self.executor.target}")

    def check_owner(self, node):
        encoded = base64.urlsafe_b64encode(node["id"].encode()).decode().rstrip("=")
        if not self.task_id.startswith("ot1." + encoded + "."):
            raise RuntimeError(f"{self.executor.target}: task handle does not identify the admitting {node['dir'].name}")

    async def read_at(self, origin, tls, token):
        # Read the original handle via a new client. Return bounded evidence even for large results.
        code = (f"const task = await mcp.tasks.wait({json.dumps(self.task_id)}, {{timeout_ms: 0}});"
                "if (task.status !== 'completed') return {status:task.status};"
                "const job = task.result.structuredContent; const r = job.response.structuredContent.result;"
                "return {status:task.status, handle:task.taskId, job:job.job_id, executor:job.server_id,"
                "rc:r.returncode, bytes:r.stdout.length, first:r.stdout.slice(0,64), last:r.stdout.slice(-64)};")
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            try:
                async with fresh_client(origin, tls, token, self.executor.target,
                                        retry=self.retry_unavailable) as (session, context):
                    result = await task_call(session, context, code)
            except Exception as error:
                if not self.retry_unavailable or not read_unavailable(error):
                    raise
            else:
                if result.get("status") not in ("working", "queued", "running"):
                    expected = self.output or (self.request_id + " " + self.executor.name)
                    if result != {"status": "completed", "handle": self.task_id, "job": self.task_id,
                                  "executor": self.executor.target, "rc": 0, "bytes": len(expected),
                                  "first": expected[:64], "last": expected[-64:]}:
                        raise RuntimeError(f"wrong task, executor, exit status or stdout for {self.task_id}")
                    self.check_effect()
                    return
            await asyncio.sleep(.5)
        raise RuntimeError(f"timed out waiting for {self.task_id} on {self.executor.target}")


def read_unavailable(error, *, admission_only=False):
    children = getattr(error, "exceptions", ())
    if children:
        return all(read_unavailable(child, admission_only=admission_only) for child in children)
    if isinstance(error, TaskCallError):
        return error.awaiting_executor if admission_only else error.code == "owner_unavailable"
    if isinstance(error, httpx.TimeoutException):
        return not admission_only
    if not isinstance(error, httpx.HTTPStatusError) or error.response.status_code != 503:
        return False
    if not admission_only:
        return True
    try:
        detail = error.response.json().get("detail")
    except (ValueError, httpx.ResponseNotRead):
        return False
    return detail in {"quorum Hub state is not ready", "quorum authority state is not ready on this leader",
                      "this GPTAdmin node has no quorum-confirmed Hub leadership"}


@asynccontextmanager
async def fresh_client(origin, tls, token, target, *, retry=True, **kwargs):
    started = time.monotonic()
    deadline = started + 45
    failures = 0
    while True:
        stack = AsyncExitStack()
        try:
            client = await stack.enter_async_context(mcp_client(origin, tls, token, target, **kwargs))
        except Exception as error:
            await stack.aclose()
            if not retry or not read_unavailable(error):
                raise
            failures += 1
            if failures == 1:
                print(f"{target}: public MCP session setup unavailable before command dispatch", flush=True)
            if time.monotonic() >= deadline:
                raise RuntimeError(f"{target}: public MCP session setup exceeded 45s "
                                   f"({failures} failed attempts)") from error
            await asyncio.sleep(.5)
            continue
        async with stack:
            if time.monotonic() >= deadline:
                raise RuntimeError(f"{target}: public MCP session setup exceeded 45s")
            if failures:
                print(f"{target}: public MCP session opened after {failures} failed attempts "
                      f"in {time.monotonic() - started:.2f}s", flush=True)
            yield client
        return


async def index_content(origin, tls, token, target, content):
    async with fresh_client(origin, tls, token, target) as (session, context):
        indexed = await task_call(session, context, f"return await mcp.content.index({json.dumps(content)});")
        source = indexed.get("source")
        if not source:
            raise RuntimeError("content.index returned no handle")
        read = await task_call(session, context, f"return await mcp.content.read({json.dumps(source)}, 1);")
        if read.get("text") != content:
            raise RuntimeError("local content.read differed from indexed text")
        return source


async def seed_history(node, token):
    target = executor(node)
    command = Command(target, output=secrets.token_hex(24 * 1024))
    origin = f"http://127.0.0.1:{node['http']}"
    await command.submit(origin, True, token)
    content = "historical" + secrets.token_hex(24)
    await index_content(origin, True, token, target.target, content)
    print(f"ordinary owner created {len(command.output)} bytes of verified stdout and indexed content", flush=True)
    return command, content


@contextmanager
def isolate_sockets(rules):
    if set(os.listdir("/sys/class/net")) != {"lo"} or os.getuid() != 0:
        raise RuntimeError("partition requires the test container with --network none --cap-add NET_ADMIN")
    table = "qa_" + secrets.token_hex(6)
    commands = [f"add table ip {table}", f"add chain ip {table} out {{ type filter hook output priority -50; }}"]
    commands.extend(f"add rule ip {table} out {rule} reject with tcp reset" for rule in rules)
    subprocess.run(["nft", "-f", "-"], input="\n".join(commands) + "\n", text=True, check=True)
    try:
        yield
    finally:
        subprocess.run(["nft", "delete", "table", "ip", table], check=True)


def partition(nodes, groups, public_port):
    """Cut both directions of real Raft, private HTTPS and alternative public peer routes."""
    rules = []
    for group in groups:
        other_ports = {n[k] for i, n in enumerate(nodes) if i not in group for k in ("raft", "private", "http", "public")}
        ports = ", ".join(map(str, sorted(other_ports | {public_port})))
        rules.extend(f"meta skgid {nodes[i]['group']} tcp dport {{ {ports} }}" for i in group)
    return isolate_sockets(rules)


async def eventually(label, check, timeout=75):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = check()
            if result is not None and result is not False:
                return result
        except (OSError, http.client.HTTPException):
            pass
        await asyncio.sleep(.25)
    raise RuntimeError(f"timed out waiting for {label}")


def leader_of(nodes, request, members):
    leaders = []
    for i in members:
        if nodes[i]["proc"].poll() is not None:
            raise RuntimeError(f"node{i} exited during election")
        status, data, _ = request(i, "/healthz")
        if (status == 200 and data.get("ok") and data.get("hub_ready") and data.get("role") == "leader"
                and data.get("node_id") == nodes[i]["id"] == data.get("leader_id") and data.get("voter_count") == 3):
            leaders.append(i)
    if len(leaders) > 1:
        raise RuntimeError("multiple voters reported quorum-confirmed leadership")
    return leaders[0] if leaders else None


async def caught_up(nodes, request, i, leader, checkpoint):
    def ready():
        status, data, _ = request(i, "/healthz")
        return (status == 503 and data.get("role") == "follower" and data.get("node_id") == nodes[i]["id"]
                and data.get("leader_id") == nodes[leader]["id"] and data.get("voter_count") == 3
                and data.get("replica_ready") and data.get("raft_applied_index", -1) >= checkpoint)
    await eventually(f"node{i} to apply the Raft log through index {checkpoint}", ready)
    print(f"node{i} caught up through Raft index {checkpoint}", flush=True)


async def refuse(origin, tls, token, context, command, *, direct):
    params = {"name": "exec", "arguments": {"code": command.start_code(), "context": context}}
    headers = {"Authorization": "Bearer " + token, "X-Request-ID": command.request_id,
               "Accept": "application/json, text/event-stream"}
    async with httpx.AsyncClient(verify=tls, trust_env=False, timeout=10) as client:
        response = await client.post(origin + "/mcp", headers=headers, json={
            "jsonrpc": "2.0", "id": command.request_id, "method": "tools/call", "params": params})
    if response.status_code != 503:
        raise RuntimeError(f"fenced command {command.request_id} received HTTP {response.status_code}")
    if direct:
        detail = response.json().get("detail", "")
        if not any(word in detail for word in ("quorum", "leadership", "current quorum leader")):
            raise RuntimeError("minority refusal did not come from the Hub quorum gate")


def verify_rejected(nodes, rejected, executors):
    for command in rejected:
        if any((owner.directory / command.marker.name).exists() for owner in executors):
            raise RuntimeError("a command rejected during partition had an executor-side effect")
        for node in nodes:
            db_url = (node["dir"] / "tasks_state.sqlite").as_uri() + "?mode=ro"
            with closing(sqlite3.connect(db_url, uri=True)) as db:
                admitted = db.execute("SELECT 1 FROM task_records WHERE kind='shell' AND "
                                      "json_extract(payload,'$.trace_id')=? LIMIT 1", (command.request_id,)).fetchone()
                reserved = db.execute("SELECT 1 FROM task_requests WHERE substr(key,-32)=? LIMIT 1",
                                      (command.request_id,)).fetchone()
            if admitted or reserved:
                raise RuntimeError("a command rejected during partition left a job or an idempotency reservation")


async def lookup_denied(origin, tls, token, command, code):
    async with fresh_client(origin, tls, token, command.executor.target) as (session, context):
        reply = await session.call_tool("exec", {"context": context, "code":
            f"return await mcp.tasks.wait({json.dumps(command.task_id)}, {{timeout_ms:0}});"})
    detail = reply.structuredContent or {}
    error = detail.get("error") or {}
    receipt = error.get("receipt") or {}
    if (detail.get("status") != "failed" or error.get("code") != code or detail.get("result") is not None
            or receipt.get("retryable") is not (code == "owner_unavailable")
            or receipt.get("id") != command.task_id or (command.request_id + " " + command.executor.name) in reply.model_dump_json()):
        raise RuntimeError(f"expected {code} for the original handle, got {detail.get('status')}/{error.get('code')}")


async def replay_offline(origin, tls, token, command):
    async with fresh_client(origin, tls, token, command.executor.target, request_id=command.request_id) as (session, context):
        reply = await task_reply(session, context, command.start_code(), target=command.executor.target)
    detail = reply.structuredContent or {}
    if detail.get("status") == "completed":
        if (detail.get("result") or {}).get("taskId") != command.task_id:
            raise RuntimeError("admission replay returned a new or missing handle")
    else:
        error = detail.get("error") or {}
        receipt = error.get("receipt") or {}
        handle = receipt.get("taskId") or receipt.get("job_id") or receipt.get("id")
        if error.get("code") not in ("owner_unavailable", "task_outcome_unknown") or handle != command.task_id:
            raise RuntimeError(f"unrecognized replay response for original handle: {error.get('code')}")
    command.check_effect()


async def partitions(nodes, request, origin, tls, token, voters, polling):
    members = tuple(range(3))
    leader = await eventually("leader before partition", lambda: leader_of(nodes, request, members))
    majority = tuple(i for i in members if i != leader)
    pids = [n["proc"].pid for n in nodes]
    port = urlsplit(origin).port
    rejected = []
    async with fresh_client(origin, tls, token, voters[leader].target) as (_, context):
        pass
    # Hold only the real mobile executor's outbound polling. Let previously open long
    # polls expire before admitting a job that must still be queued when the partition starts.
    identity = json.loads((polling.directory / "shellmcp_identity.json").read_text())
    fields = {k: identity[k] for k in ("server_id", "fingerprint", "public_key")}
    path = "/queue/" + polling.name + "?" + urlencode(dict(fields, timeout=1, mode="long_poll"))
    poll_token = polling.env["SHELLMCP_TOKEN"]
    with ExitStack() as network:
        hold_mobile = network.enter_context(ExitStack())
        hold_mobile.enter_context(isolate_sockets([f"meta skgid {POLLING_GID} tcp dport {port}"]))
        await asyncio.sleep(int(nodes[leader]["env"]["MCP_RELAY_POLL_MAX_TIMEOUT"]) + 1)
        status, idle, _ = request(leader, path, token=poll_token)
        if status != 200 or "runtime_settings" not in (idle or {}) or "id" in idle:
            raise RuntimeError(f"authenticated polling control was not idle, HTTP {status}")
        pending = Command(polling, keyed=False)
        await pending.admit(origin, tls, token)
        with closing(sqlite3.connect((nodes[leader]["dir"] / "tasks_state.sqlite").as_uri() + "?mode=ro", uri=True)) as db:
            row = db.execute("SELECT json_extract(payload,'$.status') FROM task_records WHERE kind='shell' AND key=?",
                             (pending.task_id,)).fetchone()
        if row != ("queued",) or pending.marker.exists():
            raise RuntimeError("unkeyed work was not pending at the dispatch boundary")
        network.enter_context(partition(nodes, [(leader,), majority], port))
        status, denied_poll, _ = request(leader, path, token=poll_token)
        if status != 503 or "quorum" not in (denied_poll or {}).get("detail", "").lower():
            raise RuntimeError(f"isolated owner did not fence the authenticated dequeue, HTTP {status}")
        denied = Command(voters[leader])
        rejected.append(denied)
        minority = f"http://127.0.0.1:{nodes[leader]['http']}"
        await refuse(minority, True, token, context, denied, direct=True)
        successor = await eventually("2/3 majority election", lambda: leader_of(nodes, request, majority))
        await edge_ready(origin, tls, nodes[successor]["id"])
        rejected.append(Command(voters[leader], keyed=False))
        await refuse(minority, True, token, context, rejected[-1], direct=True)
        hold_mobile.close()
        await Command(voters[successor]).submit(origin, tls, token)
        await Command(polling, keyed=False).submit(origin, tls, token)
        if pending.marker.exists():
            raise RuntimeError("previously queued unkeyed work executed while its admitting Hub was isolated")
        checkpoint = request(successor, "/healthz")[1]["raft_applied_index"]
        print(f"2+1: live former leader node{leader} refused admission, node{successor} completed new work", flush=True)
    await caught_up(nodes, request, leader, successor, checkpoint)
    with partition(nodes, [(i,) for i in members], port):
        await eventually("every voter to lose quorum", lambda: all(request(i, "/healthz")[0] == 503 for i in members))
        await edge_ready(origin, tls, None)
        for i in members:
            denied = Command(voters[i], keyed=False)
            await refuse(f"http://127.0.0.1:{nodes[i]['http']}", True, token, context, denied, direct=True)
            rejected.append(denied)
        denied = Command(polling)
        await refuse(origin, tls, token, context, denied, direct=False)
        rejected.append(denied)
        verify_rejected(nodes, rejected, [*voters, polling])
    restored = await eventually("leader after healing", lambda: leader_of(nodes, request, members))
    await edge_ready(origin, tls, nodes[restored]["id"])
    if any(n["proc"].pid != pid or n["proc"].poll() is not None for n, pid in zip(nodes, pids)):
        raise RuntimeError("a voter restarted during the network partitions")
    await Command(polling).submit(origin, tls, token)
    verify_rejected(nodes, rejected, [*voters, polling])
    print("1+1+1: 3 live voters and public ingress refused admission; no rejected effect after healing", flush=True)
    return pending


async def outages(nodes, start, stop, request, origin, tls, token, outsider, polling):
    voters = [executor(n) for n in nodes]
    members = tuple(range(3))
    initial = await eventually("initial leader", lambda: leader_of(nodes, request, members))
    old = Command(polling)
    await old.submit(origin, tls, token)
    old.check_owner(nodes[initial])
    print(f"node{initial} stored {old.task_id} executed by {polling.target}", flush=True)
    # A different scoped OAuth caller can create and read its own task on the same executor.
    await Command(polling).submit(origin, tls, outsider)
    await lookup_denied(origin, tls, outsider, old, "not_found")

    retained = None
    for victim in (initial, *(i for i in members if i != initial)):
        survivors = tuple(i for i in members if i != victim)
        before = await eventually("leader before outage", lambda: leader_of(nodes, request, members))
        stop(victim, crash=True)
        if nodes[victim]["proc"].returncode != -9:
            raise RuntimeError(f"node{victim} did not suffer the requested abrupt outage")
        leader = await eventually("majority to elect a ready leader", lambda: leader_of(nodes, request, survivors))
        await edge_ready(origin, tls, nodes[leader]["id"])
        if victim == initial:
            await lookup_denied(origin, tls, token, old, "owner_unavailable")
        else:
            await old.read_at(origin, tls, token)
        for i in survivors:
            await Command(voters[i]).submit(origin, tls, token)
        mobile = Command(polling)
        await mobile.submit(origin, tls, token)
        mobile.check_owner(nodes[leader])
        if victim == initial:
            await replay_offline(origin, tls, token, old)
            print(f"owner node{victim} offline: original handle unavailable, new poller task completed, "
                  "same key preserved one effect", flush=True)
        if retained and retained[0] != victim:
            await retained[1].read_at(origin, tls, token)
        retained = leader, mobile
        checkpoint = request(leader, "/healthz")[1]["raft_applied_index"]
        start(victim)
        await caught_up(nodes, request, victim, leader, checkpoint)
        await old.read_at(origin, tls, token)
        if victim == initial:
            await lookup_denied(origin, tls, outsider, old, "not_found")
            await old.submit(origin, tls, token)
        # The restarted voter owns its embedded executor. Prove the route by its physical effect.
        await Command(voters[victim]).submit(origin, tls, token)
        print(f"node{victim} ({'leader' if before == victim else 'follower'}) stopped and rejoined; "
              f"successor node{leader}, poller task {mobile.task_id}, original effect count=1", flush=True)

    await old.submit(origin, tls, token)


async def storage_cases(nodes, start, stop, request, origin, tls, token):
    voters = [executor(n) for n in nodes]
    restored = await eventually("leader before storage probes", lambda: leader_of(nodes, request, range(3)))
    # Establish the previous snapshot generation before recording the workload's log intervals.
    await storage.snapshot_all(nodes, request, restored)
    content = "current" + secrets.token_hex(24)
    source = await index_content(origin, tls, token, voters[restored].target, content)

    # Equal request counts, different random stdout. Measure committed log bytes at every voter.
    # Keep these tasks on the admitting Hub so neither peers nor the mobile poller need any payload.
    samples = []
    for size in (64, 48 * 1024):
        follower = (restored + 1) % len(nodes)
        stop(follower, crash=True)
        begin = request(restored, "/healthz")[1]["raft_applied_index"]
        commands = []
        for _ in range(3):
            cmd = Command(voters[restored], output=secrets.token_hex(size // 2))
            await cmd.submit(origin, tls, token)
            cmd.check_owner(nodes[restored])
            commands.append(cmd)
        end = request(restored, "/healthz")[1]["raft_applied_index"]
        samples.append((begin, end, commands))
        start(follower)
        await caught_up(nodes, request, follower, restored, end)
    # An HTTP trace can span calls. Only explicit client keys reserve journal slots.
    # Use a fresh keyed positive control so reservation expiry cannot mask a false negative.
    control = Command(voters[restored])
    await control.submit(origin, tls, token)
    await control.submit(origin, tls, token)
    begin = request(restored, "/healthz")[1]["raft_applied_index"]
    unkeyed = []
    trace = secrets.token_hex(16)
    for _ in range(2):
        command = Command(voters[restored], keyed=False, output=secrets.token_hex(24 * 1024))
        await command.submit(origin, tls, token, trace=trace)
        command.check_owner(nodes[restored])
        unkeyed.append(command)
    if unkeyed[0].task_id == unkeyed[1].task_id:
        raise RuntimeError("an HTTP trace ID collapsed two independently submitted unkeyed jobs")
    # Trigger owner routing. The next Hub must read the old bodies without retaining a copy.
    stop(restored, crash=True)
    peers = tuple(i for i in range(3) if i != restored)
    successor = await eventually("readback leader", lambda: leader_of(nodes, request, peers))
    await edge_ready(origin, tls, nodes[successor]["id"])
    start(restored)
    await caught_up(nodes, request, restored, successor, request(successor, "/healthz")[1]["raft_applied_index"])
    for command in [control, *unkeyed]:
        await command.read_at(origin, tls, token)
    async with fresh_client(origin, tls, token, voters[successor].target) as (session, context):
        read = await task_call(session, context, f"return await mcp.content.read({json.dumps(source)}, 1);")
        if read.get("text") != content:
            raise RuntimeError("successor did not read the original indexed content from its reachable owner")
    # A committed control write brackets otherwise quiet log intervals on each voter.
    node_token = nodes[successor]["env"]["CTL_TOKEN"]
    headers = storage.instruction_headers(request, successor, node_token)
    storage.put_instruction(request, successor, node_token, headers, "CI end of unkeyed operations")
    end = request(successor, "/healthz")[1]["raft_applied_index"]
    journal = storage.Journal(begin, end, control, unkeyed)
    await storage.snapshot_all(nodes, request, successor)
    return restored, content, samples, journal


async def edge_ready(origin, tls, leader_id):
    deadline = time.monotonic() + 45
    async with httpx.AsyncClient(verify=tls, trust_env=False, timeout=3) as client:
        while time.monotonic() < deadline:
            try:
                result = await client.get(origin + "/healthz")
                if leader_id is None and result.status_code == 503:
                    return
                if leader_id and result.status_code == 200 and result.json().get("leader_id") == leader_id:
                    return
            except (httpx.TransportError, ValueError):
                pass
            await asyncio.sleep(.25)
    raise RuntimeError("public ingress did not observe the expected quorum state")


def member_tls(root, nodes):
    # TLS termination on the three enrolled origins. The shared ingress makes the routing decision.
    config = ["global", "    nbthread 1", "defaults", "    mode http", "    timeout connect 3s",
              "    timeout server 130s", "    timeout client 130s"]
    directory = root / "member-tls"
    directory.mkdir(mode=0o700)
    for i, node in enumerate(nodes):
        pem = directory / f"node{i}.pem"
        pem.write_bytes(node["cert"].read_bytes() + node["cert_key"].read_bytes())
        pem.chmod(0o600)
        config.extend([f"listen node{i}", f"    bind 127.0.0.1:{node['public']} ssl crt {pem}",
                       f"    server local 127.0.0.1:{node['http']}"])
    path = directory / "haproxy.cfg"
    path.write_text("\n".join(config) + "\n")
    return running(["/usr/sbin/haproxy", "-db", "-f", path], directory)


async def missed_ingress_check(check_port, stats_port, origin, tls, token, polling):
    def server_stats():
        with socket.create_connection(("127.0.0.1", stats_port), timeout=2) as sock:
            sock.sendall(b"show stat\n")
            with sock.makefile("r") as reply:
                return {row["# pxname"]: row for row in csv.DictReader(reply)
                        if row["# pxname"].startswith("gptadmin_quorum_voter") and row["svname"] == "physical"}

    def active():
        ready = [row for row in server_stats().values() if row["status"] == "UP"]
        return ready[0] if len(ready) == 1 else None

    before = await eventually("one verified public leader backend", active)

    def missed():
        sample = server_stats()[before["# pxname"]]
        return sample if int(sample["chkfail"]) > int(before["chkfail"]) else None

    # Drop only probes. The real leader and its data socket continue serving.
    with isolate_sockets([f"tcp dport {check_port}"]):
        sample = await eventually("one failed HAProxy probe of the live leader", missed, timeout=10)
    if not sample["status"].startswith("UP") or sample["chkdown"] != before["chkdown"]:
        raise RuntimeError("one missed health check removed the live leader from public ingress")
    await Command(polling, retry_unavailable=False).submit(origin, tls, token)
    print("a missed leader health check preserved the public command path", flush=True)


@contextmanager
def public_ingress(root, nodes, origin, ca_path, cert, key):
    directory = root / "public-ingress"
    directory.mkdir(mode=0o700)
    pem = directory / "edge.pem"
    pem.write_bytes(cert.read_bytes() + key.read_bytes())
    pem.chmod(0o600)
    port = urlsplit(origin).port
    check, tunnel, reclaim, stats = [p for p in loopback_ports(5) if p != port][:4]
    # Model the two pre-existing physical routes required by the deployed renderer.
    # The active frontend, health policy, selected backend and retry policy come from that renderer.
    base = directory / "original.cfg"
    base.write_text(f"""global
    nbthread 1
    stats socket ipv4@127.0.0.1:{stats} level user
defaults
    mode http
    timeout connect 3s
    timeout client 130s
    timeout server 130s
frontend recovery_https_http
    bind 127.0.0.1:{port} ssl crt {pem}
    acl host_gptadmin_child hdr(host) -i child.test:{port}
    acl host_gptadmin_hub hdr(host) -i localhost:{port}
    use_backend be_gptadmin_child if host_gptadmin_child
    use_backend be_gptadmin_hub if host_gptadmin_hub
backend be_gptadmin_child
    mode http
    server original 127.0.0.1:{nodes[0]['http']} check
backend be_gptadmin_hub
    mode http
    server original 127.0.0.1:{nodes[0]['http']} check
""")
    pool = directory / "pool.json"
    config = directory / "active.cfg"
    renderer = [sys.executable, SCRIPTS / "gptadmin_quorum_ingress.py"]
    pins = [arg for i, n in enumerate(nodes) for arg in ("--pin", f"node{i}.test=127.0.0.1:{n['public']}")]
    subprocess.run([*renderer, "project", "--quorum", nodes[0]["dir"] / "quorum.json", *pins,
                    "--ca-file", ca_path, "--out", pool], check=True)
    subprocess.run([*renderer, "render", "--base", base, "--base-sha256", hashlib.sha256(base.read_bytes()).hexdigest(),
                    "--pool", pool, "--stage", "active", "--check-port", str(check), "--tunnel-port", str(tunnel),
                    "--reclaim-port", str(reclaim), "--out", config], check=True)
    subprocess.run(["/usr/sbin/haproxy", "-c", "-f", config], check=True)
    with ExitStack() as stack:
        stack.enter_context(running([sys.executable, SCRIPTS / "gptadmin_failover_proxy.py", "--quorum-checks", pool,
                                     "--check-port", str(check)], directory))
        stack.enter_context(running(["/usr/sbin/haproxy", "-db", "-f", config], directory))
        yield check, stats


async def verify_dispatch_capacity(root, shell, origin, tls, token, polling_env, admin):
    executor = Executor(root, "full-executor")
    executor.env.update({key: polling_env[key] for key in (
        "SSL_CERT_FILE", "HUB_URL", "SHELLMCP_TOKEN", "SHELLMCP_MODE", "SHELLMCP_QUEUE",
        "SHELLMCP_HEARTBEAT", "SHELLMCP_SELF_REPAIR_DISABLE", "QUEUE_LONG_POLL_TIMEOUT_S")})
    journal = executor.directory / "shellmcp-dispatch"
    journal.mkdir(mode=0o700)
    # The Hub assigns the task ID. Fill every shard with unresolved claims so
    # the real executor encounters the capacity limit regardless of that ID.
    for shard in range(256):
        claims = {f"{shard:02x}{slot:062x}": 0 for slot in range(128)}
        (journal / f"{shard:02x}.v2").write_text(json.dumps({"clock": int(time.time()), "claims": claims}))
    with running([shell], executor.directory, executor.env, group=POLLING_GID) as process:
        await approve_polling_executor(SimpleNamespace(admin=admin), executor, process)
        command = Command(executor)
        await command.admit(origin, tls, token)
        async with fresh_client(origin, tls, token, executor.target) as (session, context):
            deadline = time.monotonic() + 12
            while True:
                reply = await session.call_tool("job", {
                    "context": context, "id": command.task_id})
                result = reply.structuredContent or {}
                if result.get("status") == "failed":
                    failure = result.get("error") or {}
                    if failure.get("code") != "executor_dispatch_capacity" or failure.get("executed") is not False:
                        raise RuntimeError(f"owner returned a different terminal failure: {failure!r}")
                    break
                if result.get("status") not in ("queued", "running") or time.monotonic() >= deadline:
                    raise RuntimeError("owner never recorded a terminal dispatch capacity refusal")
                await asyncio.sleep(.25)
        await command.admit(origin, tls, token)
        if command.marker.exists():
            raise RuntimeError("dispatch capacity refusal permitted a command side effect")
    print("full dispatch journal: owner recorded a pre-start refusal and the keyed replay had no effect", flush=True)


async def run(root, nodes, start, stop, request, origin, ca_path, cert, key, token, outsider, shell, history, case):
    tls = ssl.create_default_context(cafile=str(ca_path))
    with public_ingress(root, nodes, origin, ca_path, cert, key) as (check, stats):
        leader = await eventually("elected writer", lambda: leader_of(nodes, request, range(3)))
        await edge_ready(origin, tls, nodes[leader]["id"])
        def admin(path, body=None):
            current = leader_of(nodes, request, range(3))
            if current is None:
                raise ConnectionError("quorum leadership unavailable for executor approval")
            status, value, _ = request(current, path, body, nodes[current]["env"]["CTL_TOKEN"])
            if status == 503 and body is None:
                raise ConnectionError(f"{path}: waiting for the current Hub to serve admin reads")
            if status != 200:
                raise RuntimeError(f"{path} HTTP {status}")
            return value
        polling = Executor(root, "standalone")
        connect = admin("/admin/api/agents/connect-info")
        if connect.get("hub_url") != origin or not connect.get("shell_token"):
            raise RuntimeError("standalone connect-info did not supply the public origin and polling credential")
        polling.env.update(SSL_CERT_FILE=str(ca_path), HUB_URL=origin, SHELLMCP_TOKEN=connect["shell_token"],
                           SHELLMCP_MODE="long_poll", SHELLMCP_QUEUE="1", SHELLMCP_HEARTBEAT="0",
                           SHELLMCP_SELF_REPAIR_DISABLE="1", QUEUE_LONG_POLL_TIMEOUT_S="3")
        # A standalone executor can learn physical quorum URLs. Prove the external
        # client's configured single edge actually handles polling and result callbacks.
        with isolate_sockets([f"meta skgid {POLLING_GID} tcp dport != {urlsplit(origin).port}"]), \
                running([shell], polling.directory, polling.env, group=POLLING_GID) as process:
            await approve_polling_executor(SimpleNamespace(admin=admin), polling, process)
            if case in (None, "outages"):
                await missed_ingress_check(check, stats, origin, tls, token, polling)
                await outages(nodes, start, stop, request, origin, tls, token, outsider, polling)
            pending = None
            if case in (None, "fencing"):
                pending = await partitions(nodes, request, origin, tls, token, [executor(n) for n in nodes], polling)
            if case in (None, "storage"):
                storage_owner, content, samples, journal = await storage_cases(nodes, start, stop, request, origin, tls, token)
            if case in (None, "dispatch-capacity"):
                leader = await eventually("leader before dispatch capacity test", lambda: leader_of(nodes, request, range(3)))
                await verify_dispatch_capacity(root, shell, origin, tls, token, polling.env, admin)
            if process.poll() is not None:
                raise RuntimeError("standalone polling process exited during failover")
        for i in range(len(nodes)):
            stop(i)
        if pending and pending.marker.exists():
            pending.check_effect()
        if case in (None, "storage"):
            storage.inspect(nodes, polling, history, storage_owner, content, samples, journal)
        print(f"owner-local quorum {case or 'matrix'} passed", flush=True)
