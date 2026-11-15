import importlib.util
import io
from pathlib import Path
import pytest
import json
import os
import re
import secrets
import shutil
import socket
import time
import base64
import hashlib
import subprocess

PATH = Path(__file__).resolve().parents[1] / "chrome_profile_manager_bridge" / "host.py"
spec = importlib.util.spec_from_file_location("profile_bridge_host", PATH)
host = importlib.util.module_from_spec(spec)
spec.loader.exec_module(host)

def test_frame_round_trip_and_limit():
    stream = io.BytesIO()
    host.send(stream, {"method": "list"})
    stream.seek(0)
    assert host.receive(stream) == {"method": "list"}
    with pytest.raises(ValueError):
        host.receive(io.BytesIO((host.MAX + 1).to_bytes(4, __import__("sys").byteorder)))

def test_profile_capability_validation():
    assert host.socket_path("a" * 64) != host.socket_path("b" * 64)
    with pytest.raises(ValueError):
        host.socket_path("../wrong")

def test_duplicate_origin_cannot_take_active_endpoint(tmp_path):
    origin = "chrome-extension://" + "a" * 32 + "/"
    secret = secrets.token_hex(32)
    config = tmp_path / "gptadmin-extension-bridge/profiles.json"
    config.parent.mkdir(mode=0o700)
    config.write_text(json.dumps({origin: secret}))
    config.chmod(0o600)
    env = dict(os.environ, XDG_CONFIG_HOME=str(tmp_path))
    first = subprocess.Popen([str(PATH), origin], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env)
    second = None
    try:
        host.send(first.stdin, {"type": "auto"})
        deadline = time.monotonic() + 3
        while not os.path.exists(host.socket_path(secret)) and time.monotonic() < deadline: time.sleep(0.01)
        assert os.path.exists(host.socket_path(secret))
        second = subprocess.Popen([str(PATH), origin], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env)
        host.send(second.stdin, {"type": "auto"})
        assert second.wait(timeout=3) != 0
        assert first.poll() is None
        assert os.path.exists(host.socket_path(secret))
    finally:
        if second and second.poll() is None: second.terminate(); second.wait(timeout=3)
        first.stdin.close()
        try: first.wait(timeout=3)
        except subprocess.TimeoutExpired: first.terminate(); first.wait(timeout=3)

