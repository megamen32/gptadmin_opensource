from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_private_instruction_tree_is_excluded_from_both_public_mirrors():
    """Personal instructions stay in the private source repository."""
    gitpublic_ignore = (ROOT / ".gitpublic" / "ignore").read_text()
    build_sync = (ROOT / ".github" / "workflows" / "build-and-sync.yml").read_text()

    assert "private/" in gitpublic_ignore
    assert "Sanitize tree for public bundles" in build_sync
    assert "scripts/publicize_sanitize.sh" in build_sync
    assert "Checkout opensource (public) repo" in build_sync
    assert "must NOT rsync the raw private tree" in build_sync
