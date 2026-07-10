from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


def test_site_docs_cover_live_action_oauth_and_apps_links():
    docs_page = (ROOT / "website" / "src" / "components" / "site" / "pages" / "docs-page.tsx").read_text()
    assert "https://u-f1102930.t.gptadmin.bezrabotnyi.com/actions/openapi.yaml" in docs_page
    assert "https://u-f1102930.t.gptadmin.bezrabotnyi.com/authorize" in docs_page
    assert "https://u-f1102930.t.gptadmin.bezrabotnyi.com/token" in docs_page
    assert "gptadmin.read gptadmin.exec" in docs_page
    assert "https://developers.openai.com/api/docs/actions/authentication" in docs_page
    assert "https://developers.openai.com/apps-sdk/deploy/connect-chatgpt" in docs_page
