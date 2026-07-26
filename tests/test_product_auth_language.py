"""Keep public onboarding copy on the Hub/AdminPassword product contract."""

from pathlib import Path
import subprocess

import pytest


ROOT = Path(__file__).resolve().parents[1]
PRODUCT_DOCS = (
    ROOT / "README.md",
    ROOT / "docs" / "GETTING_STARTED.md",
    ROOT / "docs" / "HUB.md",
    ROOT / "docs" / "INTEGRATIONS.md",
    ROOT / "docs" / "SHELLMCP.md",
    ROOT / "docs" / "FAQ.md",
    ROOT / "public" / "admin_dashboard.html",
    ROOT / "public" / "mcp-bridge.user.js",
)
FORBIDDEN_PRODUCT_NAMES = (
    "CTL_TOKEN",
    "SHELLMCP_TOKEN",
    "MCP_RELAY_AGENT_TOKEN",
    "OAUTH_CLIENT_SECRET",
    "Bridge Key",
    "bridge key",
)


@pytest.mark.parametrize("path", PRODUCT_DOCS)
def test_product_docs_do_not_teach_internal_credentials(path: Path) -> None:
    """Public setup and client docs must teach Hub URL/OAuth, not key copying."""

    text = path.read_text(encoding="utf-8")
    for internal_name in FORBIDDEN_PRODUCT_NAMES:
        assert internal_name not in text, f"{path.relative_to(ROOT)} exposes {internal_name}"


def test_cli_help_uses_product_connection_language() -> None:
    """CLI help is a setup surface and must not teach key names."""

    for command in (("setup", "--help"), ("tokens", "--help")):
        result = subprocess.run(
            ["python3", "cli.py", *command],
            cwd=ROOT,
            check=True,
            capture_output=True,
            text=True,
        )
        output = result.stdout + result.stderr
        for internal_name in FORBIDDEN_PRODUCT_NAMES:
            assert internal_name not in output, f"{command} exposes {internal_name}"
