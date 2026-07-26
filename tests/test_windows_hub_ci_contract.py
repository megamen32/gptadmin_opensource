from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github" / "workflows" / "build-and-sync.yml"


def test_windows_job_compiles_the_complete_go_hub_package():
    """Prevent Unix-only Hub code from bypassing the real Windows runner."""
    workflow = yaml.safe_load(WORKFLOW.read_text())
    steps = workflow["jobs"]["windows-shellmcp"]["steps"]
    hub_step = next(step for step in steps if step.get("name") == "Compile every Go Hub package for Windows")
    commands = hub_step["run"]

    assert hub_step["shell"] == "pwsh"
    assert "Set-Location go-hub" in commands
    assert "go test -run '^$' ./..." in commands
    assert "go build -o ../build/windows/gptadmin-hub.exe ./cmd/gptadmin-hub" in commands
