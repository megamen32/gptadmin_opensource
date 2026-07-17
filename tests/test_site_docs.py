from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parent.parent

DOCS_CONTENT = ROOT / "website" / "src" / "content" / "docs"

# The website/ is a private git submodule that CI does not check out (it has no
# token with cross-repo access). Run these guards only where its rendered-doc
# source is present (developer machines, the opensource mirror, etc.).
pytestmark = pytest.mark.skipif(
    not DOCS_CONTENT.exists(),
    reason="website submodule not checked out",
)


def _site_docs_text() -> str:
    """Return all source documents displayed by the website docs page."""
    return "\n".join(path.read_text() for path in DOCS_CONTENT.rglob("*.md"))


def test_site_docs_cover_live_action_and_oauth_contract():
    docs_text = _site_docs_text()
    assert "https://<your-hub>/actions/openapi.yaml" in docs_text
    assert "/oauth/authorize" in docs_text
    assert "/oauth/token" in docs_text
    assert "gptadmin.read gptadmin.exec" in docs_text


def test_site_docs_do_not_publish_owner_hub_url():
    assert "u-f1102930.t.gptadmin.bezrabotnyi.com" not in _site_docs_text()
