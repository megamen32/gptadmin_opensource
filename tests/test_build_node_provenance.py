import importlib.util
import json
from pathlib import Path
import subprocess
from types import SimpleNamespace

import pytest

ROOT = Path(__file__).resolve().parents[1]
MODULE_PATH = ROOT / "tools/build_node.py"
spec = importlib.util.spec_from_file_location("build_node", MODULE_PATH)
build_node = importlib.util.module_from_spec(spec)
spec.loader.exec_module(build_node)


def test_go_metadata_requires_exact_command_target_and_no_ambient_vcs(monkeypatch, tmp_path):
    binary = tmp_path / "node"
    binary.write_bytes(b"binary")
    good = """node: go1.26.4
\tpath\tgithub.com/megamen32/gptadmin/go-hub/cmd/gptadmin-node
\tbuild\tGOOS=linux
\tbuild\tGOARCH=amd64
"""
    monkeypatch.setattr(build_node.subprocess, "run", lambda *a, **k: SimpleNamespace(
        returncode=0, stdout=good, stderr=""))
    receipt = build_node.verify_go_binary_metadata(
        binary,
        command_path="github.com/megamen32/gptadmin/go-hub/cmd/gptadmin-node",
        goos="linux", goarch="amd64")
    assert receipt == {"command": "github.com/megamen32/gptadmin/go-hub/cmd/gptadmin-node",
                       "goos": "linux", "goarch": "amd64", "buildvcs": "disabled"}

    ambient = good + "\tbuild\tvcs.revision=deadbeef\n\tbuild\tvcs.modified=true\n"
    monkeypatch.setattr(build_node.subprocess, "run", lambda *a, **k: SimpleNamespace(
        returncode=0, stdout=ambient, stderr=""))
    with pytest.raises(RuntimeError, match="ambient VCS metadata"):
        build_node.verify_go_binary_metadata(
            binary,
            command_path="github.com/megamen32/gptadmin/go-hub/cmd/gptadmin-node",
            goos="linux", goarch="amd64")


def fake_node(path: Path, commit: str, version: int):
    path.write_text(f'''#!/usr/bin/env python3
import json, os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
class H(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def send(self, status, value):
        raw=(json.dumps(value)+"\\n").encode(); self.send_response(status)
        self.send_header("Content-Type","application/json"); self.send_header("Content-Length",str(len(raw)))
        self.end_headers(); self.wfile.write(raw)
    def do_GET(self):
        if self.path == "/healthz": return self.send(200,{{"ok":True}})
        if self.path == "/version": return self.send(200,{{"ok":True,"git_commit":"{commit}","build_version":"{version}"}})
        if self.path == "/admin/api/node/handoff": return self.send(412,{{"detail":"node handoff is unavailable without an activated listener"}})
        self.send(404,{{"error":"missing"}})
ThreadingHTTPServer((os.environ["GPTADMIN_HUB_HOST"],int(os.environ["GPTADMIN_HUB_PORT"])),H).serve_forever()
''')
    path.chmod(0o755)


def test_native_probe_binds_runtime_version_and_handoff(tmp_path, monkeypatch):
    monkeypatch.setattr(build_node, "ROOT", tmp_path)
    commit = "a" * 40
    binary = tmp_path / "fake-node"
    fake_node(binary, commit, 1234)
    receipt = build_node.probe_node_binary(binary, commit=commit, version=1234)
    assert receipt == {"git_commit": commit, "build_version": "1234", "handoff_capable": True}


def test_native_probe_rejects_manifest_runtime_mismatch(tmp_path, monkeypatch):
    monkeypatch.setattr(build_node, "ROOT", tmp_path)
    binary = tmp_path / "fake-node"
    fake_node(binary, "b" * 40, 1234)
    with pytest.raises(RuntimeError, match="expected commit"):
        build_node.probe_node_binary(binary, commit="a" * 40, version=1234)


def test_builder_disables_ambient_vcs_stamping():
    source = MODULE_PATH.read_text()
    assert source.count("'-buildvcs=false'") >= 2
    assert "probe_node_binary(binary" in source
