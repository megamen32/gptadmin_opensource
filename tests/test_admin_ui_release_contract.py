from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_release_builder_packages_react_admin_as_runtime_static_payload():
    build = (ROOT / "tools" / "build.sh").read_text(encoding="utf-8")

    assert "npm run build -- --base=/admin/" in build
    assert '"$ART_DIR/public/admin"' in build
    assert "public/admin" in build


def test_cli_installs_packaged_admin_static_payload_with_hub_runtime():
    cli = (ROOT / "cli.py").read_text(encoding="utf-8")

    assert "public_src = tdp / 'public'" in cli
    assert "public_dst = INSTALL_DIR / 'public'" in cli
    assert "admin_src = public_src / 'admin'" in cli
    assert "shutil.copytree(admin_src" in cli
    assert "shutil.copy2(sibling, target)" in cli
    assert "continue" in cli


def test_cli_install_keeps_secret_input_page_alongside_admin():
    """The Hub /secret-input/{token} form must ship as part of every release."""

    cli = (ROOT / "cli.py").read_text(encoding="utf-8")
    assert "public_src.iterdir()" in cli
    assert "sibling.name in {'admin', 'admin-legacy'}" in cli

    page = (ROOT / "public" / "secret-input" / "index.html")
    assert page.exists(), "public/secret-input/index.html is missing from the repository"
    body = page.read_text(encoding="utf-8")
    assert 'name="value"' in body
    assert 'type="password"' in body


def test_admin_runtime_is_the_single_react_application():
    html = (ROOT / "public" / "admin" / "index.html").read_text(encoding="utf-8")
    app = (ROOT / "admin-ui" / "src" / "App.tsx").read_text(encoding="utf-8")
    assert '<div id="root"></div>' in html
    assert '/admin/assets/' in html
    assert 'src="app.js"' not in html
    for native_screen in ("OverviewScreen", "AgentsScreen", "JobsScreen", "McpManageScreen", "SecurityScreen", "FailoverScreen"):
        assert native_screen in app
    assert "OperationsScreen" not in app
    assert not (ROOT / "admin-ui" / "src" / "OperationsScreen.tsx").exists()
    assert not (ROOT / "admin-ui" / "src" / "operations" / "runtime.js").exists()
    assert "iframe" not in app
