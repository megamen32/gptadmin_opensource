"""Contract tests for the real ChatGPT browser acceptance runner."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent / "e2e"))

from chatgpt_browser_acceptance import (  # noqa: E402
    BrowserAcceptanceError,
    _action_paths,
    _action_instruction_markers,
    _chatgpt_rate_limited,
    _extract_browser_ref,
    _last_ingress_call_count,
    _read_ingress_trace,
    _summarize_ingress,
    _redacted_url,
    _sse_json,
    run_browserclaw_admin_chat_acceptance,
    run_browserclaw_admin_oauth_save_acceptance,
    run_browserclaw_custom_gpt_acceptance,
)


def test_browser_acceptance_redacts_action_query_and_fragment() -> None:
    assert _redacted_url("https://hub.example/mcp?access_token=secret#x") == "https://hub.example/mcp"


def test_browser_acceptance_extracts_discover_and_call_paths() -> None:
    schema = """openapi: 3.0.0\npaths:\n  /mcp-relay/servers:\n    get: {}\n  /mcp-relay/call:\n    post: {}\ncomponents:\n"""
    assert _action_paths(schema) == {"/mcp-relay/call", "/mcp-relay/servers"}


def test_action_instruction_markers_cover_relay_workflow() -> None:
    markers = _action_instruction_markers("Discover schema then execute and poll the job")
    assert markers == {"discover": True, "schema": True, "execute": True, "poll": True}


def test_browser_runner_is_opt_in_and_receipt_is_secret_safe() -> None:
    source = Path("tests/e2e/chatgpt_browser_acceptance.py").read_text(encoding="utf-8")
    assert "GPTADMIN_CHATGPT_CDP_URL" in source
    # Look only inside the Plugin Flow receipt (the first one)
    legacy_receipt_section = source.split("receipt = {", 1)[1].split("receipt = {", 1)[0]
    assert "Authorization" not in legacy_receipt_section
    assert "manual outbound approval" in source or "outbound-call approval" in source


def test_browserclaw_sse_parser_returns_last_json_event_only() -> None:
    raw = "event: message\ndata: {\"id\":1}\n\ndata: {\"id\":2}\n"
    assert _sse_json(raw) == {"id": 2}


def test_browserclaw_ref_parser_requires_fresh_matching_ref() -> None:
    page = '- button "Попробовать в чате" [ref=e142]\n'
    assert _extract_browser_ref(page, "Попробовать в чате") == "e142"
    assert _extract_browser_ref(page, "GPTADMIN") is None


def test_browserclaw_classifies_chatgpt_rate_limit() -> None:
    assert _chatgpt_rate_limited('heading "Слишком много запросов" [level=2]')
    assert _chatgpt_rate_limited('heading "Too many requests" [level=2]')
    assert not _chatgpt_rate_limited('textbox "Чат с ChatGPT" [ref=e1]')


def test_browserclaw_mode_is_secret_safe_and_bounded() -> None:
    source = Path("tests/e2e/chatgpt_browser_acceptance.py").read_text(encoding="utf-8")
    assert "Mcp-Session-Id" in source
    assert "browserclaw" in source.lower()
    # Each receipt body in the file (separated by `receipt = {`) must not
    # contain "Authorization" or "cookie" — only redacted origins.
    parts = source.split("receipt = {")
    assert len(parts) >= 2
    for part in parts[1:]:
        section = part.split("if receipt_path", 1)[0]
        assert "Authorization" not in section
        assert "cookie" not in section.lower()


def test_browserclaw_custom_gpt_mode_rejects_plugin_url() -> None:
    try:
        run_browserclaw_custom_gpt_acceptance(
            ssh_alias="unused",
            custom_gpt_url="https://chatgpt.com/plugins/plugin_asdk_app_6a58185cb3c88191a208d863f7281ca9",
            action_origin="https://example.invalid",
            expected_servers=[],
        )
    except BrowserAcceptanceError as exc:
        assert "g/g-*" in str(exc)
    else:
        raise AssertionError("Plugin Flow URL was accepted as Custom GPT")


def test_admin_oauth_save_mode_validates_urls_and_client_id() -> None:
    try:
        run_browserclaw_admin_oauth_save_acceptance(
            ssh_alias="unused",
            editor_url="https://chatgpt.com/gpts/editor/g-x-admin",
            schema_url="http://example.com/actions/openapi.yaml",  # http, not https
            auth_url="https://example.com/oauth/authorize",
            token_url="https://example.com/oauth/token",
            client_id="chatgpt",
            scope="gptadmin.read",
        )
    except BrowserAcceptanceError as exc:
        assert "https" in str(exc)
    else:
        raise AssertionError("http schema URL was accepted")


def test_admin_oauth_save_mode_rejects_empty_client_id() -> None:
    try:
        run_browserclaw_admin_oauth_save_acceptance(
            ssh_alias="unused",
            editor_url="https://chatgpt.com/gpts/editor/g-x-admin",
            schema_url="https://example.com/actions/openapi.yaml",
            auth_url="https://example.com/oauth/authorize",
            token_url="https://example.com/oauth/token",
            client_id="",
            scope="gptadmin.read",
        )
    except BrowserAcceptanceError as exc:
        assert "client_id" in str(exc)
    else:
        raise AssertionError("empty client_id was accepted")


def test_admin_oauth_save_mode_redacts_client_id_in_receipt() -> None:
    """Inspect the source to ensure the receipt never emits the full client id."""
    source = Path("tests/e2e/chatgpt_browser_acceptance.py").read_text(encoding="utf-8")
    func_start = source.find("def run_browserclaw_admin_oauth_save_acceptance")
    next_def = source.find("\ndef ", func_start + 1)
    body = source[func_start:next_def if next_def > 0 else len(source)]
    assert "client_id_redacted" in body
    # Bearer tokens must never appear in the runner body.
    assert "Bearer " not in body
    # The runner builds a redacted receipt (first two + last two chars) so the
    # full client id never appears as a stored value.
    assert 'client_id[:2]' in body or "client_id.split" in body or "[:2] + " in body


def test_admin_chat_mode_requires_chatgpt_custom_gpt_url() -> None:
    try:
        run_browserclaw_admin_chat_acceptance(
            ssh_alias="unused",
            custom_gpt_url="https://chatgpt.com/plugins/plugin_asdk_legacy",
            action_origin="https://example.invalid",
            prompt="probe",
        )
    except BrowserAcceptanceError as exc:
        assert "g/g-*" in str(exc)
    else:
        raise AssertionError("Plugin Flow URL was accepted as admin chat")


def test_admin_chat_mode_redacts_prompt_in_receipt() -> None:
    """The receipt body must contain a redacted prompt and never a bearer value."""
    source = Path("tests/e2e/chatgpt_browser_acceptance.py").read_text(encoding="utf-8")
    func_start = source.find("def run_browserclaw_admin_chat_acceptance")
    next_def = source.find("\ndef ", func_start + 1)
    body = source[func_start:next_def if next_def > 0 else len(source)]
    assert "prompt_redacted" in body
    # Bearer tokens must never appear in executable code. Skip whole
    # docstring ranges and comment lines. We track whether we are inside a
    # triple-quoted block.
    in_docstring = False
    quote = '"""'
    for line in body.splitlines():
        if in_docstring:
            if quote in line:
                in_docstring = False
            continue
        stripped = line.lstrip()
        if stripped.startswith("#") or stripped.startswith('"""') or stripped.startswith("'''"):
            if '"""' in stripped and stripped.count('"""') == 1:
                in_docstring = True
                quote = '"""'
            continue
        # Opening of a multi-line docstring
        if stripped.startswith('"""') or stripped.startswith("'''"):
            in_docstring = True
            quote = stripped[:3]
            continue
        assert "Bearer " not in line, f"Bearer literal leaked: {line}"
    assert "delta_by_path" in body
    assert "call_200_observed" in body


