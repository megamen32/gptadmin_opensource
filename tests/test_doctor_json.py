import json
import socket
import subprocess
from datetime import datetime, timezone
from argparse import Namespace

import cli


def test_doctor_json_is_machine_readable_and_secret_free(monkeypatch, capsys, tmp_path):
    unit = tmp_path / "gptadmin-hub.service"
    unit.write_text("unit", encoding="utf-8")
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.bind(("127.0.0.1", 0))
    listener.listen(1)
    env_file = tmp_path / "gptadmin.env"
    env_file.write_text("ADMIN_PASSWORD=must-not-appear\n", encoding="utf-8")
    env_file.chmod(0o600)

    class RemoteHealth:
        status = 200
        headers = {"Date": datetime.now(timezone.utc).strftime("%a, %d %b %Y %H:%M:%S GMT")}

        def __enter__(self):
            return self

        def __exit__(self, *_args):
            return False

        def read(self):
            return b'{"ok":true,"build_version":"128","git_commit":"abc1234"}'

    port = listener.getsockname()[1]
    monkeypatch.setattr(cli, "installed_units", lambda: [("Hub", unit)])
    monkeypatch.setattr(cli, "ENV_FILE", env_file)
    monkeypatch.setattr(cli.urllib.request, "urlopen", lambda *_args, **_kwargs: RemoteHealth())
    def fake_run(command, **_kwargs):
        if "is-active" in command:
            return subprocess.CompletedProcess(command, 0, stdout="active\n", stderr="")
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(cli, "run", fake_run)
    monkeypatch.setattr(
        cli,
        "env_read",
        lambda: {
            "ADMIN_PASSWORD": "must-not-appear",
            "HUB_URL": "https://hub.example",
            "HUB_PORT": str(port),
            "CTL_TOKEN": "legacy-token-must-not-appear",
        },
    )

    cli.cmd_doctor(Namespace(json=True))
    listener.close()

    report = json.loads(capsys.readouterr().out)
    assert report["ok"] is True
    assert report["issues"] == 0
    assert report["hub_url"] == "https://hub.example"
    assert {check["name"] for check in report["checks"]} >= {
        "version",
        "remote_health",
        "remote_clock",
        "env_permissions",
        "service_runtime:Hub",
    }
    assert "must-not-appear" not in json.dumps(report)
    assert "legacy-token-must-not-appear" not in json.dumps(report)


def test_doctor_json_reports_missing_password(monkeypatch, capsys):
    monkeypatch.setattr(cli, "installed_units", lambda: [])
    monkeypatch.setattr(cli, "env_read", lambda: {"HUB_PORT": "1"})

    cli.cmd_doctor(Namespace(json=True))

    report = json.loads(capsys.readouterr().out)
    assert report["ok"] is False
    assert report["issues"] >= 2
    assert any(check["name"] == "admin_password" and check["status"] == "error" for check in report["checks"])


def test_doctor_json_marks_failed_service_runtime_as_issue(monkeypatch, capsys, tmp_path):
    """A present unit must not count as healthy when its service has failed."""
    unit = tmp_path / "gptadmin-hub.service"
    unit.write_text("unit", encoding="utf-8")
    env_file = tmp_path / "gptadmin.env"
    env_file.write_text("ADMIN_PASSWORD=hidden\n", encoding="utf-8")
    env_file.chmod(0o600)

    def failed_run(command, **_kwargs):
        if "is-active" in command:
            return subprocess.CompletedProcess(command, 3, stdout="failed\n", stderr="")
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(cli, "installed_units", lambda: [("Hub", unit)])
    monkeypatch.setattr(cli, "ENV_FILE", env_file)
    monkeypatch.setattr(cli, "run", failed_run)
    monkeypatch.setattr(cli, "env_read", lambda: {"ADMIN_PASSWORD": "hidden", "HUB_PORT": "1"})

    cli.cmd_doctor(Namespace(json=True))
    report = json.loads(capsys.readouterr().out)
    assert report["ok"] is False
    assert any(check["name"] == "service_runtime:Hub" and check["status"] == "error" for check in report["checks"])