def test_real_isolated_chrome_bridge(tmp_path, monkeypatch):
    if not os.environ.get("CHROME_BRIDGE_REAL_TEST"):
        pytest.skip("requires isolated Chrome and chrome-devtools-mcp 1.12")
    import sys
    sys.path.insert(0, str(PATH.parents[1]))
    from chrome_profile_manager_mcp import Backend, ProfileManagerError, bridge_call
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "config"))
    bridge = tmp_path / "bridge"
    shutil.copytree(PATH.parent / "extension", bridge)
    qa = tmp_path / "qa-extension"
    qa.mkdir()
    (qa / "manifest.json").write_text(json.dumps({"manifest_version": 3, "name": "Bridge QA extension", "version": "1.0"}))
    backend = Backend(tmp_path)
    try:
        def install(path):
            output = backend.tool("install_extension", {"path": str(path)})
            match = re.search(r"Extension installed\. Id: ([a-p]{32})", output)
            assert match, output
            return match.group(1)
        bridge_id = install(bridge)
        qa_id = install(qa)
        native = tmp_path / "chrome/NativeMessagingHosts"
        native.mkdir(parents=True, exist_ok=True)
        (native / "com.gptadmin.extension_bridge.json").write_text(json.dumps({
            "name": "com.gptadmin.extension_bridge", "description": "isolated QA",
            "path": str(PATH), "type": "stdio",
            "allowed_origins": ["chrome-extension://" + bridge_id + "/"]}))
        secret = secrets.token_hex(32)
        registry = tmp_path / "config/gptadmin-extension-bridge"
        registry.mkdir(parents=True, mode=0o700)
        for name, contents in (("profiles.json", {"chrome-extension://" + bridge_id + "/": secret}), ("bindings.json", {"qa-profile": "chrome-extension://" + bridge_id + "/"})):
            path = registry / name
            path.write_text(json.dumps(contents))
            path.chmod(0o600)
        # Wake the extension from its own page without inspecting page contents.
        page = backend.tool("new_page", {"url": "chrome-extension://" + bridge_id + "/popup.html"})
        match = re.search(r"(?m)^(\d+): .*chrome-extension://" + bridge_id, page)
        assert match, page
        page_id = int(match.group(1))
        backend.tool("evaluate_script", {"pageId": page_id, "function": "async () => chrome.runtime.sendMessage({type:'connect', secret:" + json.dumps(secret) + "})"})
        endpoint = host.socket_path(secret)
        deadline = time.monotonic() + 10
        while not os.path.exists(endpoint) and time.monotonic() < deadline:
            time.sleep(0.05)
        if not os.path.exists(endpoint):
            status = backend.tool("evaluate_script", {"pageId": page_id, "function": "async () => chrome.runtime.sendMessage({type:'status'})"})
            pytest.fail("native host did not create endpoint: " + status)
        def request(method):
            with socket.socket(socket.AF_UNIX) as conn:
                conn.settimeout(20)
                conn.connect(endpoint)
                with conn.makefile("rwb") as stream:
                    host.send(stream, {"secret": secret, "method": method, "extension_id": qa_id})
                    result = host.receive(stream)
                    assert "error" not in result, result
                    return result["result"]
        assert any(item["id"] == qa_id and item["enabled"] for item in request("list"))
        assert request("get")["enabled"] is True
        assert request("disable")["enabled"] is False
        assert request("get")["enabled"] is False
        disabled = backend.tool("list_extensions", {})
        assert qa_id in disabled and "disabled" in disabled.lower(), disabled
        assert request("enable")["enabled"] is True
        assert request("get")["enabled"] is True
        enabled = backend.tool("list_extensions", {})
        assert qa_id in enabled and "enabled" in enabled.lower(), enabled
        assert any(item["id"] == qa_id for item in bridge_call("qa-profile", "list"))
        assert bridge_call("qa-profile", "disable", qa_id)["enabled"] is False
        assert bridge_call("qa-profile", "get", qa_id)["enabled"] is False
        assert bridge_call("qa-profile", "enable", qa_id)["enabled"] is True
        assert bridge_call("qa-profile", "get", qa_id)["enabled"] is True
        # Disabling and enabling is not a reload: Chrome keeps the old manifest.
        (qa / "manifest.json").write_text(json.dumps({"manifest_version": 3, "name": "Bridge QA extension", "version": "1.1"}))
        assert bridge_call("qa-profile", "disable", qa_id)["enabled"] is False
        assert bridge_call("qa-profile", "enable", qa_id)["version"] == "1.0"
        # Exercise the actual stdio MCP executable against the same real Chrome.
        messages = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18"}},
            {"jsonrpc": "2.0", "method": "notifications/initialized"},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "profile_extension_set_enabled", "arguments": {"profile_id": "qa-profile", "extension_id": qa_id, "enabled": False}}},
            {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "profile_extension_get", "arguments": {"profile_id": "qa-profile", "extension_id": qa_id}}},
            {"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {"name": "profile_extension_set_enabled", "arguments": {"profile_id": "qa-profile", "extension_id": qa_id, "enabled": True}}},
        ]
        proc = subprocess.run([sys.executable, str(PATH.parents[1] / "chrome_profile_manager_mcp.py")],
                              input="".join(json.dumps(m) + "\n" for m in messages),
                              text=True, capture_output=True, timeout=60, check=True)
        responses = {m["id"]: m for m in map(json.loads, proc.stdout.splitlines())}
        assert responses[2]["result"]["structuredContent"]["extension"]["enabled"] is False
        assert responses[3]["result"]["structuredContent"]["extension"]["enabled"] is False
        assert responses[4]["result"]["structuredContent"]["extension"]["enabled"] is True
        assert bridge_call("qa-profile", "get", qa_id)["enabled"] is True
        with pytest.raises(ProfileManagerError, match="cannot disable its own bridge"):
            bridge_call("qa-profile", "disable", bridge_id)
        with socket.socket(socket.AF_UNIX) as conn:
            conn.settimeout(20)
            conn.connect(endpoint)
            with conn.makefile("rwb") as stream:
                host.send(stream, {"secret": secret, "method": "disable", "extension_id": bridge_id})
                assert "Cannot disable" in host.receive(stream)["error"]
        assert bridge_call("qa-profile", "get", bridge_id)["enabled"] is True
    finally:
        backend.close()