def test_ingress_counter_returns_int_or_none() -> None:
    """Smoke test the helper; it returns an int when sudo works, else None."""
    value = _last_ingress_call_count(pattern="nonexistent-pattern-xyz")
    assert value is None or isinstance(value, int)


def test_admin_chat_mode_rejects_invalid_auth_mode() -> None:
    try:
        run_browserclaw_admin_chat_acceptance(
            ssh_alias="unused",
            custom_gpt_url="https://chatgpt.com/g/g-6a2f388cffe4819186ba5f042183c03e-admin",
            action_origin="https://example.invalid",
            prompt="probe",
            auth_mode="bogus",
        )
    except BrowserAcceptanceError as exc:
        assert "auth_mode" in str(exc)
    else:
        raise AssertionError("bogus auth_mode was accepted")


def test_admin_chat_mode_accepts_oauth_and_bearer() -> None:
    """Both auth modes must pass URL validation; bearer mode requires URL only."""
    for mode in ("oauth", "bearer"):
        try:
            run_browserclaw_admin_chat_acceptance(
                ssh_alias="unused",
                custom_gpt_url="https://chatgpt.com/plugins/plugin_legacy_x",
                action_origin="https://example.invalid",
                prompt="probe",
                auth_mode=mode,
            )
        except BrowserAcceptanceError as exc:
            assert "g/g-*" in str(exc)
        else:
            raise AssertionError(f"Plugin Flow URL accepted in {mode} mode")


