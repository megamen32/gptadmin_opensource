"""Contract checks for the browser extension's managed OAuth connection."""

import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
EXTENSION = ROOT / "public" / "mcp-bridge.user.js"


def test_browser_extension_uses_oauth_callback_without_manual_internal_key() -> None:
    """The extension must use Hub OAuth and never teach a bridge key field."""

    source = EXTENSION.read_text(encoding="utf-8")
    for required in ("/oauth/authorize", "/oauth/token", "gptadmin-oauth-callback", "addEventListener('message'", "code_verifier", "code_challenge"):
        assert required in source
    for forbidden in ("bridge_key", "Bridge Key", "/mcp-prompt/call?key=", "/mcp-prompt/prompt?"):
        assert forbidden not in source, f"extension still uses {forbidden}"


def test_browser_extension_is_valid_javascript() -> None:
    """Keep the shipped userscript parseable by the runtime that installs it."""

    result = subprocess.run(["node", "--check", str(EXTENSION)], capture_output=True, text=True)
    assert result.returncode == 0, result.stderr