def test_real_provision_and_auto_connect(tmp_path, monkeypatch):
    if not os.environ.get("CHROME_BRIDGE_REAL_TEST"):
        pytest.skip("requires isolated Chrome and chrome-devtools-mcp 1.12")
    import sys
    sys.path.insert(0, str(PATH.parents[1]))
    from chrome_profile_manager_mcp import Backend, bridge_call
    from chrome_profile_manager_bridge import provision as bridge_provision
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "config"))
    chrome_root = tmp_path / "chrome"
    chrome_root.mkdir()
    pid_file = tmp_path / "qa-host.pid"
    qa_host = tmp_path / "qa-host.py"
    host_source = PATH.read_text()
    host_source = host_source.replace("import os\n", "import os\n" + f"open({str(pid_file)!r}, 'w').write(str(os.getpid()))\n", 1)
    qa_host.write_text(host_source)
    qa_host.chmod(0o700)
    monkeypatch.setattr(bridge_provision, "HOST", qa_host)
    artifact = bridge_provision.provision("QA Profile", chrome_root)
    qa = tmp_path / "qa-extension"
    qa.mkdir()
    (qa / "manifest.json").write_text(json.dumps({"manifest_version": 3, "name": "Bridge QA provision", "version": "1.0"}))
    backend = Backend(tmp_path)
    try:
        # The backend's allowed filesystem root must include the provisioned artifact.
        out = backend.tool("install_extension", {"path": str(artifact)})
        bridge_id = re.search(r"Extension installed\. Id: ([a-p]{32})", out).group(1)
        out = backend.tool("install_extension", {"path": str(qa)})
        qa_id = re.search(r"Extension installed\. Id: ([a-p]{32})", out).group(1)
        deadline = time.monotonic() + 10
        while True:
            try:
                result = bridge_call("QA Profile", "list")
                break
            except Exception:
                if time.monotonic() >= deadline: raise
                time.sleep(0.05)
        assert any(item["id"] == bridge_id for item in result)
        assert bridge_call("QA Profile", "disable", qa_id)["enabled"] is False
        assert bridge_call("QA Profile", "enable", qa_id)["enabled"] is True
        # Stop only the native host belonging to this temporary Chrome profile.
        old_pid = int(pid_file.read_text())
        os.kill(old_pid, __import__("signal").SIGTERM)
        deadline = time.monotonic() + 8
        while True:
            try:
                if int(pid_file.read_text()) != old_pid:
                    result = bridge_call("QA Profile", "get", qa_id)
                    assert result["enabled"] is True
                    break
            except (OSError, ValueError, RuntimeError):
                pass
            if time.monotonic() >= deadline:
                pytest.fail("native host did not reconnect after termination")
            time.sleep(0.05)
    finally:
        backend.close()


