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


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--package", required=True, type=Path)
    parser.add_argument("--client-origin", default=DEFAULT_CLIENT_ORIGIN)
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
