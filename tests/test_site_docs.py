import json
import os
import re
import shutil
from pathlib import Path
import subprocess


ROOT = Path(__file__).resolve().parent.parent
DOCS_MANIFEST = ROOT / "scripts" / "docs-manifest.json"
DOCS_ROOT = ROOT / "docs"
WEBSITE_SOURCE = ROOT / "website" / "src" / "content" / "docs"
WEBSITE_PUBLIC = ROOT / "website" / "public" / "docs"


def _site_docs_text() -> str:
    """Return all source documents displayed by the website docs page."""
    return "\n".join(path.read_text() for path in WEBSITE_SOURCE.rglob("*.md"))


def _published_docs() -> list[str]:
    return sorted(json.loads(DOCS_MANIFEST.read_text(encoding="utf-8")))


def _source_path(locale: str, filename: str) -> Path:
    return DOCS_ROOT / filename if locale == "en" else DOCS_ROOT / locale / filename


def _mirror_path(root: Path, locale: str, filename: str) -> Path:
    return root / locale / filename


def _assert_mirror(root: Path) -> None:
    published = _published_docs()
    for locale in ("en", "ru", "cn"):
        mirror_files = sorted(path.name for path in (root / locale).glob("*.md"))
        assert mirror_files == published
        for filename in published:
            assert _mirror_path(root, locale, filename).read_text() == _source_path(locale, filename).read_text()


def test_site_docs_cover_live_action_and_oauth_contract():
    docs_text = _site_docs_text()
    assert "https://<your-hub>/actions/openapi.yaml" in docs_text
    assert "/oauth/authorize" in docs_text
    assert "/oauth/token" in docs_text
    assert "gptadmin.read gptadmin.exec" in docs_text


def test_site_docs_do_not_publish_owner_hub_url():
    assert "u-f1102930.t.gptadmin.bezrabotnyi.com" not in _site_docs_text()


def test_site_docs_mirror_root_source_and_public_tree():
    _assert_mirror(WEBSITE_SOURCE)
    _assert_mirror(WEBSITE_PUBLIC)


def test_website_tree_is_not_a_gitlink_or_submodule_manifest():
    """The website docs tree must be a real subtree, not a submodule pointer."""

    assert not (ROOT / ".gitmodules").exists(), ".gitmodules should not exist for the website tree"

    result = subprocess.run(
        ["git", "-C", str(ROOT), "ls-tree", "HEAD", "website"],
        check=True,
        capture_output=True,
        text=True,
    )
    mode = result.stdout.split(None, 1)[0] if result.stdout.strip() else ""
    assert mode != "160000", f"website is still a gitlink: {result.stdout.strip()!r}"


def test_translate_docs_enumerates_root_manifest_not_website_mirror(tmp_path: Path):
    repo = tmp_path / "repo"
    docs_root = repo / "docs"
    scripts_root = repo / "scripts"
    website_scripts = repo / "website" / "scripts"
    website_en = repo / "website" / "src" / "content" / "docs" / "en"
    translator_stub = repo / "translator-stub.mjs"
    manifest = ["alpha.md", "beta.md"]

    docs_root.mkdir(parents=True)
    scripts_root.mkdir(parents=True)
    website_scripts.mkdir(parents=True)
    website_en.mkdir(parents=True)

    (scripts_root / "docs-manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    (docs_root / "alpha.md").write_text("alpha root\n", encoding="utf-8")
    (docs_root / "beta.md").write_text("beta root\n", encoding="utf-8")
    (website_en / "alpha.md").write_text("alpha mirror\n", encoding="utf-8")
    (website_scripts / "translate-docs.mjs").write_text(
        (ROOT / "website" / "scripts" / "translate-docs.mjs").read_text(encoding="utf-8"),
        encoding="utf-8",
    )
    translator_stub.write_text("process.exit(0);\n", encoding="utf-8")

    env = os.environ.copy()
    env["TRANSLATE_CLI"] = str(translator_stub)
    env["KEEP_TRANSLATION_TMP"] = "1"

    retained_tmp = None
    try:
        result = subprocess.run(
            ["node", str(website_scripts / "translate-docs.mjs"), "--dry-run", "--provider", "stub"],
            cwd=repo,
            check=True,
            capture_output=True,
            text=True,
            env=env,
        )

        match = re.search(r"retained (.+) for diagnostics", result.stderr)
        assert match, result.stderr
        retained_tmp = Path(match.group(1))
        temporary_docs = retained_tmp / "docs"

        assert sorted(path.name for path in temporary_docs.glob("*.md")) == manifest
        assert (temporary_docs / "alpha.md").read_text(encoding="utf-8") == (docs_root / "alpha.md").read_text(encoding="utf-8")
        assert (temporary_docs / "beta.md").read_text(encoding="utf-8") == (docs_root / "beta.md").read_text(encoding="utf-8")
        assert (temporary_docs / "alpha.md").read_text(encoding="utf-8") != (website_en / "alpha.md").read_text(encoding="utf-8")
    finally:
        if retained_tmp is not None:
            shutil.rmtree(retained_tmp, ignore_errors=True)
