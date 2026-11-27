#!/usr/bin/env python3
"""Real systemd proof for the socket bootstrap on temporary high-port units.

This never starts, stops, reloads, or rewrites gptadmin-hub, nginx, or the
production listener ports. It uses uniquely named units under /run/systemd/system
and a private state directory. The node binary is executed read-only.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import pwd
import secrets
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import gptadmin_socket_bootstrap as bootstrap  # noqa: E402

PRODUCTION_UNITS = frozenset({
    "gptadmin-hub.service", "gptadmin-hub.socket", "nginx.service",
    "gptadmin-bootstrap-bridge.service", "gptadmin-bootstrap-green.service",
    "gptadmin-bootstrap-reconcile.service",
})
PRODUCTION_PORTS = frozenset({9001, 19001, 19002})
BINARY = Path("/opt/gptadmin-node/current/bin/gptadmin-node")


class SandboxFailure(RuntimeError):
    pass


def require_safe_unit(name):
    if name in PRODUCTION_UNITS or name.startswith("gptadmin-hub"):
        raise SandboxFailure(f"refusing production unit {name}")
    if not name.startswith("gptadmin-sbx-"):
        raise SandboxFailure(f"sandbox unit must be temporary: {name}")


def require_safe_port(port):
    if int(port) in PRODUCTION_PORTS or int(port) < 1024:
        raise SandboxFailure(f"refusing production or privileged port {port}")


def run(argv, timeout=30, check=True):
    if argv and argv[0] == "systemctl":
        for arg in argv[1:]:
            if arg in PRODUCTION_UNITS or arg.startswith("gptadmin-hub"):
                raise SandboxFailure(f"refusing systemctl against production: {argv}")
    result = subprocess.run(argv, text=True, capture_output=True, timeout=timeout, check=False)
    if check and result.returncode:
        detail = (result.stderr or result.stdout or "").strip().splitlines()
        raise SandboxFailure(f"command failed ({result.returncode}): {' '.join(argv)}: "
                             f"{detail[-1] if detail else 'no output'}")
    return result


def free_port():
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    require_safe_port(port)
    return port


def production_identity():
    result = subprocess.run(
        ["systemctl", "show", "gptadmin-hub.service", "nginx.service",
         "-p", "Id", "-p", "ActiveState", "-p", "MainPID"],
        text=True, capture_output=True, timeout=10, check=False)
    return result.stdout


def wait_health(port, timeout=20):
    deadline = time.monotonic() + timeout
    last = ""
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz", timeout=1) as response:
                body = response.read(200)
            if response.status == 200 and b'"ok":true' in body.replace(b" ", b""):
                return body
            last = f"HTTP {response.status}"
        except (OSError, urllib.error.URLError) as error:
            last = str(error)
        time.sleep(0.1)
    raise SandboxFailure(f"healthz failed on {port}: {last}")


def write_env(path, port, root, token, user):
    origin = f"http://127.0.0.1:{port}"
    path.write_text("\n".join([
        "PATH=/usr/bin:/bin", f"HOME={root}/home", "LANG=C.UTF-8",
        "GPTADMIN_HUB_HOST=127.0.0.1", f"GPTADMIN_HUB_PORT={port}",
        f"GPTADMIN_ROOT={root}", f"GPTADMIN_CONFIG_DIR={root}/config",
        f"CTL_TOKEN={token}", f"SHELL_TOKEN={token}",
        f"PUBLIC_ORIGIN={origin}", f"MCP_RESOURCE={origin}",
        "GPTADMIN_AUTH_MODE=writer", "GPTADMIN_NODE_PEERS={}",
        "SHELL_NAME=gptadmin-socket-sandbox",
        f"SHELL_IDENTITY_DIR={root}/state", f"SHELL_SPOOL_DIR={root}/spool",
        f"SHELL_OUTBOX_DIR={root}/outbox", f"SHELL_DEFAULT_USER={user}",
        f"SHELL_DEFAULT_HOME={root}/home", f"SHELL_DEFAULT_CWD={root}/home",
        "SHELLMCP_SELF_REPAIR_DISABLE=1", "ROOTD_AUTO_UPDATE=0", "GOMAXPROCS=1",
        "",
    ]))
    os.chmod(path, 0o600)


def service_text(env_path, binary, after_socket=""):
    requires = f"Requires={after_socket}\nAfter={after_socket}\n" if after_socket else ""
    sockets = f"Sockets={after_socket}\n" if after_socket else ""
    return f"""[Unit]
Description=GPTAdmin temporary socket sandbox writer
{requires}
[Service]
Type=simple
User=admin
{sockets}EnvironmentFile={env_path}
ExecStart={binary}
Restart=no
KillMode=mixed
TimeoutStopSec=20
StartLimitBurst=1
"""


def socket_text(port, service):
    require_safe_port(port)
    require_safe_unit(service)
    return f"""[Unit]
Description=GPTAdmin temporary socket sandbox listener

[Socket]
Service={service}
ListenStream=127.0.0.1:{port}
Backlog=128
NoDelay=true

