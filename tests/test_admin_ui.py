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


def test_admin_ui_does_not_offer_legacy_ctl_bearer_controls():
    html = (ROOT / "public" / "admin" / "index.html").read_text()
    script = (ROOT / "public" / "admin" / "app.js").read_text()
    assert "CTL_TOKEN" not in html
    assert "CTL_TOKEN" not in script


def test_admin_security_controls_use_typed_hub_endpoints_without_shell_env_mutation():
    html = (ROOT / "public" / "admin" / "index.html").read_text()
    script = (ROOT / "public" / "admin" / "app.js").read_text()
    security_start = script.index("// ===== Security management =====")
    security = script[security_start:]
    for internal_name in ("MCP_BRIDGE_KEY", "OAUTH_CLIENT_SECRET", "SHELLMCP_TOKEN", "CTL_TOKEN"):
        assert internal_name not in html
        assert internal_name not in security
    assert "setEnvVar" not in html
    assert "shell_exec" not in security
    for endpoint in (
        "/admin/api/security/preset",
        "/admin/api/security/reauth",
        "/admin/api/security/mfa/totp/enroll",
        "/admin/api/security/mfa/totp/verify",
        "/admin/api/telemetry",
        "/admin/api/approvals",
    ):
        assert endpoint in security
    assert "function ensureSecurityReauth" in security


def test_admin_security_ui_offers_passkey_enrollment_without_raw_credentials():
    """The shipped admin SPA must expose the backend WebAuthn enrollment flow."""

    html = (ROOT / "public" / "admin" / "index.html").read_text(encoding="utf-8")
    script = (ROOT / "public" / "admin" / "app.js").read_text(encoding="utf-8")
    security_start = script.index("// ===== Security management =====")
    security = script[security_start:]
    assert "Зарегистрировать passkey" in html
    assert "securityPasskeyResult" in html
    assert "/admin/api/security/mfa/webauthn/register/begin" in security
    assert "/admin/api/security/mfa/webauthn/register/finish" in security
    assert "navigator.credentials.create" in security
    assert "CTL_TOKEN" not in security
