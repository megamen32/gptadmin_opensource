"""Prevent secrets from leaking into the repo.

Scans tracked files for hardcoded secrets. Run in CI on every PR.
False positives → add the file to SKIP_FILES.
"""

import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

# Files to skip (test harnesses that generate passwords at runtime, etc.)
SKIP_FILES = {
    "tests/test_no_secrets.py",
    "docs/OPEN_CORE_PLAN.md",
    "CHANGELOG.md",
    "scripts/check_mac_tunnel_matrix.py",  # generates runtime passwords via secrets.token_urlsafe
    "gptadmin_hub.py",  # contains password validation logic, not hardcoded secrets
}

# Real secret patterns (high specificity)
SECRET_PATTERNS = [
    (r"github_pat_[A-Za-z0-9_]{30,}", "GitHub PAT"),
    (r"gh[po]_[A-Za-z0-9]{36,}", "GitHub token"),
    (r"sk-[A-Za-z0-9]{40,}", "OpenAI API key"),
]

# Static password pattern: password = "literal" (not env, not generated)
STATIC_PASSWORD_RE = re.compile(
    r"""(?:password|passwd|admin_password)\s*[:=]\s*['"]([^'"]{6,})['"]""",
    re.IGNORECASE,
)
# Values that are clearly not real secrets
SAFE_VALUES = {
    "changeme", "example", "password", "secret", "your-password",
    "choose-a-strong-password", "generate-a-strong-random-token",
    "your_token", "your-token", "test", "demo", "placeholder",
}


def _tracked_files():
    try:
        result = subprocess.run(
            ["git", "ls-files"], cwd=ROOT, capture_output=True, text=True, timeout=10
        )
        if result.returncode == 0:
            for line in result.stdout.strip().split("\n"):
                if line and line not in SKIP_FILES:
                    yield ROOT / line
    except Exception:
        yield from ROOT.glob("*.py")


def test_no_api_tokens():
    """No GitHub/OpenAI API tokens in tracked files."""
    for fpath in _tracked_files():
        if not fpath.exists():
            continue
        rel = fpath.relative_to(ROOT).as_posix()
        if rel.startswith("binaries/venv/") or "/site-packages/" in rel:
            continue
        if fpath.suffix in {".png", ".jpg", ".webp", ".gz", ".zip", ".lock"}:
            continue
        try:
            content = fpath.read_text(errors="ignore")
        except Exception:
            continue
        for pattern, name in SECRET_PATTERNS:
            matches = re.findall(pattern, content)
            assert not matches, f"{name} found in {fpath}: {matches[:2]}"


def test_no_static_passwords():
    """No hardcoded static passwords (env vars and generated values are OK)."""
    for fpath in _tracked_files():
        if not fpath.exists():
            continue
        rel = fpath.relative_to(ROOT).as_posix()
        if rel.startswith("binaries/venv/") or "/site-packages/" in rel:
            continue
        if fpath.suffix in {".png", ".jpg", ".webp", ".gz", ".zip", ".lock"}:
            continue
        try:
            content = fpath.read_text(errors="ignore")
        except Exception:
            continue
        for m in STATIC_PASSWORD_RE.finditer(content):
            val = m.group(1).lower()
            # Skip safe placeholder values
            if val in SAFE_VALUES or any(s in val for s in ("example", "your", "placeholder")):
                continue
            # Skip values that look dynamically generated (contain + or function calls)
            ctx = content[m.start():m.end()+30]
            if "+" in ctx or "secrets." in ctx or "token_urlsafe" in ctx or "os.environ" in content[max(0,m.start()-60):m.start()]:
                continue
            assert False, f"potential hardcoded password in {fpath.name}: '{m.group(1)[:15]}'"
