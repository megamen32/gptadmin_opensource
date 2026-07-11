"""Unit tests for the launchd plist generation logic in cli.py.

The functions under test (``_plist_oneshot`` and ``_launchctl_kickstart_cmd``)
are platform-agnostic string/list builders — they emit launchd plist XML and
``launchctl`` argv regardless of whether the host is Linux or macOS. That
means we can (and should) test them on Linux CI so that Mac-specific
regressions get caught before they ship.

We import them directly from ``cli`` (the same convention used by
``test_cli_utils.py``) because cli.py's top-level side effects are benign
on Linux (it just reads env vars, defines constants, and detects platform).
"""

from __future__ import annotations

import os
import xml.etree.ElementTree as ET
from pathlib import Path

from cli import _launchctl_kickstart_cmd, _plist_oneshot


WRAPPER = Path("/usr/local/bin/run_auto_update.sh")
LOG_FILE = Path("/var/log/gptadmin/auto-update.log")
LABEL = "com.gptadmin.auto-update"


# ---------------------------------------------------------------------------
# _plist_oneshot
# ---------------------------------------------------------------------------


def test_plist_oneshot_no_interval_has_oneshot_semantics():
    """No interval -> oneshot plist: RunAtLoad/KeepAlive=false, no StartInterval."""
    xml = _plist_oneshot(LABEL, WRAPPER, LOG_FILE)

    assert "<key>RunAtLoad</key><false/>" in xml
    assert "<key>KeepAlive</key><false/>" in xml
    assert "<key>AbandonProcessGroup</key><true/>" in xml
    # No StartInterval key when interval is None.
    assert "StartInterval" not in xml
    # Wrapper path is rendered into ProgramArguments.
    assert f"<string>{WRAPPER}</string>" in xml
    # Both standard log paths present and point at log_file.
    assert f"<key>StandardOutPath</key><string>{LOG_FILE}</string>" in xml
    assert f"<key>StandardErrorPath</key><string>{LOG_FILE}</string>" in xml


def test_plist_oneshot_with_interval_emits_start_interval():
    """With interval=21600, plist also gets a StartInterval=21600 key."""
    xml = _plist_oneshot(LABEL, WRAPPER, LOG_FILE, interval=21600)

    # All oneshot semantics still hold.
    assert "<key>RunAtLoad</key><false/>" in xml
    assert "<key>KeepAlive</key><false/>" in xml
    assert "<key>AbandonProcessGroup</key><true/>" in xml
    # Plus the StartInterval.
    assert "<key>StartInterval</key><integer>21600</integer>" in xml


def test_plist_oneshot_output_is_well_formed_xml():
    """Output must parse as XML and have a <plist> root element."""
    xml_no_interval = _plist_oneshot(LABEL, WRAPPER, LOG_FILE)
    xml_with_interval = _plist_oneshot(LABEL, WRAPPER, LOG_FILE, interval=3600)

    for xml in (xml_no_interval, xml_with_interval):
        root = ET.fromstring(xml)
        assert root.tag == "plist", f"expected root <plist>, got {root.tag!r}"


def test_plist_oneshot_no_interval_regression_guard():
    """Regression guard: previously the interval plist was used everywhere.

    The bug we are guarding against is the default path silently including
    ``StartInterval`` when none was requested.
    """
    xml = _plist_oneshot(LABEL, WRAPPER, LOG_FILE)
    assert "StartInterval" not in xml, (
        "default plist must NOT contain StartInterval; pass interval=... "
        "explicitly when you want a periodic job"
    )


def test_plist_oneshot_label_is_rendered_inside_string_tag():
    """The Label key wraps the label in a <string> element."""
    xml = _plist_oneshot("com.example.thing", WRAPPER, LOG_FILE)
    assert "<key>Label</key><string>com.example.thing</string>" in xml


# ---------------------------------------------------------------------------
# _launchctl_kickstart_cmd
# ---------------------------------------------------------------------------


def test_launchctl_kickstart_cmd_user_domain_uses_current_uid():
    """is_user=True -> 'gui/<current-uid>/<label>' target domain."""
    cmd = _launchctl_kickstart_cmd(LABEL, is_user=True)

    assert cmd[:3] == ["launchctl", "kickstart", "-k"], cmd
    target = cmd[3]
    assert target.startswith("gui/"), target
    assert target.endswith(f"/{LABEL}"), target

    # The middle segment must be the current user id (a positive integer).
    uid_str = target[len("gui/"):-len(f"/{LABEL}")]
    assert uid_str.isdigit(), f"expected digit uid, got {uid_str!r}"
    assert int(uid_str) > 0, "uid should be a positive integer"
    # And it must match what os.getuid() reports on the running system.
    assert int(uid_str) == os.getuid()


def test_launchctl_kickstart_cmd_system_domain_skips_uid():
    """is_user=False -> 'system/<label>' target domain (no uid segment)."""
    cmd = _launchctl_kickstart_cmd(LABEL, is_user=False)

    assert cmd == ["launchctl", "kickstart", "-k", f"system/{LABEL}"], cmd