"""Local subprocess exit/cache orchestration; real failover runs in the public workflow."""
import os
from pathlib import Path
import subprocess
import sys
import pytest

ROOT = Path(__file__).resolve().parents[1]


@pytest.mark.parametrize("suite,scenario_exit,cache_exit,expected", [
    ("member-outage", 0, 9, 0),
    ("member-outage", 37, 0, 37),
    ("quorum-storage", 0, 0, 0),
    ("legacy", 19, 0, 19),
])
def test_cache_cannot_mask_scenario_result(tmp_path, suite, scenario_exit, cache_exit, expected):
    docker = tmp_path / "docker"
    docker.write_text("#!" + sys.executable + "\n" + """import os, sys, time
if sys.argv[1] == 'buildx':
    time.sleep(0.05)
    print('fixture cache exporter')
    sys.exit(int(os.environ['CI_TEST_CACHE_STATUS']))
if sys.argv[1] == 'run':
    print('fixture scenario')
    sys.exit(int(os.environ['CI_TEST_SCENARIO_STATUS']))
sys.exit(71)
""")
    docker.chmod(0o755)
    env = dict(os.environ, PATH=str(tmp_path) + os.pathsep + os.environ["PATH"],
               GITHUB_SHA="1" * 40, RUNNER_TEMP=str(tmp_path),
               ACTIONS_RUNTIME_TOKEN="fixture-job-token",
               CI_TEST_CACHE_STATUS=str(cache_exit), CI_TEST_SCENARIO_STATUS=str(scenario_exit))
    result = subprocess.run(["bash", str(ROOT / "tools/ci_failover.sh"), suite],
                            env=env, capture_output=True, text=True, timeout=5)
    assert result.returncode == expected, result.stdout + result.stderr
    assert "fixture scenario" in result.stdout


def test_unknown_suite_fails_before_docker(tmp_path):
    env = dict(os.environ, GITHUB_SHA="1" * 40, RUNNER_TEMP=str(tmp_path))
    result = subprocess.run(["bash", str(ROOT / "tools/ci_failover.sh"), "unknown"],
                            env=env, capture_output=True, text=True, timeout=5)
    assert result.returncode == 2
    assert "Unknown suite" in result.stderr


def test_cache_runtime_exports_only_job_cache_fields(tmp_path):
    candidates = [ROOT / ".gitpublic/public/.github/actions/cache-runtime/main.js",
                  ROOT / ".github/actions/cache-runtime/main.js"]
    action = next(path for path in candidates if path.is_file())
    destination = tmp_path / "job.env"
    env = {key: value for key, value in os.environ.items() if not key.startswith("ACTIONS_")}
    env.update(GITHUB_ENV=str(destination), ACTIONS_RUNTIME_TOKEN="fixture-token",
               ACTIONS_RESULTS_URL="https://cache.example.invalid",
               ACTIONS_UNRELATED="must-not-export")
    result = subprocess.run(["node", str(action)], env=env, capture_output=True, text=True, timeout=5)
    assert result.returncode == 0, result.stderr
    assert result.stdout == "::add-mask::fixture-token\n"
    assert destination.read_text().splitlines() == [
        "ACTIONS_RUNTIME_TOKEN=fixture-token", "ACTIONS_RESULTS_URL=https://cache.example.invalid"]