def test_real_native_host_failure_retry_is_bounded(tmp_path, monkeypatch):
    if not os.environ.get("CHROME_BRIDGE_REAL_TEST"):
        pytest.skip("requires isolated Chrome and chrome-devtools-mcp 1.12")
    import sys
    sys.path.insert(0, str(PATH.parents[1]))
    from chrome_profile_manager_mcp import Backend
    from chrome_profile_manager_bridge import provision as bridge_provision
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "config"))
    chrome_root = tmp_path / "chrome"
    chrome_root.mkdir()
    launches = tmp_path / "launches"
    failing_host = tmp_path / "failing-host.py"
    failing_host.write_text("#!/usr/bin/env python3\n" + f"open({str(launches)!r}, 'a').write('x')\n")
    failing_host.chmod(0o700)
    monkeypatch.setattr(bridge_provision, "HOST", failing_host)
    artifact = bridge_provision.provision("QA Profile", chrome_root)
    backend = Backend(tmp_path)
    try:
        backend.tool("install_extension", {"path": str(artifact)})
        deadline = time.monotonic() + 6
        while (not launches.exists() or len(launches.read_text()) < 2) and time.monotonic() < deadline:
            time.sleep(0.05)
        assert launches.read_text() == "xx"
        time.sleep(2)
        assert launches.read_text() == "xx"
    finally:
        backend.close()


def test_real_autoconnect_after_restart(tmp_path, monkeypatch):
    if not os.environ.get("CHROME_BRIDGE_REAL_TEST"):
        pytest.skip("requires isolated Chrome and chrome-devtools-mcp 1.12")
    import sys
    sys.path.insert(0, str(PATH.parents[1]))
    from chrome_profile_manager_mcp import Backend
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "config"))
    private, public = tmp_path / "key.pem", tmp_path / "public.der"
    subprocess.run(["openssl", "genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-out", str(private)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(["openssl", "pkey", "-in", str(private), "-pubout", "-outform", "DER", "-out", str(public)], check=True)
    raw_key = public.read_bytes()
    bridge_id = "".join(chr(97 + int(c, 16)) for c in hashlib.sha256(raw_key).hexdigest()[:32])
    bridge = tmp_path / "bridge"
    shutil.copytree(PATH.parent / "extension", bridge)
    manifest = json.loads((bridge / "manifest.json").read_text())
    manifest["key"] = base64.b64encode(raw_key).decode()
    (bridge / "manifest.json").write_text(json.dumps(manifest))
    (bridge / "config.js").write_text("const AUTO_CONNECT = true;\n")
    secret = secrets.token_hex(32)
    registry = tmp_path / "config/gptadmin-extension-bridge/profiles.json"
    registry.parent.mkdir(parents=True, mode=0o700)
    registry.write_text(json.dumps({"chrome-extension://" + bridge_id + "/": secret}))
    registry.chmod(0o600)
    native = tmp_path / "chrome/NativeMessagingHosts"
    native.mkdir(parents=True)
    (native / "com.gptadmin.extension_bridge.json").write_text(json.dumps({"name": "com.gptadmin.extension_bridge", "description": "QA", "path": str(PATH), "type": "stdio", "allowed_origins": ["chrome-extension://" + bridge_id + "/"]}))
    endpoint = host.socket_path(secret)
    def wait_and_list():
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            try:
                with socket.socket(socket.AF_UNIX) as conn:
                    conn.settimeout(2)
                    conn.connect(endpoint)
                    with conn.makefile("rwb") as stream:
                        host.send(stream, {"secret": secret, "method": "list"})
                        result = host.receive(stream)
                        assert any(x["id"] == bridge_id for x in result["result"])
                        return
            except (OSError, EOFError): time.sleep(0.05)
        pytest.fail("automatic connection did not become usable")
    backend = Backend(tmp_path)
    try:
        output = backend.tool("install_extension", {"path": str(bridge)})
        assert bridge_id in output, output
        wait_and_list()
    finally: backend.close()
    backend = Backend(tmp_path)
    try:
        backend.tool("list_pages", {})  # launch Chrome
        after_restart = backend.tool("list_extensions", {})
        if bridge_id not in after_restart:
            pytest.xfail("Chrome did not retain the DevTools-loaded unpacked extension across restart")
        wait_and_list()
    finally: backend.close()
