import ast
import re
import subprocess
import urllib.error
from pathlib import Path
from types import SimpleNamespace

import cli


def _launchd_status_fixture(output, result=0):
    # Exercise the production macOS-only function on Linux CI as well.
    tree = ast.parse(Path(cli.__file__).read_text())
    node = next(n for n in ast.walk(tree)
                if isinstance(n, ast.FunctionDef) and n.name == "_launchd_job_status")
    namespace = dict(re=re, subprocess=subprocess,
                     _launchd_domains=lambda: ["system"],
                     _launchd_service_target=lambda label, domain: domain + "/" + label,
                     _launchctl_capture=lambda args: subprocess.CompletedProcess(args, result, stdout=output))
    exec(compile(ast.Module(body=[node], type_ignores=[]), "<launchd>", "exec"), namespace)
    return namespace["_launchd_job_status"]


def test_launchd_running_pid():
    assert _launchd_status_fixture("system/job = {\n\tstate = running\n\tpid = 28771\n\tendpoint = {\n\t\tstate = active\n\t}\n}")( "job") == ("running", "28771")


def test_launchd_idle_ignores_nested_pid():
    assert _launchd_status_fixture("system/job = {\n\tstate = not running\n\tendpoint = {\n\t\tstate = active\n\t\tpid = 88\n\t}\n}")( "job") == ("loaded (idle)", "0")


def test_launchd_unloaded():
    assert _launchd_status_fixture("", result=1)("job") == ("not loaded", "0")


def test_hub_http_health(monkeypatch):
    class Response:
        status = 200
        def __enter__(self):
            return self
        def __exit__(self, *args):
            return False
        def read(self, size):
            return b'{"ok":true}'

    def probe(request, timeout):
        assert request.full_url == "https://example.com/healthz"
        assert not request.has_header("Authorization")
        return Response()
    monkeypatch.setattr(cli.urllib.request, "urlopen", probe)
    state, latency, detail = cli._probe_hub_status("https://example.com")
    assert state == "reachable" and latency >= 0 and detail == "HTTP 200"


def test_hub_http_failure(monkeypatch):
    def probe(request, timeout):
        raise urllib.error.HTTPError(request.full_url, 503, "unavailable", None, None)
    monkeypatch.setattr(cli.urllib.request, "urlopen", probe)
    state, latency, detail = cli._probe_hub_status("https://example.com")
    assert state == "HTTP error" and latency >= 0 and detail == "HTTP 503"


def test_config_updates_derived_manifest_and_preserves_custom(monkeypatch, tmp_path):
    monkeypatch.setattr(cli, "need_root", lambda: None)
    monkeypatch.setattr(cli, "UNIT_PATH_SHELLMCP", tmp_path / "not-installed")
    monkeypatch.setattr(cli, "env_set_many", lambda env: None)
    for manifest, expected in [
        ("https://old.example/artifacts/shellmcp.json", "https://new.example/artifacts/shellmcp.json"),
        ("https://mirror.example/custom.json", "https://mirror.example/custom.json"),
        ("", "https://new.example/artifacts/shellmcp.json"),
    ]:
        env = {"HUB_URL": "https://old.example", "SHELLMCP_UPDATE_MANIFEST_URL": manifest}
        monkeypatch.setattr(cli, "env_read", lambda: env)
        cli.cmd_config_shellmcp(SimpleNamespace(hub_url="https://new.example", transport="polling", heartbeat=False))
        assert env["SHELLMCP_UPDATE_MANIFEST_URL"] == expected
        assert env["QUEUE_URL"] == "https://new.example/queue"