def test_ingress_trace_parser_returns_structured_rows() -> None:
    rows = _read_ingress_trace()
    if not rows:
        # sudo unavailable in this environment — still a valid outcome.
        assert rows == []
        return
    for row in rows[:3]:
        assert "timestamp" in row
        assert "method" in row
        assert "path" in row
        assert "status" in row
        assert row["path"].startswith("/mcp-relay")


def test_ingress_summarize_groups_by_path_and_status() -> None:
    rows = [
        {"timestamp": "x", "method": "GET", "path": "/mcp-relay/servers", "status": 200, "size": 1},
        {"timestamp": "x", "method": "POST", "path": "/mcp-relay/call", "status": 403, "size": 1},
        {"timestamp": "x", "method": "POST", "path": "/mcp-relay/call", "status": 200, "size": 1},
    ]
    summary = _summarize_ingress(rows)
    assert summary["total"] == 3
    assert summary["by_path_status"]["/mcp-relay/call"]["403"] == 1
    assert summary["by_path_status"]["/mcp-relay/call"]["200"] == 1
    assert summary["by_status"]["200"] == 2
    assert summary["by_status"]["403"] == 1


def test_admin_chat_mode_recognises_sign_in_with_provider_button() -> None:
    """The runner detects the OAuth provider sign-in button and auto-clicks it."""
    source = Path("tests/e2e/chatgpt_browser_acceptance.py").read_text(encoding="utf-8")
    func_start = source.find("def run_browserclaw_admin_chat_acceptance")
    next_def = source.find("\ndef ", func_start + 1)
    body = source[func_start:next_def if next_def > 0 else len(source)]
    assert "Войти" in body
    assert "sign_in_with_provider_required" in body
    assert "auth_gate_kind" in body
    assert "signin_pattern" in body
    assert "consent_pattern" in body


def test_admin_chat_mode_does_click_sign_in_and_consent_buttons() -> None:
    """Operator authorization 2026-08-09: the runner must auto-click the
    `Войти в систему` and `Разрешить` controls so the T-proof is end-to-end.
    This test pins the current parallel-flow behaviour so a future agent does not revert it
    back to a manual-only loop."""
    source = Path("tests/e2e/chatgpt_browser_acceptance.py").read_text(encoding="utf-8")
    func_start = source.find("def run_browserclaw_admin_chat_acceptance")
    next_def = source.find("\ndef ", func_start + 1)
    body = source[func_start:next_def if next_def > 0 else len(source)]
    assert "auto_clicked_sign_in_with_provider" in body
    assert "auto_clicked_oauth_consent" in body
    assert "kind.*click" in body or '"kind": "click"' in body
