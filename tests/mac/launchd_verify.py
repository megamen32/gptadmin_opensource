#!/usr/bin/env python3
"""REAL-MAC launchd verification harness for gptadmin auto-update.

Run on a REAL Mac only (requires launchd). Skips automatically on non-Darwin.
Invoke:

    python3 tests/mac/launchd_verify.py

or, to wire it into the test suite on macOS runners:

    GPTADMIN_MAC_VERIFY=1 pytest tests/mac/launchd_verify.py

Exercises the actual cli.py macOS functions against the real launchd on the
host machine, in an isolated namespace (``GPTADMIN_SERVICE_SUFFIX=.macverify``
and a fresh ``tempfile.mkdtemp(prefix='gptadmin_macverify_')`` for HOME). The
real ``gptadmin`` CLI is replaced by a shim that only touches a marker file,
so "running an update" is safe and observable.

Verifies (8/8 historical pass on macOS 26.6 arm64):

  1. write_autoupdate_unit writes a valid plist and LOADS it (svc_enable).
  2. write_autoupdate_unit does NOT fire the shim at install (load-only path).
  3. launchctl print 'state' field is parseable (Go-side concern).
  4. Manual ``launchctl kickstart`` runs the shim (unified trigger works).
  5. timer_disable does NOT fire the shim (CRITICAL fix from code review).
  6. timer_disable leaves the job loaded (manual kickstart preserved).
  7. timer_enable DOES fire the shim (first kick on enable is intended).
  8. _launchctl_kickstart_cmd builds the right gui/<uid>/<label> target.
"""
from __future__ import annotations

import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import time

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]

# ---------------------------------------------------------------------------
# Non-Darwin: skip cleanly so this file is safe to keep in the test suite on
# Linux CI. Exit 0 (when run as a script) so `python3 tests/mac/launchd_verify.py`
# never fails on a non-Mac host. pytest sees a `pytest.skip` and reports 1
# skipped (still green).
# ---------------------------------------------------------------------------


def _skip_non_darwin() -> None:
    if sys.platform == "darwin":
        return
    msg = (
        "SKIP: tests/mac/launchd_verify.py requires macOS launchd "
        f"(sys.platform={sys.platform!r}); skipping."
    )
    if "pytest" in sys.modules:
        import pytest

        pytest.skip(msg, allow_module_level=True)
    print(msg)
    sys.exit(0)


_skip_non_darwin()


# ---------------------------------------------------------------------------
# ISOLATED ENV — must be set BEFORE importing cli, because cli.py freezes
# paths and the launchd label at import time from these env vars.
# ---------------------------------------------------------------------------
ISOLATED_HOME = pathlib.Path(tempfile.mkdtemp(prefix="gptadmin_macverify_"))
SERVICE_SUFFIX = ".macverify"
SHIM_PATH = ISOLATED_HOME / "shim.sh"
MARKER_PATH = ISOLATED_HOME / "marker.log"

os.environ["GPTADMIN_INSTALL_MODE"] = "user"
# Force every per-user path (Library/LaunchAgents, etc.) into the isolated
# HOME so the harness never touches the real user account.
os.environ["GPTADMIN_USER_HOME"] = str(ISOLATED_HOME)
os.environ["GPTADMIN_HOME"] = str(ISOLATED_HOME)
os.environ["GPTADMIN_CONFIG_DIR"] = str(ISOLATED_HOME / "etc")
os.environ["GPTADMIN_SERVICE_SUFFIX"] = SERVICE_SUFFIX
os.environ["GPTADMIN_CLI_PATH"] = str(SHIM_PATH)

SHIM_PATH.parent.mkdir(parents=True, exist_ok=True)
# Hardcode the absolute marker path: the wrapper runs under launchd whose
# environment is sourced from ENV_FILE, not from this Python process, so
# $GPTADMIN_HOME is NOT in the wrapper's env. A relative/env-dependent path
# would write the marker where the harness can't read it.
SHIM_PATH.write_text(
    f'#!/bin/sh\necho "RAN $(date +%s)" >> "{MARKER_PATH}"\nexit 0\n'
)
SHIM_PATH.chmod(0o755)
MARKER_PATH.write_text("")


# ---------------------------------------------------------------------------
# Import cli with the repo root on sys.path so the harness is self-contained.
# ---------------------------------------------------------------------------
sys.path.insert(0, str(REPO_ROOT))
import cli  # noqa: E402  (must come after env setup)


results: list[tuple[str, bool]] = []


def _marker_count() -> int:
    try:
        return len(MARKER_PATH.read_text().splitlines())
    except FileNotFoundError:
        return 0


def _check(name: str, ok: bool, detail: str = "") -> None:
    results.append((name, bool(ok)))
    tag = "PASS" if ok else "FAIL"
    print(f"[{tag}] {name}  {detail}")


