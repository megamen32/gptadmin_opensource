from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github" / "workflows" / "build-and-sync.yml"
BUILD_SCRIPT = ROOT / "tools" / "build.sh"


def test_windows_job_compiles_the_complete_go_hub_package():
    """Prevent Unix-only Hub code from bypassing the real Windows runner."""
    workflow = yaml.safe_load(WORKFLOW.read_text())
    steps = workflow["jobs"]["windows-shellmcp"]["steps"]
    hub_step = next(step for step in steps if step.get("name") == "Compile every Go Hub package for Windows")
    commands = hub_step["run"]

    assert hub_step["shell"] == "pwsh"
    assert "New-Item -ItemType Directory -Force build/windows" in commands
    assert "Set-Location go-hub" in commands
    assert "go test -run '^$' ./..." in commands
    assert "go build -o ../build/windows/gptadmin-hub.exe ./cmd/gptadmin-hub" in commands


def test_release_windows_package_builds_and_archives_the_go_hub():
    """The release archive must contain the Windows hub, not only ShellMCP."""
    script = BUILD_SCRIPT.read_text(encoding="utf-8")

    assert 'GOOS=windows GOARCH=amd64 go build "${GO_HUB_LDFLAGS[@]}"' in script
    assert '"$ART_DIR/windows/gptadmin-hub.exe"' in script
    assert 'zip -q -9 "../gptadmin-win.zip" gptadmin-hub.exe shellmcp.exe' in script
    assert 'cp -f public/gptadmin-win.zip public/gptadmin-win.zip.sha256 public/install_win.ps1 "$ART_DIR/public/"' in script


def test_all_archive_is_created_after_windows_payload_is_current():
    """The aggregate package must not embed a stale pre-hub Windows zip."""
    script = BUILD_SCRIPT.read_text(encoding="utf-8")
    all_block = script.split("if want all; then", 1)[1].split("else", 1)[0]

    assert all_block.index("build_windows_shellmcp") < all_block.index("archive_all")
