from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "deploy" / "setup_nginx.sh"


def test_canonical_gptadmin_nginx_keeps_web_and_api_separate():
    text = SCRIPT.read_text()

    assert 'DOMAIN="${GPTADMIN_DOMAIN:-became.bezrabotnyi.com}"' in text
    assert 'SITE_DOMAIN="${GPTADMIN_SITE_DOMAIN:-became.bezrabotnyi.com}"' in text
    assert "proxy_pass http://gptadmin_hub_active;" in text
    assert "mcp(?:\\$|-relay/|-prompt/)" in text
    assert "actions/" in text
    assert "\\.well-known/oauth-" in text
    assert "oauth/" in text
    assert "connect(?:\\$|\\\\.json\\$|/)" in text
    assert "server/" in text
    assert "agent/" in text
    assert "artifacts/" in text
    assert 'return 301 https://$SITE_DOMAIN\\$request_uri;' in text

    # A server-level redirect would bypass all location routing and recreate
    # the historical bug where /mcp and /actions/openapi.yaml went to became.
    assert "server_name $DOMAIN www.$DOMAIN;\n    return 301" not in text


def test_canonical_api_http_upgrade_stays_on_canonical_host():
    text = SCRIPT.read_text()
    assert 'return 308 https://$DOMAIN\\$request_uri;' in text
