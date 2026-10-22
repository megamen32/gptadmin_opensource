#!/usr/bin/env python3
"""Real-process E2E and transcript benchmark for GPTAdmin exec.

The candidate Node embeds the real ShellMCP file tools and starts a separate
stdio fixture MCP process. All writes target a temporary directory. No live
credentials, service, user session, or production endpoint is used.
"""

import argparse
import base64
import hashlib
import hmac
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

import tiktoken


ENCODING = tiktoken.get_encoding("o200k_base")
MARKERS = {
    "RARE_START": "RARE_START marker=START-EXACT request=fixture-000001",
    "MIDDLE-EXACT": "RARE_MIDDLE marker=MIDDLE-EXACT request=fixture-015001",
    "END-EXACT": "RARE_END marker=END-EXACT request=fixture-030001",
}


def b64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def jwt(secret: str, audience: str, scope: str = "gptadmin.read gptadmin.exec") -> str:
    now = int(time.time())
    header = b64url(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
    claims = {
        "sub": "exec-context-candidate",
        "client_id": "exec-context-candidate",
        "scope": scope,
        "access_mode": "full",
        "aud": audience,
        "resource": audience,
        "iat": now,
        "exp": now + 1800,
        "kid": "gptadmin-hs256-v1",
    }
    payload = b64url(json.dumps(claims, separators=(",", ":")).encode())
    unsigned = header + "." + payload
    signature = b64url(hmac.new(secret.encode(), unsigned.encode(), hashlib.sha256).digest())
    return unsigned + "." + signature


class MCP:
    def __init__(self, origin: str, token: str):
        self.origin = origin
        self.token = token
        self.protocol = "2025-03-26"
        self.context = ""
        self.records = []
        self.rpc_id = 0

    def rpc(self, method: str, params: dict, label: str):
        self.rpc_id += 1
        body = {"jsonrpc": "2.0", "id": self.rpc_id, "method": method, "params": params}
        request_text = json.dumps(body, ensure_ascii=False, separators=(",", ":"))
        request = urllib.request.Request(
            self.origin + "/mcp",
            data=request_text.encode(),
            headers={
                "Authorization": "Bearer " + self.token,
                "Content-Type": "application/json",
                "Accept": "application/json, text/event-stream",
                "MCP-Protocol-Version": self.protocol,
            },
        )
        started = time.monotonic()
        try:
            with urllib.request.urlopen(request, timeout=75) as response:
                raw = response.read(12 << 20).decode(errors="replace")
        except urllib.error.HTTPError as exc:
            raw = exc.read(1 << 20).decode(errors="replace")
            try:
                decoded = json.loads(raw)
            except Exception:
                decoded = {"http_error": exc.code, "body": raw[:2000]}
            self.records.append(self.record(label, body, decoded, time.monotonic() - started))
            return decoded

        decoded_text = raw
        if raw.startswith("event:") or "\ndata:" in raw:
            data = [line[5:].strip() for line in raw.splitlines() if line.startswith("data:")]
            decoded_text = "\n".join(data)
        decoded = json.loads(decoded_text)
        elapsed = time.monotonic() - started
        self.records.append(self.record(label, body, decoded, elapsed))
        if "error" in decoded:
            return decoded
        return decoded

    @staticmethod
    def record(label: str, request: dict, response: dict, elapsed: float) -> dict:
        req = json.dumps(request, ensure_ascii=False, separators=(",", ":"))
        resp = json.dumps(response, ensure_ascii=False, separators=(",", ":"))
        return {
            "label": label,
            "request_bytes": len(req.encode()),
            "response_bytes": len(resp.encode()),
            "request_tokens": len(ENCODING.encode(req)),
            "response_tokens": len(ENCODING.encode(resp)),
            "elapsed_ms": round(elapsed * 1000, 3),
        }

    def initialize(self):
        result = self.rpc(
            "initialize",
            {"protocolVersion": self.protocol, "capabilities": {}, "clientInfo": {"name": "exec-context-e2e", "version": "1"}},
            "initialize",
        )
        assert "result" in result, result
        self.protocol = result["result"].get("protocolVersion", self.protocol)

    def tools(self):
        response = self.rpc("tools/list", {}, "tools/list")
        assert "result" in response, response
        return response["result"].get("tools", [])

    def call(self, name: str, arguments: dict, label: str | None = None):
        arguments = dict(arguments)
        if self.context and name not in ("initialize", "tools/list"):
            arguments.setdefault("context", self.context)
        response = self.rpc("tools/call", {"name": name, "arguments": arguments}, label or name)
        if "error" in response:
            return {"rpc_error": response["error"]}
        result = response.get("result", {})
        structured = result.get("structuredContent")
        if isinstance(structured, dict):
            return structured
        texts = [item.get("text", "") for item in result.get("content", []) if item.get("type") == "text"]
        if texts:
            try:
                return json.loads(texts[0])
            except Exception:
                return {"text": texts[0]}
        return result

    def search(self, query: str, target: str = "", label: str | None = None):
        args = {"query": query, "limit": 10}
        if target:
            args["target"] = target
        result = self.call("search", args, label or f"search:{query}")
        if isinstance(result.get("context"), str):
            self.context = result["context"]
        return result

    def exec(self, code: str, label: str, **limits):
        args = {"code": code, **limits}
        if self.context:
            args["context"] = self.context
        return self.call("exec", args, label)

    def ro(self, code: str, label: str, **limits):
        args = {"code": code, **limits}
        if self.context:
            args["context"] = self.context
        return self.call("ro", args, label)


def ro_body(response: dict) -> dict:
    # MCP.call already unwraps the tool's structuredContent, so the read-only
    # envelope is the response itself.
    return response if isinstance(response, dict) else {}


def aggregate(records: list[dict]) -> dict:
    return {
        "public_mcp_calls": len(records),
        "request_bytes": sum(r["request_bytes"] for r in records),
        "response_bytes": sum(r["response_bytes"] for r in records),
        "request_tokens_o200k_base": sum(r["request_tokens"] for r in records),
        "response_tokens_o200k_base": sum(r["response_tokens"] for r in records),
        "total_tokens_o200k_base": sum(r["request_tokens"] + r["response_tokens"] for r in records),
        "latency_ms_sum": round(sum(r["elapsed_ms"] for r in records), 3),
    }


def first_string(value, *keys) -> str:
    if not isinstance(value, dict):
        return ""
    for key in keys:
        item = value.get(key)
        if isinstance(item, str) and item.strip():
            return item
    return ""


def expected_file(index: int) -> str:
    suffix = f"{index:02d}"
    return f"source={suffix}\nlocale=Привет-{suffix}\nvalue=VALUE-{suffix}\n"


def summary_result(value: dict) -> dict:
    if value.get("status") != "completed":
        raise AssertionError(value)
    return value.get("result")


def run(candidate: Path, fixture: Path, root_fixture: Path) -> dict:
    with tempfile.TemporaryDirectory(prefix="gptadmin-exec-context-") as temp:
        root = Path(temp)
        root.chmod(0o700)
        config = root / "config"
        config.mkdir(mode=0o700)
        sources = root / "sources"
        sources.mkdir(mode=0o700)
        for i in range(50):
            (sources / f"source-{i:02d}.txt").write_text(expected_file(i), encoding="utf-8")
        file_path = root / "file-helper-canary.txt"

        mcp_config = config / "mcp.json"
        mcp_config.write_text(json.dumps({"mcpServers": {
            "exec-fixture": {
                "ref": "exec-fixture", "name": "exec fixture", "transport": "stdio",
                "command": str(fixture), "args": [], "enabled": True,
            }
        }}), encoding="utf-8")

        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            root_mcp_port = sock.getsockname()[1]
        origin = f"http://127.0.0.1:{port}"
        root_id = "root:exec-context-fixture"
        relay_token = secrets.token_urlsafe(32)
        registry = {
            "agents": {root_id: {"agent_id": root_id, "name": "Exec Context Root Fixture", "kind": "remote_mcp", "transport": "relay", "status": "online", "last_seen": time.time(), "meta": {"approved": True, "enabled": True}}},
            "relay_credentials": {root_id: hashlib.sha256(relay_token.encode()).hexdigest()},
        }
        (config / "registry_state.json").write_text(json.dumps(registry), encoding="utf-8")
        shell_name = "exec-context-" + secrets.token_hex(4)
        secret = secrets.token_urlsafe(32)
        token = jwt(secret, origin)
        env = {key: os.environ[key] for key in ("PATH", "HOME", "LANG", "TZ") if key in os.environ}
        env.update({
            "GPTADMIN_ROOT": str(root),
            "GPTADMIN_CONFIG_DIR": str(config),
            "GPTADMIN_HUB_HOST": "127.0.0.1",
            "GPTADMIN_HUB_PORT": str(port),
            "GPTADMIN_AUTH_MODE": "legacy",
            "GPTADMIN_INSTRUCTION_CONTEXT_REQUIRED": "1",
            "PUBLIC_ORIGIN": origin,
            "MCP_RESOURCE": origin,
            "OAUTH_CLIENT_SECRET": secret,
            "SHELL_TOKEN": secrets.token_urlsafe(32),
            "SHELL_NAME": shell_name,
            "SHELL_HOST": "127.0.0.1",
            "SHELL_PORT": "0",
            "SHELL_DEFAULT_USER": __import__("getpass").getuser(),
            "SHELL_DEFAULT_CWD": str(root),
            "SHELL_IDENTITY_DIR": str(config / "identity"),
            "SHELL_SPOOL_DIR": str(config / "spool"),
            "SHELLMCP_MCP_CONFIG": str(mcp_config),
            "MCP_RELAY_DEFAULT_TIMEOUT": "60",
            "MCP_RELAY_POLL_MAX_TIMEOUT": "60",
        })
        log_path = root / "candidate.log"
        report = {
            "status": "failed",
            "candidate_sha256": hashlib.sha256(candidate.read_bytes()).hexdigest(),
            "fixture_sha256": hashlib.sha256(fixture.read_bytes()).hexdigest(),
            "root_fixture_sha256": hashlib.sha256(root_fixture.read_bytes()).hexdigest(),
            "tokenizer": {"name": "tiktoken", "version": tiktoken.__version__, "encoding": "o200k_base"},
            "scenario": {},
        }
        with log_path.open("wb") as log:
            process = subprocess.Popen([str(candidate)], cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT)
            root_process = None
            try:
                deadline = time.monotonic() + 25
                client = MCP(origin, token)
                while True:
                    if process.poll() is not None:
                        raise AssertionError("candidate exited during startup")
                    try:
                        client.initialize()
                        break
                    except (OSError, urllib.error.URLError, TimeoutError):
                        if time.monotonic() > deadline:
                            raise AssertionError("candidate listener did not become ready")
                        time.sleep(0.1)

                root_env = dict(env)
                root_env.update({"ROOT_FIXTURE_HUB": origin, "ROOT_FIXTURE_AGENT": root_id, "ROOT_FIXTURE_TOKEN": relay_token, "ROOT_FIXTURE_ADDR": f"127.0.0.1:{root_mcp_port}"})
                root_process = subprocess.Popen([str(root_fixture)], cwd=root, env=root_env, stdout=log, stderr=subprocess.STDOUT)

                public_tools = client.tools()
                public_names = [tool.get("name") for tool in public_tools]
                # The public catalog is deliberately short: search finds the
                # capability, ro reads it, exec runs the script that batches the
                # calls. The direct execute facade is not public, and the retired
                # code_exec alias is never published. The task readers stay
                # published, because a client has to read the result of a task
                # it started.
                assert "exec" in public_names, public_names
                assert "ro" in public_names, public_names
                assert "search" in public_names, public_names
                assert "job" in public_names, public_names
                assert "expand" in public_names, public_names
                assert "execute" not in public_names, public_names
                assert "code_exec" not in public_names, public_names
                client.search("file editor", f"file:{shell_name}", "bootstrap:file-search")
                file_search = client.search("file editor", f"file:{shell_name}", "bootstrap:file-search-confirm")
                file_target = f"file:{shell_name}"
                assert any(x.get("name") == "file_editor" and x.get("inputSchema") for x in file_search.get("results", [])), file_search
                file_schema = client.call("schema", {"target": file_target, "tool": "file_editor", "detail": "full"}, "bootstrap:file-schema")
                assert file_schema.get("status") == "completed", file_schema

                shell_target = f"shell:{shell_name}"
                root_search = None
                for attempt in range(20):
                    root_search = client.search("zircon root", root_id, f"bootstrap:root-search-{attempt+1}")
                    if any(x.get("name") == "root_marker" for x in root_search.get("results", [])):
                        break
                    time.sleep(0.15)
                root_hit = next((x for x in root_search.get("results", []) if x.get("name") == "root_marker"), None)
                assert root_hit and root_hit.get("inputSchema"), root_search
                root_schema = client.call("schema", {"target": root_id, "tool": "root_marker", "detail": "full"}, "bootstrap:root-schema")
                assert root_schema.get("status") == "completed", root_schema

                child_search = None
                for attempt in range(20):
                    child_search = client.search("large_log", label=f"bootstrap:child-search-{attempt+1}")
                    if any(x.get("name") == "large_log" for x in child_search.get("results", [])):
                        break
                    time.sleep(0.15)
                child_hit = next((x for x in child_search.get("results", []) if x.get("name") == "large_log"), None)
                assert child_hit and child_hit.get("inputSchema"), child_search
                child_target = child_hit["target"]
                child_schema = client.call("schema", {"target": child_target, "tool": "large_log", "detail": "full"}, "bootstrap:child-schema")
                assert child_schema.get("status") == "completed", child_schema
                bootstrap_records = list(client.records)
                report["bootstrap"] = aggregate(bootstrap_records)
                report["bootstrap"]["records"] = [{"label": r["label"], "request_bytes": r["request_bytes"], "response_bytes": r["response_bytes"], "request_tokens": r["request_tokens"], "response_tokens": r["response_tokens"]} for r in bootstrap_records]
                report["public_tools"] = public_names
                report["targets"] = {"file": file_target, "child": child_target, "registered_root": root_id}
                report["search_schema_bytes"] = file_search.get("coverage", {}).get("search_result_bytes")

                composition_code = f"""
                    const rootSearch=await search('zircon root',{{target:{json.dumps(root_id)}}});
                    const rootHit=rootSearch.results.find(x=>x.name==='root_marker');
                    const rootSchema=await mcp.schema({json.dumps(root_id)},'root_marker');
                    const root=await call({json.dumps(root_id)},'root_marker',{{}});
                    const hub=await mcp.call('hub','demo',{{}});
                    const targets=await mcp.discover('hub');
                    const childSearch=await search('describe policy',{{target:{json.dumps(child_target)}}});
                    const childHit=childSearch.results.find(x=>x.name==='describe_policy');
                    const child=await call(childHit.target,childHit.name,{{}});
                    return {{root_call:root.answer,root_schema:rootSchema.tool,root_search:rootHit.name,hub_facade_call:hub.status,hub_facade_targets:targets.servers.length,child_value:child.value}};
                """
                composed = summary_result(client.exec(composition_code, "composition:root-child-facade"))
                assert composed == {"root_call": "zircon-root-http-real-result", "root_schema": "root_marker", "root_search": "root_marker", "hub_facade_call": "ok", "hub_facade_targets": composed["hub_facade_targets"], "child_value": "quartz-child-real-result"}, composed
                report["scenario"]["root_child_hub_facade"] = {"pass": True, "result": composed}

                # Short-call counterexample: a direct public Hub tool is cheaper
                # in model tokens than entering the executor for the same answer.
                direct_start = len(client.records)
                direct_demo = client.call("demo", {}, "tiny:direct-demo")
                direct_demo_records = client.records[direct_start:]
                wrapped_start = len(client.records)
                wrapped_demo = summary_result(client.exec("const d=await mcp.call('hub','demo',{}); return {status:d.status};", "tiny:exec-demo"))
                wrapped_demo_records = client.records[wrapped_start:]
                assert direct_demo.get("status") == "ok" and wrapped_demo.get("status") == "ok", (direct_demo, wrapped_demo)
                report["scenario"]["tiny_counterexample"] = {
                    "direct": aggregate(direct_demo_records), "exec": aggregate(wrapped_demo_records),
                    "same_answer": True,
                }

                # One public exec per file (full content goes to the model).
                paths = [str(sources / f"source-{i:02d}.txt") for i in range(50)]
                direct_start = len(client.records)
                direct_file_results = []
                for i, path in enumerate(paths):
                    code = f"const f=file({json.dumps(file_target)}); const r=await f('read',{json.dumps(path)},{{max_bytes:4096}}); return r.text;"
                    value = summary_result(client.exec(code, f"files:direct-{i:02d}"))
                    assert value == expected_file(i), {"file": path, "got": value}
                    direct_file_results.append(value)
                direct_file_records = client.records[direct_start:]

                batch_code = f"""
                    const f=file({json.dumps(file_target)}); const paths={json.dumps(paths)}; let bytes=0, mismatches=0;
                    for (let i=0;i<paths.length;i++) {{
                      const r=await f('read',paths[i],{{max_bytes:4096}});
                      const n=String(i).padStart(2,'0');
                      const want=`source=${{n}}\\nlocale=Привет-${{n}}\\nvalue=VALUE-${{n}}\\n`;
                      if(r.truncated || r.text!==want) mismatches++;
                      bytes+=r.bytes;
                    }}
                    return {{count:paths.length,bytes,mismatches}};
                """
                aggregate_start = len(client.records)
                batch_result = summary_result(client.exec(batch_code, "files:aggregate-50", max_calls=64, max_output_bytes=1024))
                batch_records = client.records[aggregate_start:]
                assert batch_result == {"count": 50, "bytes": sum(len(expected_file(i).encode()) for i in range(50)), "mismatches": 0}, batch_result
                assert len(direct_file_results) == 50
                bootstrap_agg = aggregate(bootstrap_records)
                direct_agg = aggregate(direct_file_records)
                batch_agg = aggregate(batch_records)
                report["scenario"]["fifty_files"] = {
                    "correctness": "PASS exact text for all 50 sources; no truncated reads",
                    "nested_tool_calls": 50,
                    "direct_one_public_exec_per_file": direct_agg,
                    "aggregate_one_public_exec_max_calls_64": batch_agg,
                    "with_required_bootstrap": {
                        "direct_tokens": direct_agg["total_tokens_o200k_base"] + bootstrap_agg["total_tokens_o200k_base"],
                        "aggregate_tokens": batch_agg["total_tokens_o200k_base"] + bootstrap_agg["total_tokens_o200k_base"],
                    },
                }

                # Real file create/read/update/stale-refusal through file:<host>.
                file_flow_code = f"""
                    const f=file({json.dumps(file_target)});
                    await f('create',{json.dumps(str(file_path))},{{text:'alpha\\nbeta\\n'}});
                    const r=await f('read',{json.dumps(str(file_path))},{{max_bytes:1024}});
                    return {{text:r.text,line_ids:r.line_ids,line_count:r.line_count,bytes:r.bytes,truncated:r.truncated}};
                """
                created = summary_result(client.exec(file_flow_code, "file:create-read"))
                assert created.get("text") == "alpha\nbeta\n" and created.get("line_count") == 2 and created.get("bytes") == len(b"alpha\nbeta\n") and created.get("truncated") is False, created
                ids = created.get("line_ids", [])
                assert len(ids) == 2, created
                edit_code = f"const f=file({json.dumps(file_target)}); const r=await f('batchEdit',{json.dumps(str(file_path))},{{operations:[{{action:'replace',start:{json.dumps(ids[1])},end:{json.dumps(ids[1])},content:'BETA'}}]}}); return r;"
                edited = summary_result(client.exec(edit_code, "file:exact-edit"))
                assert edited, edited
                stale_code = f"const f=file({json.dumps(file_target)}); await f('batchEdit',{json.dumps(str(file_path))},{{operations:[{{action:'replace',start:{json.dumps(ids[1])},end:{json.dumps(ids[1])},content:'SHOULD_NOT_APPLY'}}]}}); return 'unexpected';"
                stale = client.exec(stale_code, "file:stale-edit")
                assert stale.get("status") == "failed" and stale.get("error", {}).get("receipt", {}).get("tool") == "file_editor", stale
                readback_code = f"const f=file({json.dumps(file_target)}); return await f('read',{json.dumps(str(file_path))},{{max_bytes:1024}});"
                readback = summary_result(client.exec(readback_code, "file:stale-readback"))
                assert readback.get("text") == "alpha\nBETA\n", readback
                index_file_code = f"const f=file({json.dumps(file_target)}); return await f('read',{json.dumps(str(file_path))},{{max_bytes:1024,index:true}});"
                indexed_file = summary_result(client.exec(index_file_code, "file:index"))
                assert indexed_file.get("source") and indexed_file.get("format") == "file-text" and indexed_file.get("bytes") == len(b"alpha\nBETA\n"), indexed_file
                file_content_code = f"const h=await search('BETA',{{source:{json.dumps(indexed_file['source'])}}}); const r=await read({json.dumps(indexed_file['source'])},h.hits[0].chunk_id); return {{count:h.count,text:r.text}};"
                file_content = summary_result(client.exec(file_content_code, "file:index-readback"))
                assert file_content.get("count", 0) > 0 and file_content.get("text") == "alpha\nBETA\n", file_content
                report["scenario"]["real_file_helpers"] = {
                    "create_read_exact_edit_stale_guard": "PASS",
                    "read_metadata": {k: created[k] for k in ("line_count", "bytes", "truncated")},
                    "index_format": indexed_file.get("format"),
                    "indexed_readback_exact": True,
                }

                # Large multi-line MCP answer: compare repeated direct retrieval
                # with one opt-in index and a later, separate exec for recall.
                direct_log_start = len(client.records)
                direct_lines = []
                for marker, exact in MARKERS.items():
                    code = f"const r=await mcp.call({json.dumps(child_target)},'large_log',{{}}); return r.text.split('\\n').find(x=>x.includes({json.dumps(marker)}));"
                    line = summary_result(client.exec(code, f"log:direct-{marker}"))
                    assert line == exact, {"marker": marker, "line": line}
                    direct_lines.append(line)
                direct_log_records = client.records[direct_log_start:]

                index_log_code = f"const s=await call({json.dumps(child_target)},'large_log',{{}},{{index:true,text:'text',name:'noisy-log'}}); return s;"
                indexed_log = summary_result(client.exec(index_log_code, "log:index"))
                log_bytes = indexed_log.get("bytes", 0)
                assert 1_000_000 <= log_bytes <= 2_000_000 and indexed_log.get("format") == "text", indexed_log
                recall_code = f"""
                    const source={json.dumps(indexed_log['source'])}; const queries={json.dumps(list(MARKERS.keys()))}; const got=[];
                    for(const query of queries) {{
                      const s=await search(query,{{source,limit:2}});
                      const h=s.hits[0]; if(!h) throw new Error('missing '+query);
                      const r=await read(source,h.chunk_id);
                      const line=r.text.split('\\n').find(x=>x.includes(query));
                      got.push({{query,line,start:h.line_start,end:h.line_end}});
                    }}
                    return got;
                """
                recall = summary_result(client.exec(recall_code, "log:new-exec-recall"))
                assert len(recall) == 3, recall
                expected_lines = list(MARKERS.values())
                expected_positions = [1, 15001, 30001]
                for i, row in enumerate(recall):
                    assert row.get("line") == expected_lines[i] and row.get("start", 0) <= expected_positions[i] <= row.get("end", 0), row
                stats = client.exec(f"return await call({json.dumps(child_target)},'fixture_stats',{{}});", "log:fixture-stats")
                stats_result = summary_result(stats)
                assert stats_result.get("large_log_calls") == 4, stats_result
                report["scenario"]["large_log_fts5"] = {
                    "raw_log_bytes": log_bytes,
                    "markers_start_middle_end": "PASS exact line text and line-number ranges",
                    "direct_refetches_for_three_queries": 3,
                    "indexed_fetches_for_three_queries": 1,
                    "fixture_large_log_calls_total_including_both_paths": stats_result["large_log_calls"],
                    "direct_transcript": aggregate(direct_log_records),
                    "index_and_followup_transcript": aggregate(client.records[direct_log_start + len(direct_log_records):]),
                    "source_metadata": {k: indexed_log.get(k) for k in ("format", "bytes", "chunks", "expires_at")},
                }

                # ------------------------------------------------------------------
                # Lane 2: the read-only executor, the large-result split and the
                # confirmed full source.
                # ------------------------------------------------------------------
                shell_target = f"shell:{shell_name}"
                ro_read_path = root / "ro-read.txt"
                ro_read_path.write_text("ro-alpha\nro-beta\n", encoding="utf-8")
                ro_denied_path = root / "ro-denied.txt"

                # (1) ro reads a real temporary file through the file helper.
                ro_read = ro_body(client.ro(
                    f"const f=file({json.dumps(file_target)}); const r=await f('read',{json.dumps(str(ro_read_path))},{{max_bytes:4096}}); return r.text;",
                    "readonly:file-read"))
                assert ro_read.get("status") == "completed", ro_read
                assert ro_read.get("result") == "ro-alpha\nro-beta\n", ro_read
                report["scenario"]["ro_reads_a_real_file"] = {"pass": True, "bytes": len((ro_read_path).read_bytes())}

                # (2) ro refuses a file write and the file does not exist afterwards.
                ro_write = ro_body(client.ro(
                    f"const f=file({json.dumps(file_target)}); await f('create',{json.dumps(str(ro_denied_path))},{{text:'x'}}); return 1;",
                    "readonly:file-write-denied"))
                assert ro_write.get("status") == "failed", ro_write
                assert not ro_denied_path.exists(), "a refused read-only write created the file"

                # (3) ro refuses an MCP restart and the registration is unchanged.
                # The registration identity is compared, not the live liveness
                # flag: the check is that a refused restart did not change the
                # set of registered servers, and a heartbeat may move the
                # volatile status field between two snapshots.
                def registration_identity(servers):
                    return sorted((s.get("server_id"), s.get("name")) for s in servers or [])

                before_agents = registration_identity(client.call("discover", {}, "readonly:discover-before").get("servers", []))
                ro_restart = ro_body(client.ro(
                    f"await mcp.call({json.dumps('control:' + shell_name)},'mcp_manage',{{action:'restart'}}); return 1;",
                    "readonly:mcp-restart-denied"))
                assert ro_restart.get("status") == "failed", ro_restart
                after_agents = registration_identity(client.call("discover", {}, "readonly:discover-after").get("servers", []))
                assert before_agents == after_agents, ("a refused restart changed the registration", before_agents, after_agents)

                # (4) ro refuses the escalation wrappers.
                for label, code in [
                    ("nested-exec", f"await mcp.call('hub','exec',{{code:'return 1;'}}); return 1;"),
                    ("nested-execute", f"await mcp.call('hub','execute',{{target:'hub',tool:'settings_schema',args:{{}}}}); return 1;"),
                    ("raw-mcp-call", f"await mcp.call({json.dumps('control:' + shell_name)},'mcp_call',{{ref:'x',name:'y',arguments:{{}}}}); return 1;"),
                    ("raw-mcp-request", f"await mcp.call({json.dumps('control:' + shell_name)},'mcp_request',{{method:'tools/list'}}); return 1;"),
                    ("fleet-exec", f"await mcp.call('hub','fleetExec',{{servers:[{json.dumps(shell_target)}],cmd:'echo denied'}}); return 1;"),
                    ("fleet-exec-alias", f"await mcp.call('hub','fleet_exec',{{servers:[{json.dumps(shell_target)}],cmd:'echo denied'}}); return 1;"),
                ]:
                    denied = ro_body(client.ro(code, f"readonly:escalation-{label}"))
                    assert denied.get("status") == "failed", (label, denied)

                # (5) ro refuses index(), drop() and index:true.
                for label, code in [
                    ("index", "await index('persistent'); return 1;"),
                    ("drop", "await drop('persistent'); return 1;"),
                    ("index-true", f"await mcp.call({json.dumps(child_target)},'large_log',{{}},{{index:true}}); return 1;"),
                ]:
                    denied = ro_body(client.ro(code, f"readonly:{label}-denied"))
                    assert denied.get("status") == "failed", (label, denied)

                # A caught refusal does not let the script continue: the second
                # forbidden call is refused too and the run is not completed.
                sticky = ro_body(client.ro(
                    f"try {{ await mcp.call({json.dumps(shell_target)},'shell_exec',{{cmd:'echo denied'}}); }} catch(e) {{}} "
                    f"try {{ await mcp.call('hub','settings_set',{{settings:{{}}}}); }} catch(e) {{}} return 42;",
                    "readonly:sticky-refusal"))
                assert sticky.get("status") == "failed", sticky
                assert sticky.get("result") != 42, sticky
                report["scenario"]["ro_ceiling"] = {
                    "file_read": "PASS",
                    "file_write_denied": "PASS",
                    "mcp_restart_denied": "PASS",
                    "escalation_wrappers_denied": "PASS",
                    "index_drop_denied": "PASS",
                    "sticky_refusal": "PASS",
                }

                # (6) A large exec return is stored without an index; the first
                # search builds it and a repeated search does not rebuild it.
                big = client.exec(
                    f"await mcp.call({json.dumps(child_target)},'describe_policy',{{}}); "
                    "return 'ROW_START\\n'+('padding row for the stored log\\n'.repeat(20000))+'ROW_END\\n';",
                    "large:exec-result", max_output_bytes=4096)
                descriptor = big.get("result", {})
                assert descriptor.get("saved") is True, (descriptor, big)
                assert descriptor.get("source"), descriptor
                assert descriptor.get("indexed") is False, descriptor
                assert descriptor.get("complete") is True, descriptor
                for key in ("source", "bytes", "format", "complete", "indexed", "expires_at"):
                    assert key in descriptor, (key, descriptor)
                assert descriptor.get("read", {}).get("call"), descriptor
                assert descriptor.get("search", {}).get("call"), descriptor
                stored_source = descriptor["source"]

                before_status = client.exec(
                    f"return await mcp.content.status({json.dumps(stored_source)});",
                    "large:status-before-search").get("result", {})
                assert before_status.get("indexed") is False, before_status
                assert before_status.get("indexed_rows") == 0, before_status

                first_search = client.exec(
                    f"return await mcp.content.search('ROW_END',{{source:{json.dumps(stored_source)},limit:5}});",
                    "large:search-first")
                assert first_search.get("result", {}).get("count", 0) > 0, first_search
                after_status = client.exec(
                    f"return await mcp.content.status({json.dumps(stored_source)});",
                    "large:status-after-search").get("result", {})
                assert after_status.get("indexed") is True, after_status
                indexed_rows = after_status.get("indexed_rows", 0)
                assert indexed_rows > 0, after_status

                for attempt in range(2):
                    again = client.exec(
                        f"return await mcp.content.search('ROW_START',{{source:{json.dumps(stored_source)},limit:5}});",
                        f"large:search-repeat-{attempt + 1}")
                    assert again.get("result", {}).get("count", 0) > 0, again
                repeat_status = client.exec(
                    f"return await mcp.content.status({json.dumps(stored_source)});",
                    "large:status-after-repeat").get("result", {})
                assert repeat_status.get("indexed_rows") == indexed_rows, (repeat_status, indexed_rows)
                report["scenario"]["large_result_saved_then_indexed"] = {
                    "bytes": descriptor.get("bytes"),
                    "indexed_rows_after_first_search": indexed_rows,
                    "indexed_rows_after_repeated_search": repeat_status.get("indexed_rows"),
                    "index_rebuilt": repeat_status.get("indexed_rows") != indexed_rows,
                }

                # (7) A saved and searched result is not re-executed.
                stats_after_store = summary_result(client.exec(
                    f"return await call({json.dumps(child_target)},'fixture_stats',{{}});", "large:fixture-stats"))
                assert stats_after_store.get("large_log_calls") == 4, stats_after_store
                report["scenario"]["saved_result_is_not_re_executed"] = {
                    "fixture_large_log_calls": stats_after_store.get("large_log_calls"),
                }

                # (8) The shell spill variant: the confirmed full source is
                # indexed, and the command runs exactly once.
                counter_path = root / "shell-run-counter.txt"
                counter_path.write_text("", encoding="utf-8")
                spill_cmd = (
                    "sh -c 'echo RUN >> " + str(counter_path) + "; "
                    "echo RARE_SPILL_START marker=SPILL-EXACT; seq 1 200000; "
                    "echo RARE_SPILL_END marker=SPILL-END'"
                )
                spill_index = client.exec(
                    f"const s=await call({json.dumps(shell_target)},'shell_exec',{{cmd:{json.dumps(spill_cmd)}}},{{index:true,name:'spill-log'}}); return s;",
                    "spill:index")
                spill_source = spill_index.get("result", {}).get("source")
                assert spill_source, spill_index
                assert spill_index.get("result", {}).get("complete") is True, spill_index
                spill_hits = client.exec(
                    f"const s=await search('RARE_SPILL_END',{{source:{json.dumps(spill_source)},limit:2}}); "
                    f"const r=await read({json.dumps(spill_source)},s.hits[0].chunk_id); "
                    f"return {{count:s.count,line:r.text.split('\\n').find(x=>x.includes('RARE_SPILL_END'))}};",
                    "spill:search")
                spill_line = spill_hits.get("result", {}).get("line", "")
                assert "RARE_SPILL_END marker=SPILL-END" in spill_line, spill_hits
                runs = len([line for line in counter_path.read_text(encoding="utf-8").splitlines() if line.strip()])
                assert runs == 1, {"command_runs": runs}
                report["scenario"]["confirmed_full_source"] = {
                    "spilled_command_runs": runs,
                    "end_marker_line_found": spill_line,
                    "complete": spill_index.get("result", {}).get("complete"),
                }

                # (9) Right revocation: the right on the external source is
                # withdrawn, and the derived result stops being readable.
                child_ref = child_target.rsplit(":", 1)[-1]
                revoked = {"status": "skipped", "reason": "the child registration could not be withdrawn"}
                # The direct execute facade is not public, so the business call
                # runs inside the public executor.
                disabled = client.exec(
                    "return await mcp.call(" + json.dumps(f"control:{shell_name}") + ",'mcp_manage',"
                    "{action:'disable',ref:" + json.dumps(child_ref) + "});",
                    "revocation:disable")
                inner = disabled.get("result") if isinstance(disabled.get("result"), dict) else {}
                disabled_ok = (
                    first_string(disabled, "status") == "completed"
                    and (inner.get("ok") is True or first_string(inner, "status") in ("completed", "ok"))
                )
                if disabled_ok:
                    revoked_read = ro_body(client.ro(
                        f"return await mcp.content.read({json.dumps(stored_source)},1);", "revocation:read"))
                    revoked_search = ro_body(client.ro(
                        f"return await mcp.content.search('ROW_END',{{source:{json.dumps(stored_source)}}});", "revocation:search"))
                    assert revoked_read.get("status") != "completed", revoked_read
                    assert revoked_search.get("status") != "completed", revoked_search
                    revoked = {"read": "PASS denied", "search": "PASS denied"}
                report["scenario"]["right_revocation"] = revoked

                legacy_code = client.call("code_exec", {"code": "return 1;"}, "migration:code_exec")
                assert legacy_code.get("status") == "failed" and "renamed to exec" in legacy_code.get("error", ""), legacy_code
                # The direct execute facade is not public any more. A client that
                # still calls it gets the explicit migration answer instead of a
                # silent fallback, and the public surface stays search/ro/exec.
                retired_args = {"target": "hub", "tool": "settings_schema", "args": {}}
                if client.context:
                    retired_args["context"] = client.context
                retired = client.call("execute", retired_args, "public:execute-retired")
                assert retired.get("status") == "failed", retired
                assert first_string(retired.get("error"), "code") == "execute_facade_retired", retired
                # The task read facade stays reachable, because the public catalog
                # publishes it.
                assert "job" in public_names, public_names
                ro = client.call("ro", {"code": "try { await mcp.call('" + shell_target + "','shell_exec',{cmd:'echo denied'}); } catch(e) {} return 42;"}, "readonly:shell-denial")
                assert ro.get("status") == "failed", ro
                report["scenario"]["compatibility"] = {"code_exec": "PASS migration alias", "execute": "PASS refused with migration answer", "job": "PASS published task reader", "ro_shell_denial": "PASS"}

                report["status"] = "PASS"
                report["scope"] = "isolated real-process candidate; not deployed and not ChatGPT app acceptance"
                return report
            finally:
                if root_process is not None and root_process.poll() is None:
                    root_process.terminate()
                    try:
                        root_process.wait(timeout=8)
                    except subprocess.TimeoutExpired:
                        root_process.kill()
                        root_process.wait(timeout=5)
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=12)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=5)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("candidate", type=Path)
    parser.add_argument("fixture", type=Path)
    parser.add_argument("root_fixture", type=Path)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    result = run(args.candidate.resolve(), args.fixture.resolve(), args.root_fixture.resolve())
    encoded = json.dumps(result, ensure_ascii=False, indent=2)
    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(encoded + "\n", encoding="utf-8")
    print(encoded)
    if result.get("status") != "PASS":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
