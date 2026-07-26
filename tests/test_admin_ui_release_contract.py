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


def test_legacy_admin_uses_document_relative_static_asset_urls():
    """The release builder serves this source payload below /admin/legacy/."""
    html = (ROOT / "public" / "admin" / "index.html").read_text(encoding="utf-8")

    assert 'href="style.css"' in html
    assert 'src="app.js"' in html
    assert 'href="/admin/style.css"' not in html
    assert 'src="/admin/app.js"' not in html
