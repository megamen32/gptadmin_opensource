"""Guard active source and documentation against Python ShellMCP launch paths."""

from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_active_docs_use_go_shellmcp() -> None:
    text = "\n".join((ROOT / name).read_text(encoding="utf-8") for name in ("README.md", "CONTRIBUTING.md"))
    assert "python client/shellmcp.py" not in text


def test_macos_shellmcp_wrapper_has_no_python_fallback() -> None:
    source = (ROOT / "cli.py").read_text(encoding="utf-8")
    wrapper = source[source.index("    def _wrapper_script"):source.index("    def _make_plist")]
    assert "_mac_python" not in wrapper
    assert "PYTHONPATH=" not in wrapper


def test_release_smoke_does_not_require_a_long_poll_listener() -> None:
    smoke = (ROOT / "tools/test-user-install-ssh.sh").read_text(encoding="utf-8")
    assert "127.0.0.1:25900/version" not in smoke
    assert "127.0.0.1:25900/system/health" not in smoke


def test_release_payload_does_not_ship_the_python_mcp_relay() -> None:
    """Production archives must contain Go ShellMCP, not the retired Python transport."""
    build = (ROOT / "tools" / "build.sh").read_text(encoding="utf-8")
    assert "cp -a agents/generic_stdio_mcp_relay" not in build
    assert "'agents/generic_stdio_mcp_relay'" not in build


def test_linux_smoke_uses_queue_mode_without_shellmcp_listener() -> None:
    """The release smoke must exercise long-poll mode instead of curling port 25900."""
    build = (ROOT / "tools" / "build.sh").read_text(encoding="utf-8")
    smoke = build[build.index("smoke_linux()") : build.index("# Dependency expansion.")]
    assert "SHELLMCP_QUEUE=1" in smoke
    assert "wait_for_http \"http://127.0.0.1:${SHELLMCP_PORT}/version\"" not in smoke


def test_repository_has_one_canonical_cli_source() -> None:
    """The generated package CLI must not be committed as a stale second source."""
    assert not (ROOT / "cli" / "gptadmin.py").exists()
