from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_haos_shellmcp_uses_stable_hub_ingress_not_primary_transport_url() -> None:
    script = (ROOT / "scripts/deploy_haos_shellmcp.sh").read_text(encoding="utf-8")

    assert "get('HAOS_HUB_URL')" in script
    assert "or 'https://became.bezrabotnyi.com'" in script
    assert "get('HUB_PUBLIC_URL')" not in script
    assert "get('HUB_URL')" not in script
