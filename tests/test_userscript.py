"""Static smoke test for the MCP Bridge userscript.

Validates that public/mcp-bridge.user.js exists, has a valid ==UserScript==
header, and has balanced braces (a basic syntax sanity check that catches
the most common corruption without needing a JS runtime).
"""

import re
from pathlib import Path

from fastapi.testclient import TestClient

import server_for_installer

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
    for site in ["chatgpt.com", "deepseek.com", "chat.z.ai", "qwen.ai", "alice.yandex.ru"]:
        assert site in content, f"expected @match for {site} not found"
    assert "@downloadURL  https://became.bezrabotnyi.com/mcp-bridge.user.js" in content
    assert "@updateURL    https://became.bezrabotnyi.com/mcp-bridge.user.js" in content
    assert "// @connect      mcp.bezrabotnyi.com" in content


def test_userscript_version_is_bumped_for_installable_update():
    content = USERSCRIPT.read_text()
    assert "// @version      2.3.6" in content


def test_injected_prompt_describes_current_code_mode():
    content = USERSCRIPT.read_text()
    for required in [
        "MCP_USAGE_PROMPT",
        "fenced `mcp` JSON block",
        "tool `exec`",
        "args.code",
        "mcp.call(target, tool, args)",
        "await Promise.all([...])",
        "not automatically parallel",
        "approval rules",
        "inline play button",
        "same-profile instruction context",
        "Current profile instructions:",
        "shell_exec",
        '{cmd:"uptime"}',
        "keyed by the exact target ID",
    ]:
        assert required in content, f"missing current MCP usage instruction: {required}"
    assert "event.origin !== new URL(hubUrl()).origin" in content
    assert "event.data.state !== oauthState()" in content


def test_zai_custom_mcp_code_blocks_get_inline_execution():
    content = USERSCRIPT.read_text()
    assert "Strategy 6: Z.ai" in content
    assert ".markdown-prose > div.relative.my-2.flex.flex-col" in content
    assert "tryInjectPlay(block, () => block.innerText)" in content


def test_mcp_execution_uses_current_facade_and_same_profile_context():
    content = USERSCRIPT.read_text()
    start = content.index("async function execMcp(cmd, inlineBtn)")
    end = content.index("// ═══════════════════════════════════════════════════════════════\n  //  INLINE PLAY BUTTONS", start)
    execute = content[start:end]
    assert "mcpCall(cmd.tool" in execute
    assert "'/mcp-relay/call'" in execute
    assert "bridgeInstructionContext" in execute
    assert "mcpCall('execute'" not in execute


def test_alice_accepts_json_labeled_blocks_only_when_they_are_mcp_calls():
    content = USERSCRIPT.read_text()
    assert "lang !== 'mcp' && lang !== 'json'" in content
    assert "const cmd = extractMcpJson(textExtractor())" in content


def test_oauth_popup_opens_inside_user_gesture_before_async_work():
    content = USERSCRIPT.read_text()
    start = content.index("function beginOAuthFromClick()")
    end = content.index("async function connectOAuth(popup)", start)
    click_handler = content[start:end]
    assert "window.open('about:blank', 'gptadmin-oauth'" in click_handler
    assert "connectOAuth(popup)" in click_handler


def test_oauth_resumes_pending_registration_without_registering_again():
    content = USERSCRIPT.read_text()
    start = content.index("async function connectOAuth(popup)")
    end = content.index("window.addEventListener('message'", start)
    connect = content[start:end]
    assert "const resumePending = Boolean(pendingClientId && pendingState && pendingVerifier)" in connect
    assert "? { client_id: pendingClientId }" in connect
    assert "if (!resumePending)" in connect


def test_mcp_all_starts_oauth_and_resumes_prompt_insertion():
    content = USERSCRIPT.read_text()
    start = content.index("async function injectAll()")
    end = content.index("// ═══════════════════════════════════════════════════════════════\n  //  AGENT SELECT", start)
    inject_all = content[start:end]
    assert "requireOAuthConnection('MCP All', () => injectAll())" in inject_all


def test_mcp_tools_starts_oauth_and_resumes_agent_list():
    content = USERSCRIPT.read_text()
    start = content.index("function toggleAgentDropdown(e)")
    end = content.index("async function injectAgent(agentId)", start)
    menu = content[start:end]
    assert "requireOAuthConnection('MCP agent list', () => openAgentDropdown(e.currentTarget))" in menu


def test_mcp_all_loads_target_schemas_in_parallel_batches():
    content = USERSCRIPT.read_text()
    start = content.index("async function injectAll()")
    end = content.index("// ═══════════════════════════════════════════════════════════════\n  //  AGENT SELECT", start)
    inject_all = content[start:end]
    assert "MCP_ALL_SCHEMA_BATCH_SIZE = 8" in content
    assert "Promise.all(servers.slice(i, i + MCP_ALL_SCHEMA_BATCH_SIZE)" in inject_all


def test_agent_menu_is_labeled_as_mcp_tools():
    content = USERSCRIPT.read_text()
    assert "<span class=\"icon\">🔌</span> MCP Tools" in content


def test_first_use_mcp_actions_are_visible_before_hub_oauth():
    content = USERSCRIPT.read_text()
    assert "#mcp-toolbar:not(.mcp-open) .mcp-action { display: none; }" not in content
    assert "#mcp-toolbar.mcp-no-key .mcp-secure { display: none; }" not in content


def test_inserted_prompts_and_results_auto_send_by_default():
    content = USERSCRIPT.read_text()
    assert "GM_getValue('auto_enter', true)" in content
    start = content.index("function autoSubmitInserted(el)")
    end = content.index("// ═══════════════════════════════════════════════════════════════\n  //  REACT-COMPATIBLE INPUT", start)
    auto_submit = content[start:end]
    assert "form?.querySelectorAll('button[type=\"submit\"]')" in auto_submit
    assert "button[data-testid*=\"send\" i]" in auto_submit
    assert "button[aria-label*=\"отправить\" i]" in auto_submit
    assert "form.requestSubmit()" in auto_submit
    assert "el.dispatchEvent(new KeyboardEvent('keydown'" in auto_submit
    input_start = content.index("function setInputText(text)")
    input_end = content.index("// ═══════════════════════════════════════════════════════════════\n  //  CLIPBOARD", input_start)
    input_code = content[input_start:input_end]
    assert "autoSubmitInserted(el)" in input_code
    assert "Auto-send inserted prompts and MCP results" in content


def test_install_urls_allow_cross_origin_tampermonkey_fetch():
    client = TestClient(server_for_installer.app)
    for path in ("/mcp-bridge.user.js", "/userscript.js"):
        response = client.get(path)
        assert response.status_code == 200
        assert response.headers["access-control-allow-origin"] == "*"
        assert response.headers["content-disposition"] == "inline; filename=mcp-bridge.user.js"
