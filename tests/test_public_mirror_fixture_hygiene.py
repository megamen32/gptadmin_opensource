import re
import unittest
from pathlib import Path


class PublicMirrorFixtureHygieneTest(unittest.TestCase):
    def test_sensitive_go_fixtures_are_not_embedded_as_complete_literals(self):
        root = Path(__file__).resolve().parents[1]
        sources = (
            root / "go-shellmcp/internal/inspect/inspect_test.go",
            root / "go-shellmcp/internal/redact/redact_test.go",
            root / "go-shellmcp/internal/server/server_test.go",
        )
        jwt_pattern = re.compile(
            "eyJ" + r"[A-Za-z0-9_-]+" + r"\." + r"[A-Za-z0-9_-]+" + r"\." + r"[A-Za-z0-9_-]+"
        )
        pem_pattern = re.compile(
            "-" * 5 + r"BEGIN\s+[A-Z ]*PRIVATE KEY" + "-" * 5
        )

        violations = []
        for source in sources:
            text = source.read_text(encoding="utf-8")
            if jwt_pattern.search(text):
                violations.append(f"{source.relative_to(root)}: complete JWT-shaped fixture")
            if pem_pattern.search(text):
                violations.append(f"{source.relative_to(root)}: complete private-key marker")

        self.assertEqual([], violations, "public mirror scan must not find credential-shaped test literals")


if __name__ == "__main__":
    unittest.main()
