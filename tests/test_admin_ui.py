from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


def test_authenticated_admin_page_has_no_topbar_password_field():
    html = (ROOT / "public" / "admin" / "index.html").read_text()
    assert 'id="token"' not in html
    assert 'placeholder="optional CTL_TOKEN"' not in html


def test_authenticated_admin_page_explains_the_simple_mcp_auth_choice():
    html = (ROOT / "public" / "admin" / "index.html").read_text()
    assert "PUBLIC_ORIGIN" in html
    assert "Большинство клиентов подключаются сами через OAuth" in html


def test_authenticated_admin_page_links_to_live_docs_without_protocol_jargon():
    html = (ROOT / "public" / "admin" / "index.html").read_text()
    assert "https://became.bezrabotnyi.com/#/docs" in html
    assert "JWT для клиента без OAuth" in html


def test_admin_page_offers_simple_jwt_issue_and_rotation_for_non_oauth_clients():
    """Clients without OAuth need an obvious one-click JWT fallback path."""
    html = (ROOT / "public" / "admin" / "index.html").read_text()
    script = (ROOT / "public" / "admin" / "app.js").read_text()
    assert "JWT для клиента без OAuth" in html
    assert "rotateClient" in script
    assert "/admin/api/mcp/tokens/" in script
    assert 'secMcpAccessMode' in html
    assert 'value="readonly" selected' in html
    assert 'access_mode:accessMode' in script
    assert "r.access_mode === 'readonly'" in script


def test_admin_oauth_rotation_uses_hub_endpoint_without_client_side_secret_generation():
    script = (ROOT / "public" / "admin" / "app.js").read_text()
    assert "/admin/api/auth/rotate-oauth" in script
    assert "crypto.getRandomValues" not in script[script.index("async function rotateOAuth") : script.index("async function issueMcpTokenFromPanel")]
