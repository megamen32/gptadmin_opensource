#!/usr/bin/env python3
"""Exercise nested target scopes on one real Node and one managed ShellMCP."""
import argparse
from contextlib import ExitStack
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("node", type=Path)
    parser.add_argument("shell", type=Path)
    args = parser.parse_args()
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    origin = f"http://127.0.0.1:{port}"
    owner, queue = secrets.token_urlsafe(32), secrets.token_urlsafe(32)

    device_token = secrets.token_urlsafe(32)
    contexts = {}

    def request(method, path, token, body=None, etag=None, response_headers=None):
        headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json"}
        if etag:
            headers["If-Match"] = etag
        data = json.dumps(body).encode() if body is not None else None
        for attempt in range(2):
            if token in contexts:
                headers["X-GPTAdmin-Context"] = contexts[token]
            req = urllib.request.Request(origin + path, method=method, headers=headers, data=data)
            try:
                response = urllib.request.urlopen(req, timeout=12)
            except urllib.error.HTTPError as exc:
                response = exc
            with response:
                status, value = response.status, json.load(response)
            if (attempt == 0 and status == 428 and value.get("executed") is False
                    and (value.get("error") or {}).get("code") == "instructions_required"
                    and value.get("instructions") and value.get("context")):
                contexts[token] = value["context"]
                continue
            if response_headers is not None:
                response_headers.update({k.lower(): v for k, v in response.headers.items()})
            return status, value

    def expect(code, *args):
        status, value = request(*args)
        assert status == code, (args[0], args[1], status, value.get("detail"))
        return value

    def accept_instructions(token):
        response = expect(200, "POST", "/mcp", token, {"jsonrpc": "2.0", "id": 1, "method": "initialize",
            "params": {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "tree-qa", "version": "1"}}})
        assert response["result"]["instructions"]

    def call(target, tool, arguments):
        return {"target": target, "tool": tool, "arguments": arguments, "detail": "full"}

    def can_execute(token, target, allowed):
        marker = "qa-" + secrets.token_hex(8)
        payload = call(target, "shell_exec", {"cmd": "printf " + marker, "timeout": 5})
        data = expect(200 if allowed else 403, "POST", "/mcp-relay/call", token, payload)
        if allowed:
            job = data.get("job_id") or data.get("task_id")
            deadline = time.monotonic() + 15
            while data.get("status") != "completed" and time.monotonic() < deadline:
                assert job, data.get("status")
                time.sleep(.1)
                data = expect(200, "GET", "/mcp-relay/job/" + job, token)
            assert data.get("status") == "completed" and marker in json.dumps(data.get("response", data))
        else:
            assert not data.get("job_id") and not data.get("task_id")

    def registrations():
        return {x["name"]:x for x in expect(200, "GET", "/servers", owner)["servers"]}

    def wait(check, process):
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            assert process.poll() is None, "test process exited"
            try:
                if check():
                    return
            except (OSError, urllib.error.URLError):
                pass
            time.sleep(.08)
        raise TimeoutError("isolated executor was not registered")

    with tempfile.TemporaryDirectory(prefix="gptadmin-access-tree-") as tmp, ExitStack() as stack:
        def start(binary, name, extra):
            path = Path(tmp) / name
            path.mkdir(mode=0o700)
            env = dict(PATH=os.environ["PATH"], HOME=str(path), NO_PROXY="*", SHELL_NAME=name,
                       SHELL_IDENTITY_DIR=str(path), SHELL_SPOOL_DIR=str(path / "spool"),
                       SHELL_DEFAULT_CWD=str(path), SHELL_DEFAULT_HOME=str(path),
                       SHELLMCP_MCP_CONFIG=str(path / "no-children.json"),
                       SHELLMCP_SELF_REPAIR_DISABLE="1", **extra)
            log = stack.enter_context((path / "service.log").open("ab"))
            proc = subprocess.Popen([str(binary.resolve())], cwd=path, env=env, stdout=log, stderr=log)
            def stop():
                proc.terminate()
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
            stack.callback(stop)
            return proc

        central = Path(tmp) / "node-office"
        hub = start(args.node, central.name, {"GPTADMIN_CONFIG_DIR": str(central), "GPTADMIN_ROOT": str(central),
            "GPTADMIN_ENV_FILE": str(central / "absent.env"), "GPTADMIN_HUB_HOST": "127.0.0.1", "GPTADMIN_HUB_PORT": str(port),
            "PUBLIC_ORIGIN": origin, "OAUTH_CLIENT_SECRET": secrets.token_urlsafe(32), "CTL_TOKEN": owner, "SHELL_TOKEN": queue})
        wait(lambda: central.name in registrations(), hub)
        accept_instructions(owner)
        standalone = start(args.shell, "field-worker", {"HUB_URL": origin, "SHELLMCP_TOKEN": device_token,
            "SHELLMCP_MODE": "long_poll", "SHELLMCP_QUEUE": "1", "SHELLMCP_HEARTBEAT": "0"})
        wait(lambda: "field-worker" in registrations(), standalone)
        pending = registrations()["field-worker"]
        assert pending["status"] == "awaiting_approval", "fresh executor must await fingerprint approval"
        identity = json.loads((Path(tmp) / "field-worker" / "shellmcp_identity.json").read_text())
        assert pending["meta"].get("fingerprint") == identity["fingerprint"]
        args_ = call("hub", "approve_pending_server", {"server_id":"shell:field-worker"})
        expect(200, "POST", "/mcp-relay/call", owner, args_)
        wait(lambda: registrations()["field-worker"]["status"] == "online", standalone)

        clients = {}
        for group in ("company", "eng", "field", "finance"):
            expect(200, "PUT", "/admin/api/access-profiles/" + group, owner,
                   {"access_mode":"full", "approval_mode":"bounded_autonomous",
                    "allowed_tools":["shell_exec"], "allowed_targets":[]}, "*")
            issued = expect(200, "POST", "/admin/api/mcp/issue-token", owner,
                            {"client_id": "tree-"+group, "role":"client", "profile_id":group, "access_mode":"full", "ttl_days":1})
            clients[group] = issued["access_token"]
            accept_instructions(clients[group])

        worker, office = "shell:field-worker", "shell:node-office"
        groups = {"company":{}, "eng":{"parent":"company"}, "field":{"parent":"eng","targets":[worker]},
                  "finance":{"parent":"company","targets":[office]}}
        path = "/admin/api/access-profile-trees/company"

        def mcp_tool(name, arguments):
            result = expect(200, "POST", "/mcp", owner, {"jsonrpc":"2.0", "id":2, "method":"tools/call",
                "params":{"name":name, "arguments":arguments}})["result"]
            assert not result.get("isError")
            return result["structuredContent"]

        schema = mcp_tool("schema", {"target":"hub", "tool":"access_profiles", "detail":"full"})
        assert "plan_tree" in json.dumps(schema) and "apply_tree" in json.dumps(schema)

        def tree(action, etag=None):
            arguments = {"action":action, "id":"company", "groups":groups}
            if etag:
                arguments["etag"] = etag
            result = mcp_tool("exec", {"code":
                'return await mcp.call("hub","access_profiles",' + json.dumps(arguments) + ')'})
            assert result["status"] == "completed", (result.get("status"), result.get("error", {}).get("code"))
            return result["result"]

        first = tree("plan_tree")["etag"]
        can_execute(clients["field"], worker, False)
        tree("apply_tree", first)
        for who, target, allowed in (("field",worker,True),("eng",worker,True),("company",worker,True),
                                     ("company",office,True),("finance",office,True),("eng",office,False),
                                     ("finance",worker,False),("field",office,False)):
            can_execute(clients[who], target, allowed)
        instructions_path = "/admin/api/instruction-sets/default"
        def current_instructions():
            headers = {}
            status, value = request("GET", instructions_path, owner, response_headers=headers)
            assert status == 200 and headers["etag"] and value["content"]
            return headers["etag"], value["content"]

        before = current_instructions()
        for token in (clients["eng"], device_token):
            status, _ = request("PUT", instructions_path, token, {"content": "Isolated authorization test."}, before[0])
            assert status in (401, 403), ("unexpected instruction write authority", status)
            assert current_instructions() == before, "denied write changed default instruction ETag or content"
            denied, _ = request("PUT", path, token, {"groups":groups}, first)
            assert denied in (401,403), ("unexpected management rights", denied)
        assert request("PUT", path, owner, {"groups":groups}, first)[0] == 412
        del groups["field"]
        groups["finance"]["targets"].append(worker)
        tree("apply_tree", tree("plan_tree")["etag"])
        for who, allowed in (("field",False),("eng",False),("finance",True),("company",True)):
            can_execute(clients[who], worker, allowed)
        print(json.dumps({"ok":True, "levels":3, "separate_executor":True, "scoped_clients":4,
                          "sibling_denied":True, "removed_group_revoked":True, "unchanged_tokens":True,
                          "stale_plan_rejected":True, "executor_and_department_management_denied":True,
                          "default_instruction_write_denied":True, "instruction_etag_preserved":True}))


if __name__ == "__main__":
    main()
