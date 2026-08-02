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


def test_android_lan_proxy_prefers_a_connected_usb_device_over_stale_wifi_adb() -> None:
    """A physical USB device must win over a configured but stale Wi-Fi serial."""
    script = SCRIPT.read_text(encoding="utf-8")

    assert "usb_serial()" in script
    assert 'devices -l' in script
    assert '$0 ~ /usb:/' in script
    assert 'SERIAL="$(usb_serial || true)"' in script


def test_android_lan_proxy_chooses_a_free_adb_forward_port() -> None:
    """The internal ADB forward must not collide with an Xray listener."""
    script = SCRIPT.read_text(encoding="utf-8")

    assert "choose_adb_forward_port()" in script
    assert "for ((port=3127; port>=3000; port--))" in script
    assert 'ADB_FORWARD_PORT="$(choose_adb_forward_port)"' in script


def test_android_lan_proxy_requires_a_cellular_route_before_serving() -> None:
    """A 4G bridge must fail closed instead of silently using Wi-Fi."""
    script = SCRIPT.read_text(encoding="utf-8")

    assert "ensure_cellular_route()" in script
    assert "settings put global wifi_on 0" in script
    assert "svc wifi disable" in script
    assert "ip route get" in script
    assert "CELLULAR_ROUTE_PROBE" in script
    assert 'dev rmnet' in script


def test_android_lan_proxy_waits_for_wifi_shutdown_before_rejecting_cellular() -> None:
    """Wi-Fi teardown is asynchronous, so the route check needs a bounded retry."""
    script = SCRIPT.read_text(encoding="utf-8")

    assert "CELLULAR_ROUTE_ATTEMPTS" in script
    assert "for ((attempt=1; attempt<=CELLULAR_ROUTE_ATTEMPTS; attempt++))" in script
