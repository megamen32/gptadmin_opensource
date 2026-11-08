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

    # A node-level redirect would bypass all location routing and recreate
    # the historical bug where /mcp and /actions/openapi.yaml went to became.
    assert "server_name $DOMAIN www.$DOMAIN;\n    return 301" not in text


def test_canonical_api_http_upgrade_stays_on_canonical_host():
    text = SCRIPT.read_text()
    assert 'return 308 https://$DOMAIN\\$request_uri;' in text


def test_bootstrap_location_keeps_secret_url_out_of_logs():
    text = SCRIPT.read_text()

    # A dedicated prefix location so regex locations never re-route the
    # secret-bearing path back through the logged default handling.
    assert "location ^~ /bootstrap/ {" in text
    assert text.count("location ^~ /bootstrap/ {") == 2

    # Both HTTP and TLS bootstrap contexts disable request logging; crit-level
    # errors only, so the URL never lands in access/error logs.
    assert text.count("access_log off;") == 2
    assert text.count("error_log /dev/null crit;") == 2

    # Non-cacheable responses: a secret-bearing URL must not be stored by
    # browsers or shared caches, and must not leak via Referer.
    assert text.count('add_header Cache-Control "no-store, private" always;') == 2
    assert text.count('add_header Referrer-Policy "no-referrer" always;') == 2

    # Plain-HTTP bootstrap hop redirects non-permanently (307), keeping the
    # URL out of any permanent-redirect caches.
    assert "return 307 https://$DOMAIN\\$request_uri;" in text

    # TLS bootstrap proxies to the same Hub upstream with the same header,
    # timeout and buffering contract as the existing MCP/API location.
    assert text.count("proxy_pass http://gptadmin_hub_active;") == 2
    assert text.count("proxy_read_timeout 3600s;") == 2
    assert text.count("proxy_buffering off;") == 2
