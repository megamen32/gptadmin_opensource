from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


def test_authenticated_admin_page_has_no_topbar_password_field():
    html = (ROOT / "public" / "admin" / "index.html").read_text()
    assert 'id="token"' not in html
    assert 'placeholder="optional CTL_TOKEN"' not in html


def test_authenticated_admin_page_explains_live_mcp_token_issuer():
    html = (ROOT / "public" / "admin" / "index.html").read_text()
    assert "PUBLIC_ORIGIN" in html
    assert "127.0.0.1" in html


def test_authenticated_admin_page_links_to_live_docs_and_authorize_hint():
    html = (ROOT / "public" / "admin" / "index.html").read_text()
    assert "https://became.bezrabotnyi.com/#/docs" in html
    assert "code_challenge_method=S256" in html
