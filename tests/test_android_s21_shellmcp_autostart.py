import json
import os
from pathlib import Path
import subprocess


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "deploy" / "android-s21-shellmcp-autostart.sh"
SERVICE = ROOT / "deploy" / "systemd" / "android-s21-shellmcp-autostart.service"
TIMER = ROOT / "deploy" / "systemd" / "android-s21-shellmcp-autostart.timer"


def test_android_maintainer_is_exact_device_and_single_owner_only() -> None:
    script = SCRIPT.read_text(encoding="utf-8")

    assert "R5CR702SRFP" in script
    assert "ANDROID_SHELLMCP_BASE:-/data/local/tmp/gptadmin" in script
    assert 'RUN="$BASE/run.sh"' in script
    assert "parent_count" in script
    assert "child_count" in script
    assert "status=noop" in script
    assert "status=started" in script
    assert "status=waiting" in script
    assert "duplicate" in script
    assert "ps -A -o USER,PID,PPID,ARGS" not in script
    assert "ps -A -o PID,PPID,ARGS" in script
    assert "ps -A -o PID,PPID,NAME" in script
    assert r'readlink \"/proc/\$child_pid/exe\"' in script
    assert "canonical_child_count" in script
    assert "exact_executable_count" in script


def test_android_maintainer_forbids_adjacent_phone_mutations() -> None:
    script = SCRIPT.read_text(encoding="utf-8")
    forbidden = (
        "settings put",
        "appops",
        "deviceidle",
        "adb reverse",
        " reverse ",
        "sed -i",
        "com.termux.boot",
        "android4gproxy",
        "SHELLMCP_TOKEN",
    )

    for fragment in forbidden:
        assert fragment not in script


def test_android_owner_is_boot_persistent_system_timer() -> None:
    service = SERVICE.read_text(encoding="utf-8")
    timer = TIMER.read_text(encoding="utf-8")

    assert "Type=oneshot" in service
    assert "User=root" in service
    assert "/usr/local/libexec/gptadmin/android-s21-shellmcp-autostart.sh" in service
    assert "OnBootSec=" in timer
    assert "OnUnitActiveSec=" in timer
    assert "Persistent=true" in timer
    assert "WantedBy=timers.target" in timer


def make_fake_adb(tmp_path: Path) -> tuple[Path, Path, Path]:
    state = tmp_path / "state"
    calls = tmp_path / "calls"
    fake = tmp_path / "adb"
    fake.write_text(
        """#!/usr/bin/env bash
set -eu
printf '%s\\n' "$*" >>"$FAKE_ADB_CALLS"
if [[ "${1:-}" == "devices" ]]; then
  printf 'List of devices attached\\n'
  if [[ "$(cat "$FAKE_ADB_STATE")" != "unavailable" ]]; then
    printf 'R5CR702SRFP\\tdevice\\n'
  fi
  exit 0
fi
if [[ "${1:-}" == "-s" && "${3:-}" == "shell" ]]; then
  command="${4:-}"
  if [[ "$command" == *"parent_count="* ]]; then
    case "$(cat "$FAKE_ADB_STATE")" in
      healthy) printf '1 1 1\\n' ;;
      healthy_plus_orphan) printf '1 1 2\\n' ;;
      wrong_parent|wrong_executable)
        if [[ "$command" == *"canonical_child_count"* ]]; then
          printf '1 0 1\\n'
        else
          printf '1 1 1\\n'
        fi
        ;;
      *) printf '0 0 0\\n' ;;
    esac
  else
    printf 'healthy\\n' >"$FAKE_ADB_STATE"
  fi
  exit 0
fi
exit 2
""",
        encoding="utf-8",
    )
    fake.chmod(0o755)
    return fake, state, calls


def run_with_fake_adb(tmp_path: Path, initial_state: str) -> subprocess.CompletedProcess[str]:
    fake, state, calls = make_fake_adb(tmp_path)
    state.write_text(initial_state, encoding="utf-8")
    env = {
        **os.environ,
        "ADB_BIN": str(fake),
        "ADB_RUN_AS_USER": "0",
        "FAKE_ADB_STATE": str(state),
        "FAKE_ADB_CALLS": str(calls),
        "AUTOSTART_RECEIPT_DIR": str(tmp_path / "receipts"),
    }
    return subprocess.run([str(SCRIPT)], env=env, text=True, capture_output=True, check=False)


def test_android_healthy_owner_is_noop(tmp_path: Path) -> None:
    result = run_with_fake_adb(tmp_path, "healthy")

    assert result.returncode == 0, result.stderr
    assert "status=noop" in result.stdout
    receipt = json.loads((tmp_path / "receipts" / "latest.json").read_text())
    assert receipt["status"] == "noop"
    assert receipt["parent_count"] == receipt["child_count"] == 1


def test_android_missing_owner_is_started_once(tmp_path: Path) -> None:
    result = run_with_fake_adb(tmp_path, "missing")

    assert result.returncode == 0, result.stderr
    assert "status=started" in result.stdout
    assert (tmp_path / "state").read_text().strip() == "healthy"
    calls = (tmp_path / "calls").read_text()
    assert calls.count("nohup '/data/local/tmp/gptadmin/run.sh'") == 1
    assert all(fragment not in calls for fragment in ("settings put", "appops", "deviceidle", "reverse"))


def test_android_wrong_parent_child_is_not_accepted_as_healthy(tmp_path: Path) -> None:
    result = run_with_fake_adb(tmp_path, "wrong_parent")

    assert result.returncode == 0, result.stderr
    assert "status=started" in result.stdout
    assert "status=noop" not in result.stdout


def test_android_wrong_executable_is_not_accepted_as_healthy(tmp_path: Path) -> None:
    result = run_with_fake_adb(tmp_path, "wrong_executable")

    assert result.returncode == 0, result.stderr
    assert "status=started" in result.stdout
    assert "status=noop" not in result.stdout


def test_android_healthy_pair_plus_orphan_exact_binary_is_reconciled(tmp_path: Path) -> None:
    result = run_with_fake_adb(tmp_path, "healthy_plus_orphan")

    assert result.returncode == 0, result.stderr
    assert "status=started" in result.stdout
    assert "status=noop" not in result.stdout


def test_android_unavailable_device_waits_without_mutation(tmp_path: Path) -> None:
    result = run_with_fake_adb(tmp_path, "unavailable")

    assert result.returncode == 0, result.stderr
    assert "status=waiting" in result.stdout
    assert " -s " not in (tmp_path / "calls").read_text()
