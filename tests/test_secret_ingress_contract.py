"""Documentation contract for remote secret ingress."""

from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_secret_ingress_docs_define_no_plaintext_mcp_flow() -> None:
    text = (ROOT / "docs" / "HUB.md").read_text(encoding="utf-8").lower()
    for required in ("secret_request", "secret_status", "secret_env", "plaintext", "input_url"):
        assert required in text, f"HUB.md is missing {required!r}"
    assert "never" in text


def test_secret_ingress_security_docs_define_secure_configuration() -> None:
    text = (ROOT / "docs" / "SECURITY_DOCS.md").read_text(encoding="utf-8")
    for required in (
        "GPTADMIN_SECRET_STORE_DIR",
        "GPTADMIN_SECRET_STORE_KEY_FILE",
        "GPTADMIN_SECRET_INGRESS_STATE_FILE",
        "GPTADMIN_SECRET_INGRESS_TTL",
        "0700",
        "0600",
    ):
        assert required in text, f"SECURITY_DOCS.md is missing {required!r}"
