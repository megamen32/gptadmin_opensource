#!/usr/bin/env python3
"""Black-box a built Hub artifact through both documented client contracts.

This is intentionally an artifact gate, not a source/unit test: it starts the
packaged Hub binary on loopback, gives it the real client-facing origin as its
identity, and exercises Custom GPT Actions plus native MCP before a release can
be published/deployed.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import tempfile
import os
import socket
import subprocess
import time
from contextlib import contextmanager
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CLI_PATH = ROOT / "cli.py"
DEFAULT_CLIENT_ORIGIN = "https://your-subdomain.t.became.bezrabotnyi.com"


def load_cli():
    spec = importlib.util.spec_from_file_location("gptadmin_cli_candidate_gate", CLI_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {CLI_PATH}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@contextmanager
def local_candidate_origin(cli, package, root):
    """Serve OpenAPI from the real packaged Hub, without a deployed tenant.

    This is a second real executable instance, not a simulated dependency.
    The existing gate still validates its public import and both protocols.
    """
    extracted = root / 'public-origin'
    extracted.mkdir()
    cli.extract_tgz(package, extracted)
    binary = next((p for p in cli._platform_hub_candidates(extracted)
                   if p.is_file() and cli._binary_looks_native(p)), None)
    if binary is None:
        raise RuntimeError('candidate origin: package has no native Hub')
    os.chmod(binary, 0o755)
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    origin = f'http://127.0.0.1:{port}'
    env = dict(os.environ)
    env.update({
        'GPTADMIN_HUB_HOST': '127.0.0.1', 'HUB_HOST': '127.0.0.1', 'HUB_BIND': '127.0.0.1',
        'GPTADMIN_HUB_PORT': str(port), 'HUB_PORT': str(port), 'PORT': str(port),
        'GPTADMIN_CONFIG_DIR': str(root / 'public-origin-config'),
        'GPTADMIN_PUBLIC_DIR': str(extracted / 'public'),
        'GPTADMIN_OUTPUT_DIR': str(root / 'public-origin-outputs'),
        'GPTADMIN_ENV_FILE': str(root / 'origin.env'),
        'PUBLIC_ORIGIN': origin, 'TENANT_ORIGIN': origin, 'MCP_RESOURCE': origin,
        'HUB_PUBLIC_URL': origin,
        'NO_PROXY': 'localhost,127.0.0.1', 'no_proxy': 'localhost,127.0.0.1',
    })
    process = subprocess.Popen([str(binary)], cwd=extracted, env=env,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError('candidate origin exited before readiness')
            try:
                cli._candidate_http(origin, 'GET', '/version', timeout=1)
                break
            except Exception:
                time.sleep(0.1)
        else:
            raise RuntimeError('candidate origin readiness deadline')
        yield origin
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--package", required=True, type=Path)
    parser.add_argument("--client-origin", default=DEFAULT_CLIENT_ORIGIN)
    parser.add_argument('--local-origin', action='store_true',
                        help='Use a real isolated packaged Hub as the imported origin')
    args = parser.parse_args()

    package = args.package.resolve()
    if not package.is_file():
        raise SystemExit(f"missing Hub package: {package}")

    cli = load_cli()
    origin = cli.canonical_public_url(args.client_origin)
    if not origin:
        raise SystemExit("invalid --client-origin")

    with tempfile.TemporaryDirectory(prefix="gptadmin-ci-candidate-") as raw:
        config_dir = Path(raw) / "config"
        env = {
            "TENANT_ORIGIN": origin,
            "PUBLIC_ORIGIN": origin,
            "MCP_RESOURCE": origin,
            "HUB_PUBLIC_URL": origin,
            "OAUTH_CLIENT_SECRET": "ci-candidate-contract-secret-2026",
            "GPTADMIN_CONFIG_DIR": str(config_dir),
        }
        if args.local_origin:
            with local_candidate_origin(cli, package, Path(raw)) as local_origin:
                origin = local_origin
                env.update({key: origin for key in
                            ('TENANT_ORIGIN', 'PUBLIC_ORIGIN', 'MCP_RESOURCE', 'HUB_PUBLIC_URL')})
                result = cli._run_hub_candidate_pre_restart_gate(package, env)
        else:
            result = cli._run_hub_candidate_pre_restart_gate(package, env)

    expected = {"status": "passed", "custom_gpt": "passed", "mcp": "passed"}
    for key, value in expected.items():
        if result.get(key) != value:
            raise SystemExit(f"candidate contract failed: {json.dumps(result, sort_keys=True)}")
    if result.get("client_origin") != origin:
        raise SystemExit(f"candidate gate used wrong client origin: {result.get('client_origin')!r}")

    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
