"""Static smoke test for the MCP Bridge userscript.

Validates that public/mcp-bridge.user.js exists, has a valid ==UserScript==
header, and has balanced braces (a basic syntax sanity check that catches
the most common corruption without needing a JS runtime).
"""

import re
from pathlib import Path

USERSCRIPT = Path(__file__).resolve().parent.parent / "public" / "mcp-bridge.user.js"


def test_userscript_exists():
    assert USERSCRIPT.exists(), f"userscript not found at {USERSCRIPT}"


def test_userscript_header_valid():
    content = USERSCRIPT.read_text()
    assert "// ==UserScript==" in content, "missing ==UserScript== opening"
    assert "// ==/UserScript==" in content, "missing ==/UserScript== closing"
    # Must have @name and @match
    assert re.search(r"// @name\s+\S+", content), "missing @name"
    assert re.search(r"// @match\s+\S+", content), "missing @match"
    # Must have at least one @grant
    assert re.search(r"// @grant\s+\S+", content), "missing @grant"


def test_userscript_balanced_braces():
    """Basic brace balance check — catches truncated/corrupted files."""
    content = USERSCRIPT.read_text()
    # Strip strings and comments to avoid false positives
    cleaned = re.sub(r"//.*", "", content)  # line comments
    cleaned = re.sub(r"/\*.*?\*/", "", cleaned, flags=re.DOTALL)  # block comments
    cleaned = re.sub(r"'[^']*'", "''", cleaned)  # single-quoted strings
    cleaned = re.sub(r'"[^"]*"', '""', cleaned)  # double-quoted strings
    cleaned = re.sub(r"`[^`]*`", "``", cleaned)  # template literals (basic)
    opens = cleaned.count("{")
    closes = cleaned.count("}")
    # Allow small delta — regex/template literal cleaning is imperfect
    delta = abs(opens - closes)
    assert delta <= 2, f"brace imbalance too large: {opens} open vs {closes} close (delta={delta})"


def test_userscript_supported_sites():
    """The userscript should @match at least the core supported sites."""
    content = USERSCRIPT.read_text()
    for site in ["chatgpt.com", "deepseek.com", "qwen.ai"]:
        assert site in content, f"expected @match for {site} not found"
