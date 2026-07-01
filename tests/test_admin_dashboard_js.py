from pathlib import Path


ADMIN_HTML = Path(__file__).resolve().parents[1] / "public" / "admin_dashboard.html"


def _line_no(text: str, needle: str) -> int:
    idx = text.find(needle)
    assert idx != -1, f"missing {needle!r} in admin dashboard"
    return text[:idx].count("\n") + 1


def test_agent_card_constants_are_initialized_before_rendering_agents():
    """Regression for ReferenceError: Cannot access AGENT_CARD_CAPS_SHOWN before initialization.

    renderAll() immediately renders agents with renderAgentCard(). Because renderAgentCard()
    reads AGENT_CARD_CAPS_SHOWN / AGENT_CARD_META_KEYS_SHOWN, those consts must be
    initialized earlier in the script/function body than the first renderAgentCard call.
    """
    html = ADMIN_HTML.read_text(encoding="utf-8")
    call_line = _line_no(html, "agents.map(renderAgentCard)")
    caps_line = _line_no(html, "const AGENT_CARD_CAPS_SHOWN")
    meta_line = _line_no(html, "const AGENT_CARD_META_KEYS_SHOWN")

    assert caps_line < call_line, (
        "AGENT_CARD_CAPS_SHOWN must be initialized before renderAll calls renderAgentCard "
        f"(const line {caps_line}, call line {call_line})"
    )
    assert meta_line < call_line, (
        "AGENT_CARD_META_KEYS_SHOWN must be initialized before renderAll calls renderAgentCard "
        f"(const line {meta_line}, call line {call_line})"
    )

def test_max_active_ips_helpers_are_top_level_before_render_all():
    """Regression for ReferenceError: initMaxActiveIpsInput is not defined."""
    html = ADMIN_HTML.read_text(encoding="utf-8")
    render_all_line = _line_no(html, "function renderAll()")
    bootstrap_line = _line_no(html, "initMaxActiveIpsInput();showView")

    for name in ("getMaxActiveIps", "onMaxActiveIpsChange", "initMaxActiveIpsInput"):
        line = _line_no(html, f"function {name}")
        assert line < render_all_line, f"{name} must be top-level before renderAll (line {line})"
        assert line < bootstrap_line, f"{name} must be initialized before bootstrap (line {line})"

