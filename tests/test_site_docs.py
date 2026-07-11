from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parent.parent

DOCS_PAGE = ROOT / "website" / "src" / "components" / "site" / "pages" / "docs-page.tsx"
DOCS_REFERENCE = ROOT / "website" / "src" / "components" / "site" / "docs-reference.ts"

# The website/ is a private git submodule that CI does not check out (it has no
# token with cross-repo access). Run these guards only where the submodule is
# present (developer machines, the opensource mirror, etc.).
pytestmark = pytest.mark.skipif(
    not DOCS_PAGE.exists(),
    reason="website submodule not checked out",
)


def test_site_docs_cover_live_action_oauth_and_apps_links():
    docs_page = DOCS_PAGE.read_text()
    assert "https://your-subdomain.t.gptadmin.bezrabotnyi.com/actions/openapi.yaml" in docs_page
    assert "https://your-subdomain.t.gptadmin.bezrabotnyi.com/authorize" in docs_page
    assert "https://your-subdomain.t.gptadmin.bezrabotnyi.com/token" in docs_page
    assert "gptadmin.read gptadmin.exec" in docs_page
    assert "https://developers.openai.com/api/docs/actions/authentication" in docs_page
    assert "https://developers.openai.com/apps-sdk/deploy/connect-chatgpt" in docs_page


def test_site_docs_do_not_publish_owner_hub_url():
    docs_page = DOCS_PAGE.read_text()
    docs_reference = DOCS_REFERENCE.read_text()
    assert "u-f1102930.t.gptadmin.bezrabotnyi.com" not in docs_page
    assert "u-f1102930.t.gptadmin.bezrabotnyi.com" not in docs_reference
