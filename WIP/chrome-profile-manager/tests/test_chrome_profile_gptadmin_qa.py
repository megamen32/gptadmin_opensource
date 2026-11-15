"""Opt-in real Chrome/GPTAdmin QA. No user profiles or page inspection."""
import json
import os
import queue
from pathlib import Path
import re
import select
import subprocess
import sys
import threading
import time

import pytest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))


def test_real_gptadmin_chrome_bridge(tmp_path, monkeypatch):
    if os.environ.get("GPTADMIN_CHROME_QA") != "1":
        pytest.skip("explicit isolated Chrome QA opt-in required")
    from chrome_profile_manager_mcp import Backend, ProfileManager, bridge_call
    from chrome_profile_manager_bridge.provision import provision

    binary = Path(os.environ.get("GPTADMIN_QA_BINARY", "/opt/gptadmin/bin/shellmcp"))
    assert binary.is_file()
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "config"))
    monkeypatch.setenv("CHROME_PROFILE_MANAGER_STATE", str(tmp_path / "state"))
    chrome_root = tmp_path / "chrome"
    chrome_root.mkdir()
    artifact = provision("default", chrome_root)
    qa = tmp_path / "qa-extension"
    qa.mkdir()
    (qa / "manifest.json").write_text(json.dumps({"manifest_version": 3, "name": "Isolated GPTAdmin QA", "version": "1.0"}))
    # Chrome selects an available loopback port for this isolated profile.
    entry = Path(os.environ["CHROME_DEVTOOLS_MCP_ENTRY"]).resolve()
    package = json.loads((entry.parents[3] / "package.json").read_text())
    assert package.get("name") == "chrome-devtools-mcp" and package.get("version") == "1.12.0"
    backend = Backend.__new__(Backend)
    backend.proc = subprocess.Popen([
        "node", str(entry), "--headless", "--categoryExtensions",
        "--experimentalStructuredContent", "--no-usage-statistics",
        "--userDataDir=" + str(chrome_root), "--filesystemRoot=" + str(tmp_path),
        "--chromeArg=--remote-debugging-port=0",
    ], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
       text=True, bufsize=1)
    backend.messages = queue.Queue()
    backend.seq = 0
    def reader():
        for line in backend.proc.stdout:
            backend.messages.put(line)
        backend.messages.put(None)
    threading.Thread(target=reader, daemon=True).start()
    proc = None
    try:
        backend.call("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "qa", "version": "1"}})
        backend.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        def install(path):
            result = backend.tool("install_extension", {"path": str(path)})
            match = re.search(r"Extension installed\. Id: ([a-p]{32})", result)
            assert match, "Chrome extension installation failed"
            return match.group(1)
        install(artifact)
        qa_id = install(qa)
        deadline = time.monotonic() + 15
        while True:
            try:
                if any(x["id"] == qa_id for x in bridge_call("default", "list")):
                    break
            except Exception:
                pass
            if time.monotonic() >= deadline:
                pytest.fail("QA bridge native connection did not become ready")
            time.sleep(.1)

        probe = ProfileManager(state_dir=tmp_path / 'probe', roots=[chrome_root])
        deadline = time.monotonic() + 15
        while not any(p['profile_id'] == 'default' for p in probe.profile_list()):
            if time.monotonic() >= deadline: pytest.fail('Chrome did not persist QA profile metadata')
            time.sleep(.05)
        probe.close()
        env = os.environ.copy()
        env.update(CHROME_PROFILE_ROOTS=str(chrome_root), SHELLMCP_MCP_CONFIG=str(tmp_path / "mcp.json"), SHELLMCP_SPOOL_DIR=str(tmp_path / "spool"), SHELLMCP_IDENTITY_DIR=str(tmp_path / "identity"), SHELLMCP_DEFAULT_HOME=str(tmp_path), SHELLMCP_DEFAULT_CWD=str(tmp_path), SHELLMCP_HEARTBEAT="0", SHELLMCP_QUEUE="0", HUB_URL="")
        proc = subprocess.Popen([str(binary), "--mcp-stdio"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, bufsize=1, env=env)
        sequence = 0
        def rpc(method, params):
            nonlocal sequence
            sequence += 1
            proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": sequence, "method": method, "params": params}) + "\n")
            proc.stdin.flush()
            assert select.select([proc.stdout], [], [], 30)[0], "GPTAdmin response timeout"
            result = json.loads(proc.stdout.readline())
            assert "error" not in result, result.get("error")
            return result["result"]
        def call(tool, args):
            result = rpc("tools/call", {"name": "mcp_call", "arguments": {"ref": "qa-chrome", "name": tool, "arguments": args}})
            assert not result.get("isError"), result
            child = result["structuredContent"]["result"]
            assert not child.get("isError"), child
            return child["structuredContent"]
        rpc("initialize", {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "qa", "version": "1"}})
        config = {"ref": "qa-chrome", "transport": "stdio", "command": sys.executable, "args": [str(ROOT / "chrome_profile_manager_mcp.py")], "enabled": True}
        managed = rpc("tools/call", {"name": "mcp_manage", "arguments": {"action": "upsert", "config": config}})
        assert not managed.get("isError"), managed
        listed = rpc("tools/call", {"name": "mcp_tools", "arguments": {"ref": "qa-chrome"}})
        assert {"profile_extensions_list", "profile_extension_set_enabled"} <= {x["name"] for x in listed["structuredContent"]["tools"]}
        entries = call("profile_extensions_list", {"profile_id": "default"})["extensions"]
        assert any(x["id"] == qa_id and x["enabled"] for x in entries)
        for enabled in (False, True):
            result = call("profile_extension_set_enabled", {"profile_id": "default", "extension_id": qa_id, "enabled": enabled})
            assert result["extension"]["enabled"] is enabled
            # Ask Chrome directly, bypassing Puppeteer's extension cache.
            import websocket
            port, browser_path = (chrome_root / "DevToolsActivePort").read_text().splitlines()[:2]
            assert port.isdigit() and browser_path.startswith("/devtools/browser/")
            ws = websocket.create_connection("ws://127.0.0.1:" + port + browser_path, timeout=10, suppress_origin=True)
            try:
                ws.send(json.dumps({"id": 1, "method": "Extensions.getExtensions"}))
                while True:
                    raw = json.loads(ws.recv())
                    if raw.get("id") == 1:
                        break
            finally:
                ws.close()
            assert "error" not in raw, raw.get("error")
            matches = [item for item in raw["result"]["extensions"] if item.get("id") == qa_id]
            assert len(matches) == 1
            print("CDP_RAW_QA_EXTENSION " + json.dumps(matches[0], sort_keys=True))
            assert matches[0]["enabled"] is enabled
            assert matches[0]["enabled"] == result["extension"]["enabled"]
    finally:
        if proc is not None and proc.poll() is None:
            proc.terminate()
            try: proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill(); proc.wait(timeout=5)
        # This test constructs the disposable process directly, without Backend's staging.
        if backend.proc.poll() is None:
            backend.proc.terminate()
            try: backend.proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                backend.proc.kill(); backend.proc.wait(timeout=5)
