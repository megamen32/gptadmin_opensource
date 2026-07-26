"""Regression checks for the Android 4G LAN proxy deployment contract."""

from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "deploy" / "android-4g-lan-proxy.sh"


def test_android_lan_proxy_opens_only_configured_private_lan_firewall_rule() -> None:
    """A LAN listener must install its private-source UFW rule idempotently."""
    script = SCRIPT.read_text(encoding="utf-8")

    assert "LAN_PROXY_ALLOWED_CIDRS" in script
    assert "configure_firewall" in script
    assert "ufw allow from \"$cidr\" to any port \"$port\" proto tcp" in script
    assert "ufw status" in script
