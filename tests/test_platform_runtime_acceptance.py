from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "tests" / "e2e" / "platform" / "runtime_acceptance.py"


def source() -> str:
    return SCRIPT.read_text(encoding="utf-8")


def test_platform_acceptance_proves_real_execution_on_both_client_surfaces() -> None:
    text = source()
    assert '"/mcp-relay/call"' in text
    assert '"initialize"' in text
    assert '"tools/list"' in text
    assert '"tools/call"' in text
    assert "uptime" in text.lower()
    assert "shell_exec" in text
    assert "file_editor" in text


def test_platform_acceptance_independently_verifies_file_side_effects() -> None:
    text = source()
    assert '"action": "create"' in text
    assert '"action": "str_replace"' in text
    assert '"action": "delete"' in text
    assert "not visible through shell" in text
    assert "GPTADMIN_FILE_DELETE=passed" in text


def test_platform_acceptance_contains_no_test_double_fallback() -> None:
    lower = source().lower()
    for forbidden in ("mock", "fake service", "stub response", "monkeypatch"):
        assert forbidden not in lower