def test_doctor_reports_legacy_shellmcp_binary_in_canonical_unit(monkeypatch, capsys, tmp_path):
    """An active legacy rootd unit must not look like the supported ShellMCP runtime."""
    unit = tmp_path / "shellmcp.service"
    unit.write_text("[Service]\nExecStart=/opt/gptadmin/bin/rootd-go\n", encoding="utf-8")
    env_file = tmp_path / "gptadmin.env"
    env_file.write_text("ADMIN_PASSWORD=hidden\n", encoding="utf-8")
    env_file.chmod(0o600)

    def active_run(command, **_kwargs):
        if "is-active" in command:
            return subprocess.CompletedProcess(command, 0, stdout="active\n", stderr="")
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(cli, "installed_units", lambda: [(cli.SYSTEMD_SHELLMCP, unit)])
    monkeypatch.setattr(cli, "UNIT_PATH_SHELLMCP", unit)
    monkeypatch.setattr(cli, "ENV_FILE", env_file)
    monkeypatch.setattr(cli, "run", active_run)
    monkeypatch.setattr(cli, "env_read", lambda: {"ADMIN_PASSWORD": "hidden", "HUB_PORT": "1"})

    cli.cmd_doctor(Namespace(json=True))

    report = json.loads(capsys.readouterr().out)
    assert report["ok"] is False
    assert any(
        check["name"] == "shellmcp_unit" and check["status"] == "error" and "legacy" in check["message"]
        for check in report["checks"]
    )


def test_doctor_rejects_tcp_listener_without_hub_health(monkeypatch, capsys):
    """A stale process on the configured Hub port must not count as a healthy Hub."""
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.bind(("127.0.0.1", 0))
    listener.listen(1)
    port = listener.getsockname()[1]

    monkeypatch.setattr(cli, "installed_units", lambda: [])
    monkeypatch.setattr(cli, "env_read", lambda: {
        "ADMIN_PASSWORD": "hidden",
        "HUB_HOST": "127.0.0.1",
        "HUB_PORT": str(port),
    })

    cli.cmd_doctor(Namespace(json=True))
    listener.close()

    report = json.loads(capsys.readouterr().out)
    assert any(
        check["name"] == "hub_local_health" and check["status"] == "error"
        for check in report["checks"]
    )


def test_doctor_probes_authenticated_hub_readiness_without_echoing_token(monkeypatch, capsys, tmp_path):
    """Configured machine auth must be checked without entering the report."""
    unit = tmp_path / "gptadmin-hub.service"
    unit.write_text("unit", encoding="utf-8")
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.bind(("127.0.0.1", 0))
    listener.listen(1)
    env_file = tmp_path / "gptadmin.env"
    env_file.write_text("ADMIN_PASSWORD=admin-secret\n", encoding="utf-8")
    env_file.chmod(0o600)
    calls = []

    class Response:
        status = 200
        headers = {"Date": datetime.now(timezone.utc).strftime("%a, %d %b %Y %H:%M:%S GMT")}

        def __init__(self, body):
            self.body = body

        def __enter__(self):
            return self

        def __exit__(self, *_args):
            return False

        def read(self):
            return self.body

    def urlopen(request, **_kwargs):
        calls.append((request.full_url, request.headers.get("Authorization", "")))
        if request.full_url.endswith("/healthz"):
            return Response(b'{"ok":true,"build_version":"128"}')
        if request.full_url.endswith("/admin/api/overview"):
            assert request.headers.get("Authorization") == "Bearer doctor-token"
            return Response(b'{"servers":[],"clients":[]}')
        raise AssertionError(request.full_url)

    port = listener.getsockname()[1]
    monkeypatch.setattr(cli, "installed_units", lambda: [("Hub", unit)])
    monkeypatch.setattr(cli, "ENV_FILE", env_file)
    monkeypatch.setattr(cli.urllib.request, "urlopen", urlopen)
    monkeypatch.setattr(cli, "env_read", lambda: {
        "ADMIN_PASSWORD": "admin-secret",
        "HUB_URL": "https://hub.example",
        "HUB_PORT": str(port),
        "CTL_TOKEN": "doctor-token",
    })

    cli.cmd_doctor(Namespace(json=True))
    listener.close()

    report = json.loads(capsys.readouterr().out)
    assert any(check["name"] == "remote_auth" and check["status"] == "ok" for check in report["checks"])
    assert calls == [
        ("https://hub.example/healthz", ""),
        ("https://hub.example/admin/api/overview", "Bearer doctor-token"),
    ]
    assert "doctor-token" not in json.dumps(report)
