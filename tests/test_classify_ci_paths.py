"""Classification regressions with a real temporary Git repository."""
import importlib.util
import json
import os
import re
import textwrap
from pathlib import Path
import subprocess
import tempfile
import unittest

SOURCE = Path(__file__).parents[1] / "scripts/classify_ci_paths.py"
SPEC = importlib.util.spec_from_file_location("classify_ci_paths", SOURCE)
ci = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ci)


class ClassifierTests(unittest.TestCase):
    def test_only_exact_reviewed_operator_paths_are_exempt(self):
        self.assertTrue(ci.classify(list(ci.OPERATOR_PATHS)))
        for path in ("go-hub/internal/hub/server.go", ".github/workflows/build-and-sync.yml",
                     ".github/workflows/auto-tag.yml", ".github/workflows/unreviewed.yml",
                     "scripts/github_ci_autofix.py/extra", "VERSION", "website/scripts/sync-docs.mjs",
                     "docs/GITHUB_CI_AUTOFIX.md\nVERSION", "./scripts/github_ci_autofix.py", None):
            self.assertFalse(ci.classify(["scripts/github_ci_autofix.py", path]))
        self.assertFalse(ci.classify([]))

    def test_auto_tag_uses_the_same_exact_operator_allowlist(self):
        text = (SOURCE.parents[1] / ".github/workflows/auto-tag.yml").read_text()
        paths = set(re.findall(r"^      - '([^']+)'$", text, re.MULTILINE))
        self.assertEqual(paths - {".github/**"}, ci.OPERATOR_PATHS)
        self.assertIn(".github/**", paths)  # existing release policy, not product-CI exemption

    def test_release_waits_for_installer_and_operator_checks(self):
        text = (SOURCE.parents[1] / ".github/workflows/build-and-sync.yml").read_text()
        release = text.split("\n  build-and-release:", 1)[1].split("    runs-on:", 1)[0]
        needs = re.search(r"needs: \[(.*?)\]", release).group(1).split(", ")
        self.assertIn("installer-e2e-linux", needs)
        self.assertIn("operator-checks", needs)
        self.assertIn("go-unit-tests", needs)

    def test_actual_ci_gate_rejects_failure_and_unjustified_skips(self):
        text = (SOURCE.parents[1] / ".github/workflows/build-and-sync.yml").read_text()
        script = textwrap.dedent(text.split("\n  ci-gate:", 1)[1].split("        run: |\n", 1)[1])
        products = ("GO_RESULT", "UI_RESULT", "FAILOVER_RESULT", "INSTALLER_RESULT", "DOCS_RESULT", "CANDIDATE_RESULT")
        releases = ("RELEASE_RESULT", "MACOS_RESULT", "WINDOWS_RESULT")
        base = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "SCOPE_RESULT": "success",
                "OPERATOR_RESULT": "success", "PRODUCT_REQUIRED": "false", "EVENT_NAME": "push",
                "REF_NAME": "refs/heads/main", **dict.fromkeys((*products, *releases), "skipped")}
        def accepted(**changes):
            return subprocess.run(["bash", "-c", script], env={**base, **changes},
                                  capture_output=True, timeout=5).returncode == 0
        self.assertTrue(accepted())
        self.assertFalse(accepted(SCOPE_RESULT="failure"))
        self.assertFalse(accepted(OPERATOR_RESULT="failure"))
        self.assertFalse(accepted(PRODUCT_REQUIRED=""))
        self.assertFalse(accepted(UI_RESULT="success"))
        self.assertFalse(accepted(EVENT_NAME="workflow_dispatch"))
        product = {"PRODUCT_REQUIRED": "true", **dict.fromkeys(products, "success")}
        self.assertTrue(accepted(**product))
        self.assertFalse(accepted(**{**product, "INSTALLER_RESULT": "failure"}))
        self.assertFalse(accepted(**{**product, "CANDIDATE_RESULT": "skipped"}))
        self.assertFalse(accepted(**product, EVENT_NAME="workflow_dispatch"))
        release = {**product, **dict.fromkeys(releases, "success"), "EVENT_NAME": "workflow_dispatch"}
        self.assertTrue(accepted(**release))
        self.assertFalse(accepted(**{**release, "INSTALLER_RESULT": "failure"}))
        self.assertFalse(accepted(**{**release, "WINDOWS_RESULT": "skipped"}))
        self.assertTrue(accepted(**{**release, "EVENT_NAME": "push", "REF_NAME": "refs/tags/v1008"}))
        self.assertFalse(accepted(REF_NAME="refs/tags/v1008"))

    def test_invalid_shas_refuse_exemption(self):
        for base in ("0" * 40, "HEAD~1", "-option", "f" * 39):
            with self.assertRaises(ValueError):
                ci.changed_paths(base, "a" * 40)

    def test_real_diff_includes_deleted_and_renamed_product_paths(self):
        # Root .tmp follows the repository temporary-storage policy.
        project_tmp = Path(__file__).parents[1] / ".tmp"
        project_tmp.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(dir=project_tmp, prefix="operator-classifier-") as directory:
            root = Path(directory)
            def git(*args):
                return subprocess.check_output(["git", "-C", str(root), *args], stderr=subprocess.DEVNULL).decode().strip()
            git("init", "--quiet")
            git("config", "user.name", "Local classification test")
            git("config", "user.email", "test@example.invalid")
            (root / "VERSION").write_text("1\n")
            git("add", "VERSION")
            git("commit", "--quiet", "-m", "base")
            base = git("rev-parse", "HEAD")
            (root / "scripts").mkdir()
            (root / "VERSION").rename(root / "scripts/github_ci_autofix.py")
            git("add", "--all")
            git("commit", "--quiet", "-m", "rename")
            head = git("rev-parse", "HEAD")
            result = subprocess.run(["python3", str(SOURCE), "--base", base, "--head", head],
                                    cwd=root, capture_output=True, text=True, check=True)
            value = json.loads(result.stdout)
            self.assertIn("VERSION", value["paths"])
            self.assertFalse(value["operator_only"])
            self.assertTrue(value["product_required"])


if __name__ == "__main__":
    unittest.main()
