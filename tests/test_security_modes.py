import cli
from pathlib import Path


def test_normal_system_mode_does_not_add_privilege_blocking_hardening():
    assert cli.linux_systemd_hardening("normal") == ""


def test_maximum_system_mode_enables_all_process_hardening():
    unit = cli.linux_systemd_hardening("maximum")
    assert "NoNewPrivileges=true" in unit
    assert "ProtectSystem=full" in unit
    assert "ProtectHome=true" in unit


def test_custom_system_mode_uses_explicit_process_flags():
    unit = cli.linux_systemd_hardening(
        "custom",
        {
            "no_new_privileges": False,
            "protect_system": True,
            "protect_home": False,
        },
    )
    assert "NoNewPrivileges" not in unit
    assert "ProtectSystem=full" in unit
    assert "ProtectHome" not in unit


def test_custom_profile_cannot_claim_privilege_blocking_without_nnp():
    try:
        cli.linux_systemd_hardening(
            "custom",
            {"allow_privileged_execution": False, "no_new_privileges": False},
        )
    except ValueError as exc:
        assert "no_new_privileges" in str(exc)
    else:
        raise AssertionError("contradictory custom profile was accepted")


def test_unit_rendering_replaces_import_time_profile_without_prefix_corruption():
    rendered = cli.render_unit_with_hardening(cli.UNIT_HUB, "")
    assert rendered.startswith("\n[Unit]")
    assert "Description=GPTAdmin Hub Proxy" in rendered


def test_admin_dashboard_exposes_process_and_bearer_profiles():
    dashboard = (Path(__file__).parents[1] / "public" / "admin_dashboard.html").read_text(encoding="utf-8")
    for marker in ("processSecurityMode", "bearerSecurityMode", "require_issuer", "/admin/api/security/profile"):
        assert marker in dashboard
