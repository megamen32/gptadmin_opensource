from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CLI = ROOT / "cli.py"


def test_update_prefers_explicit_component_flags_over_stale_files():
    text = CLI.read_text()
    assert "if 'INSTALL_HUB' in env else" in text
    assert "if 'INSTALL_SHELLMCP' in env else" in text


def test_update_does_not_run_setup_only_autoapprove_or_client_config():
    text = CLI.read_text()
    start = text.index("def cmd_update(args):")
    end = text.index("\n\n# ===== AI client MCP auto-configuration =====", start)
    block = text[start:end]
    assert "maybe_autoapprove_local_shellmcp(" not in block
    assert "auto_configure_ai_mcp_clients(" not in block


def test_macos_launchd_bootout_is_not_duplicated_before_bootstrap():
    # After the fix that splits svc_enable_start into svc_enable (load-only)
    # + svc_enable_start (load + kickstart), the bootstrap call lives in
    # svc_enable. timer_disable must use svc_enable (not svc_enable_start)
    # so a config reload never fires an unintended kickstart.
    text = CLI.read_text()
    enable_start = text.index("    def svc_enable_start(label: str, unit_path: Path):")
    enable_end = text.index("\n    def svc_restart", enable_start)
    enable_block = text[enable_start:enable_end]
    # svc_enable_start now delegates the bootout + bootstrap to svc_enable.
    assert "bootout', domain, str(unit_path)" not in enable_block
    assert "bootstrap = _launchctl_capture" not in enable_block
    assert "svc_enable(label, unit_path)" in enable_block

    # svc_enable owns the bootout + bootstrap + enable + load -w fallback.
    enable_fn = text.index("    def svc_enable(label: str, unit_path: Path):")
    enable_fn_end = text.index("\n    def svc_enable_start", enable_fn)
    load_block = text[enable_fn:enable_fn_end]
    assert "bootout', domain, str(unit_path)" not in load_block
    assert "bootstrap = _launchctl_capture" in load_block

    # timer_disable must use svc_enable (load-only) so the kickstart does
    # NOT fire on disable — that was the original CRITICAL bug.
    timer_disable_start = text.index("    def timer_disable(timer_unit: str):")
    timer_disable_end = text.index("\n    def timer_status", timer_disable_start)
    disable_block = text[timer_disable_start:timer_disable_end]
    assert "svc_enable(SVC_AUTO_UPDATE_LABEL" in disable_block
    assert "svc_enable_start(SVC_AUTO_UPDATE_LABEL" not in disable_block

    # timer_enable may still kick once (the first run of a freshly enabled
    # periodic update is intentional), so it keeps svc_enable_start.
    timer_enable_start = text.index("    def timer_enable(timer_unit: str):")
    timer_enable_end = text.index("\n    def timer_disable", timer_enable_start)
    enable_block_timer = text[timer_enable_start:timer_enable_end]
    assert "svc_enable_start(SVC_AUTO_UPDATE_LABEL" in enable_block_timer