[Install]
WantedBy=sockets.target
"""


def bridge_text(unit_names, bridge_port, upstream_port, control):
    require_safe_port(bridge_port)
    require_safe_port(upstream_port)
    script = ROOT / "scripts" / "gptadmin_socket_bootstrap.py"
    return f"""[Unit]
Description=GPTAdmin temporary socket sandbox bridge
[Service]
Type=simple
ExecStart=/usr/bin/python3 {script} bridge-serve --listen 127.0.0.1:{bridge_port} --upstream 127.0.0.1:{upstream_port} --control {control} --deadline 120 --max-connections 32 --max-buffer 1048576 --per-connection-buffer 262144
Restart=no
"""


def install(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    os.chmod(path, 0o644)


def canary_once(port):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz", timeout=15) as response:
            body = response.read(200)
            if response.status >= 500 or b'"ok":true' not in body.replace(b" ", b""):
                return f"HTTP {response.status}"
            return ""
    except (OSError, urllib.error.URLError) as error:
        return f"{type(error).__name__}: {error}"


def prove_direct_start_inherits_one_listener():
    """A direct systemctl start must receive the active socket, not bind another."""
    suffix = secrets.token_hex(3)
    service = f"gptadmin-sbx-{suffix}.service"
    sock = f"gptadmin-sbx-{suffix}.socket"
    require_safe_unit(service)
    require_safe_unit(sock)
    port = free_port()
    marker = Path(f"/var/tmp/{service}.out")
    program = Path(f"/var/tmp/{service}.py")
    unit_dir = Path("/run/systemd/system")
    program.write_text(
        "import os, time, sys\n"
        "fds = os.environ.get('LISTEN_FDS', '')\n"
        "pid = os.environ.get('LISTEN_PID', '')\n"
        "open(sys.argv[1], 'w').write(f'{fds} {pid} {os.getpid()}\\n')\n"
        "if fds != '1' or pid != str(os.getpid()):\n"
        "    raise SystemExit(2)\n"
        "time.sleep(30)\n")
    os.chmod(program, 0o755)
    try:
        install(unit_dir / sock, socket_text(port, service))
        install(unit_dir / service,
                "[Unit]\n"
                f"Requires={sock}\nAfter={sock}\n"
                "[Service]\nType=simple\nUser=admin\n"
                f"Sockets={sock}\n"
                f"ExecStart=/usr/bin/python3 {program} {marker}\n"
                "Restart=no\n")
        run(["systemctl", "daemon-reload"])
        run(["systemctl", "start", sock], timeout=20)
        run(["systemctl", "start", service], timeout=20)
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline and not marker.exists():
            time.sleep(0.05)
        if not marker.is_file():
            raise SandboxFailure("direct start did not record listener metadata")
        fds, pid, self_pid = marker.read_text().split()
        if fds != "1" or pid != self_pid:
            raise SandboxFailure(f"direct start missed the socket fd: {marker.read_text()!r}")
        listeners = run(["ss", "-ltn", f"sport = :{port}"], check=False).stdout
        if listeners.count(f"127.0.0.1:{port}") != 1:
            raise SandboxFailure(f"expected one listener on {port}: {listeners!r}")
    finally:
        run(["systemctl", "stop", service, sock], timeout=20, check=False)
        (unit_dir / service).unlink(missing_ok=True)
        (unit_dir / sock).unlink(missing_ok=True)
        program.unlink(missing_ok=True)
        marker.unlink(missing_ok=True)
        run(["systemctl", "daemon-reload"], check=False)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=BINARY)
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SandboxFailure("root is required for temporary systemd units")
    if not args.binary.is_file():
        raise SandboxFailure(f"node binary is missing: {args.binary}")
    prove_direct_start_inherits_one_listener()
    bootstrap.run = lambda argv, timeout=30, check=True, input_text=None: run(
        argv, timeout=timeout, check=check)
    before = production_identity()
    token = secrets.token_hex(16)
    suffix = secrets.token_hex(4)
    names = {
        "old": f"gptadmin-sbx-{suffix}-old.service",
        "green": f"gptadmin-sbx-{suffix}-green.service",
        "canonical": f"gptadmin-sbx-{suffix}-canonical.service",
        "bridge": f"gptadmin-sbx-{suffix}-bridge.service",
        "socket": f"gptadmin-sbx-{suffix}.socket",
        "runtime": f"gptadmin-sbx-{suffix}",
    }
    for name in (names["old"], names["green"], names["canonical"], names["bridge"], names["socket"]):
        require_safe_unit(name)
    writer_port, bridge_port = free_port(), free_port()
    state = Path(f"/var/tmp/{names['runtime']}")
    unit_dir = Path("/run/systemd/system")
    paths = {key: unit_dir / names[key] for key in ("old", "green", "canonical", "bridge", "socket")}
    user = pwd.getpwnam("admin")
    errors = []
    try:
        state.mkdir(mode=0o755, parents=True, exist_ok=True)
        for leaf in ("old", "green", "canonical"):
            root = state / leaf
            for name in ("config", "state", "spool", "outbox", "home"):
                (root / name).mkdir(parents=True, mode=0o700)
            os.chown(root, user.pw_uid, user.pw_gid)
            for child in root.iterdir():
                os.chown(child, user.pw_uid, user.pw_gid)
            write_env(state / f"{leaf}.env", writer_port, root, token, user.pw_name)
            os.chown(state / f"{leaf}.env", 0, 0)
        control = state / "bridge.sock"
        install(paths["old"], service_text(state / "old.env", args.binary))
        install(paths["green"], service_text(state / "green.env", args.binary, names["socket"]))
        install(paths["canonical"], service_text(state / "canonical.env", args.binary, names["socket"]))
        install(paths["socket"], socket_text(writer_port, names["green"]))
        install(paths["bridge"], bridge_text(names, bridge_port, writer_port, control))
        run(["systemctl", "daemon-reload"])
        run(["systemctl", "start", names["old"]], timeout=20)
        wait_health(writer_port)
        run(["systemctl", "start", names["bridge"]], timeout=20)
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline and not control.exists():
            time.sleep(0.05)
        wait_health(bridge_port)
        if canary_once(bridge_port):
            raise SandboxFailure("bridge failed before the switch")
        bootstrap.bridge_control = _control_factory(control)
        # The imported bridge_control uses the production socket path.
        # Talk to this sandbox socket directly.
        status = control_command(control, "hold")
        if status.get("mode") != "hold":
            raise SandboxFailure("sandbox bridge refused HOLD")
        bootstrap.prove_writer_stopped(names["old"], 20)
        run(["systemctl", "start", names["socket"]], timeout=20)
        run(["systemctl", "start", names["green"]], timeout=20)
        if run(["systemctl", "is-active", names["green"]], check=False).stdout.strip() != "active":
            raise SandboxFailure("green writer did not stay active after socket adoption")
        wait_health(writer_port)
        install(paths["socket"], socket_text(writer_port, names["canonical"]))
        run(["systemctl", "daemon-reload"])
        active = run(["systemctl", "is-active", names["socket"]], check=False).stdout.strip()
        if active != "active":
            raise SandboxFailure(f"socket dropped during retarget: {active}")
        listen = run(["systemctl", "show", names["socket"], "-p", "Listen", "--value"],
                     check=False).stdout
        if str(writer_port) not in listen:
            raise SandboxFailure(f"socket listen changed: {listen!r}")
        bootstrap.prove_writer_stopped(names["green"], 20)
        run(["systemctl", "start", names["canonical"]], timeout=20)
        if run(["systemctl", "is-active", names["canonical"]], check=False).stdout.strip() != "active":
            raise SandboxFailure("canonical writer did not stay active after socket adoption")
        wait_health(writer_port)
        released = control_command(control, "release")
        if released.get("mode") != "forward":
            raise SandboxFailure("sandbox bridge refused RELEASE")
        failure = canary_once(bridge_port)
        if failure:
            errors.append("post-switch " + failure)
        # Induced failure: canonical dies after the socket is up. Rollback must
        # prove every sandbox writer and the socket are stopped before old starts.
        held = control_command(control, "hold")
        if held.get("mode") != "hold":
            raise SandboxFailure("sandbox bridge refused rollback HOLD")
        bootstrap.prove_writer_stopped(names["canonical"], 20)
        try:
            bootstrap.prove_writer_stopped(names["green"], 20)
            bootstrap.prove_socket_stopped(names["socket"], 20)
        except bootstrap.BootstrapFailure as error:
            errors.append(f"rollback proof failed, old writer not started: {error}")
            raise
        run(["systemctl", "start", names["old"]], timeout=20)
        wait_health(writer_port)
        restored = control_command(control, "release")
        if restored.get("mode") != "forward":
            raise SandboxFailure("sandbox bridge refused rollback RELEASE")
        failure = canary_once(bridge_port)
        if failure:
            errors.append("post-rollback " + failure)
        if errors:
            raise SandboxFailure("; ".join(errors))
        after = production_identity()
        print(json.dumps({
            "ok": True, "suffix": suffix, "writer_port": writer_port,
            "bridge_port": bridge_port, "binary": str(args.binary),
            "production_unchanged": before == after,
        }, sort_keys=True))
        return 0
    finally:
        for name in (names["bridge"], names["canonical"], names["green"], names["old"], names["socket"]):
            run(["systemctl", "stop", name], timeout=20, check=False)
        for path in paths.values():
            path.unlink(missing_ok=True)
        run(["systemctl", "daemon-reload"], check=False)
        if production_identity() != before:
            print("gptadmin-bootstrap-sandbox: production identity changed during the run",
                  file=sys.stderr)


def _control_factory(path):
    def command(text, timeout=3):
        return control_command(path, text, timeout)
    return command


def control_command(path, text, timeout=3):
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
        sock.settimeout(timeout)
        sock.connect(str(path))
        sock.sendall(text.encode())
        return json.loads(sock.recv(65536))


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (SandboxFailure, bootstrap.BootstrapFailure, OSError) as error:
        print(f"gptadmin-bootstrap-sandbox: {type(error).__name__}: {error}", file=sys.stderr)
        raise SystemExit(4)
