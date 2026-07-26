from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github" / "workflows" / "build-and-sync.yml"


def test_admin_ui_build_job_is_an_explicit_runtime_release_contract():
    """Keep the React UI build and its static URL contract visible in CI."""
    workflow = yaml.safe_load(WORKFLOW.read_text())
    job = workflow["jobs"]["admin-ui-build"]
    steps = job["steps"]
    runs = "\n".join(step.get("run", "") for step in steps)
    node_setup = next(step for step in steps if step.get("name") == "Setup Node")

    assert job["runs-on"] == "ubuntu-latest"
    assert any(step.get("uses", "").startswith("actions/setup-node@") for step in steps)
    assert node_setup["with"]["node-version"] == 22
    assert node_setup["with"]["cache"] == "npm"
    assert node_setup["with"]["cache-dependency-path"] == "admin-ui/package-lock.json"
    assert "npm ci" in runs
    assert "npm test" in runs
    assert "npm run lint" in runs
    assert "npm run build" in runs
    assert "grep -q '/admin/assets/' dist/index.html" in runs
    assert "the complete operational shell at /admin/legacy/" in WORKFLOW.read_text()
    release_runs = "\n".join(
        step.get("run", "")
        for step in yaml.safe_load(WORKFLOW.read_text())["jobs"]["build-and-release"]["steps"]
    )
    assert "./tools/build.sh" in release_runs
    assert any(
        step.get("name") == "Setup Node for packaged admin UI"
        for step in yaml.safe_load(WORKFLOW.read_text())["jobs"]["build-and-release"]["steps"]
    )
