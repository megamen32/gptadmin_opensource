"""The add-on passes only the explicitly configured callback list to the hub."""
import json
import shutil
import subprocess
from pathlib import Path
import pytest

ROOT = Path(__file__).resolve().parents[1]
APP = ROOT / "deploy/server01/gptadmin_hub_standby"


def test_exact_redirects_default_to_empty_and_are_exported(tmp_path):
    if not shutil.which("jq"):
        pytest.skip("jq is required by the HAOS entrypoint")
    for name in ("public_config.yaml", "config.yaml.template"):
        source = (APP / name).read_text()
        assert "oauth_allowed_redirects: []" in source
        assert "oauth_allowed_redirects:\n    - url" in source
    line = next(line for line in (APP / "run.sh").read_text().splitlines() if line.startswith("export GPTADMIN_OAUTH_ALLOWED_REDIRECTS="))
    opts = tmp_path / "options.json"
    for data, expected in (({}, []), ({"oauth_allowed_redirects": ["https://legacy.example/cb?a=1"]}, ["https://legacy.example/cb?a=1"])):
        opts.write_text(json.dumps(data))
        script = "set -euo pipefail\nOPTS=$1\n" + line + "\nprintf %s \"$GPTADMIN_OAUTH_ALLOWED_REDIRECTS\"\n"
        result = subprocess.run(["bash", "-c", script, "test", str(opts)], capture_output=True, text=True, check=True)
        assert json.loads(result.stdout) == expected