def run_checks() -> int:
    """Execute all 8 checks against the real launchd. Returns the process exit code."""
    label = cli.SVC_AUTO_UPDATE_LABEL
    uid = os.getuid()
    domain_target = f"gui/{uid}/{label}"

    print("=== CONFIG ===")
    print(f"label         = {label}")
    print(f"plist path    = {cli.UNIT_PATH_AUTO_UPDATE}")
    print(f"wrapper       = {cli.BIN_DIR / 'run_auto_update.sh'}")
    print(f"domain target = {domain_target}")
    print(f"INSTALL_SCOPE = {cli.INSTALL_SCOPE}")
    print(f"isolated HOME = {ISOLATED_HOME}")

    # clean slate (best-effort; ignore "service not loaded" errors)
    subprocess.run(
        ["launchctl", "bootout", f"gui/{uid}/{label}"], capture_output=True
    )

    # ---- 1+2. write_autoupdate_unit (writes plist + loads via svc_enable) ----
    print("\n=== 1. write_autoupdate_unit (should LOAD, not run) ===")
    before = _marker_count()
    cli.write_autoupdate_unit({})
    time.sleep(1)
    after = _marker_count()
    print(f"plist exists: {cli.UNIT_PATH_AUTO_UPDATE.exists()}")
    print(f"plist content:\n{cli.UNIT_PATH_AUTO_UPDATE.read_text()}")
    loaded = subprocess.run(
        ["launchctl", "print", domain_target], capture_output=True, text=True
    )
    print(f"launchctl print rc={loaded.returncode}")
    _check(
        "write_autoupdate_unit loads the job (print rc=0)",
        loaded.returncode == 0,
    )
    _check(
        "write_autoupdate_unit does NOT fire the shim at install",
        before == after,
        f"(marker {before} -> {after})",
    )

    # ---- 3. launchctl print 'state' field (Go-parser concern) ----
    print("\n=== 3. launchctl print 'state' field location ===")
    state_lines = [
        line for line in loaded.stdout.splitlines() if "state" in line.lower()
    ]
    print(f"lines containing 'state': {state_lines[:8]}")
    parsed_state = None
    for line in loaded.stdout.splitlines():
        t = line.strip()
        if t.startswith("state"):
            rest = t[len("state") :].lstrip(" \t=").strip(';"').strip()
            parsed_state = rest
            break
    print(f"Go-style parsed state = {parsed_state!r}")
    _check(
        "state field parseable (not None)",
        parsed_state is not None,
        f"= {parsed_state!r}",
    )

    # ---- 4. Manual kickstart runs the shim ----
    print("\n=== 4. manual launchctl kickstart (should RUN shim) ===")
    before = _marker_count()
    ks = subprocess.run(
        ["launchctl", "kickstart", "-k", domain_target],
        capture_output=True,
        text=True,
    )
    print(f"kickstart rc={ks.returncode} err={ks.stderr.strip()}")
    time.sleep(2)
    after = _marker_count()
    _check(
        "manual kickstart runs the shim",
        after > before,
        f"(marker {before} -> {after})",
    )

    # ---- 5+6. timer_disable must NOT fire the shim (CRITICAL) ----
    print("\n=== 5. timer_disable (should NOT run shim — CRITICAL fix) ===")
    before = _marker_count()
    cli.timer_disable(label)
    time.sleep(2)
    after = _marker_count()
    _check(
        "timer_disable does NOT fire the shim",
        before == after,
        f"(marker {before} -> {after})",
    )
    still = subprocess.run(
        ["launchctl", "print", domain_target], capture_output=True, text=True
    )
    _check(
        "job still loaded after timer_disable (manual kickstart preserved)",
        still.returncode == 0,
    )

    # ---- 7. timer_enable SHOULD fire (first kick on enable intended) ----
    print("\n=== 7. timer_enable (should RUN shim — intentional first kick) ===")
    before = _marker_count()
    cli.timer_enable(label)
    time.sleep(2)
    after = _marker_count()
    _check(
        "timer_enable fires the shim (intended)",
        after > before,
        f"(marker {before} -> {after})",
    )

    # ---- 8. _launchctl_kickstart_cmd builds correct target ----
    print("\n=== 8. _launchctl_kickstart_cmd ===")
    cmd = cli._launchctl_kickstart_cmd(label, is_user=True)
    print(f"cmd = {cmd}")
    _check(
        "kickstart cmd targets gui/<uid>/<label>",
        cmd == ["launchctl", "kickstart", "-k", f"gui/{uid}/{label}"],
        f"= {cmd}",
    )

    # ---- cleanup ----
    print("\n=== cleanup ===")
    cli.svc_disable_stop(label, cli.UNIT_PATH_AUTO_UPDATE)
    subprocess.run(
        ["launchctl", "bootout", f"gui/{uid}/{label}"], capture_output=True
    )
    try:
        cli.UNIT_PATH_AUTO_UPDATE.unlink()
    except FileNotFoundError:
        pass
    print("bootout + plist removed.")

    print("\n=== SUMMARY ===")
    passed = sum(1 for _, ok in results if ok)
    print(f"{passed}/{len(results)} checks passed")
    return 0 if passed == len(results) else 1


def _cleanup_isolated_home() -> None:
    shutil.rmtree(ISOLATED_HOME, ignore_errors=True)


# ---------------------------------------------------------------------------
# pytest entry point — keeps the harness discoverable from the test suite on
# a real Mac (where `GPTADMIN_MAC_VERIFY=1` is typically set in CI). On Linux
# the module-level _skip_non_darwin() already aborted collection.
# ---------------------------------------------------------------------------
def test_launchd_verify() -> None:
    """Run all 8 launchd verification checks on this host."""
    rc = run_checks()
    _cleanup_isolated_home()
    assert rc == 0, f"launchd verification failed (rc={rc})"


if __name__ == "__main__":
    try:
        rc = run_checks()
    finally:
        _cleanup_isolated_home()
    sys.exit(rc)
