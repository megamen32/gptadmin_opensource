from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
ADMIN_APP = ROOT / "admin-ui" / "src" / "App.tsx"
AGENTS_SCREEN = ROOT / "admin-ui" / "src" / "AgentsScreen.tsx"
OVERVIEW_SCREEN = ROOT / "admin-ui" / "src" / "OverviewScreen.tsx"
FAILOVER_SCREEN = ROOT / "admin-ui" / "src" / "FailoverScreen.tsx"


def test_server_card_constants_are_initialized_before_rendering_servers():
    """Native React agent rendering must not depend on legacy initialization order."""
    source = AGENTS_SCREEN.read_text(encoding="utf-8")
    assert "selected.capabilities" in source
    assert "Object.entries(selected.meta" in source
    assert "SERVER_CARD_CAPS_SHOWN" not in source
    assert "SERVER_CARD_META_KEYS_SHOWN" not in source
    assert "renderServerCard" not in source


def test_removed_max_active_ips_helpers_leave_no_stale_bootstrap_call():
    """The retired max-active-IPs control must not survive in the native dashboard."""
    source = "\n".join(
        path.read_text(encoding="utf-8")
        for path in (ADMIN_APP, AGENTS_SCREEN, OVERVIEW_SCREEN)
    )
    for name in ("getMaxActiveIps", "onMaxActiveIpsChange", "initMaxActiveIpsInput"):
        assert name not in source


def test_split_admin_problem_servers_id_matches_render_target():
    """Problem-server rendering is a React list, not a fragile DOM id target."""
    source = OVERVIEW_SCREEN.read_text(encoding="utf-8")
    assert "problems.map" in source
    assert 'href="#agents"' in source
    assert "problemAgents" not in source
    assert "problemServers" not in source


def test_split_admin_show_view_knows_failover_title():
    app = ADMIN_APP.read_text(encoding="utf-8")
    failover = FAILOVER_SCREEN.read_text(encoding="utf-8")
    assert 'import FailoverScreen from "./FailoverScreen"' in app
    assert 'view === "failover" ? <FailoverScreen />' in app
    assert "Резервирование" in failover
