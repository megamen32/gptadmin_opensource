"""Fast units for the actual public CI definition; not runtime acceptance."""
import json
import os
from pathlib import Path
import subprocess
import yaml

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / '.github/workflows/public-ci.yml'


def test_public_product_gate_requires_all_actual_product_jobs():
    workflow = yaml.safe_load(WORKFLOW.read_text())
    gate = workflow['jobs']['ci-gate']
    expected = {'go-unit-tests', 'admin-ui-build', 'failover-e2e',
                'installer-e2e-linux', 'candidate-client-contract', 'macos-build',
                'windows-shellmcp', 'public-python'}
    assert set(gate['needs']) == expected
    script = gate['steps'][0]['run']
    def admitted(results):
        env = dict(os.environ, RESULTS=json.dumps(results))
        return subprocess.run(['bash', '-c', script], env=env,
                              capture_output=True, timeout=5).returncode == 0
    results = {job: {'result': 'success'} for job in expected}
    assert admitted(results)
    assert not admitted({})
    for job in expected:
        for status in ('failure', 'cancelled', 'skipped'):
            assert not admitted({**results, job: {'result': status}})


def test_public_checks_have_no_secret_input_or_private_checkout():
    text = WORKFLOW.read_text()
    assert 'secrets.' not in text
    assert 'self-hosted' not in text
    assert 'repository:' not in text
    workflow = yaml.safe_load(text)
    assert workflow['permissions'] == {'contents': 'read'}
    jobs = workflow['jobs']
    runs = '\n'.join(step.get('run', '') for step in jobs['go-unit-tests']['steps'])
    for module in ('go-hub', 'go-shellmcp', 'go-proxyrelay', 'bezhook'):
        assert f'cd {module} && go test ./...' in runs
    assert 'python3 tools/build_node.py --arch amd64' in runs
    steps = jobs['admin-ui-build']['steps']
    ui = '\n'.join(step.get('run', '') for step in steps)
    for command in ('npm ci', 'npm test', 'npm run lint', 'npm run build'):
        assert command in ui
    candidate = '\n'.join(step.get('run', '') for step in jobs['candidate-client-contract']['steps'])
    assert '--local-origin' in candidate
    assert 'tests/e2e/systemd/run-lifecycle.sh' in candidate
    assert 'DefaultToken         = ""' in (ROOT / 'bezhook/internal/bezhook/config.go').read_text()
    assert "FRPC_TOKEN_DEFAULT = os.environ.get('GPTADMIN_FRP_TOKEN', '').strip()" in (ROOT / 'cli.py').read_text()


def test_public_python_scope_flags_are_in_the_same_shell_command():
    workflow = yaml.safe_load(WORKFLOW.read_text())
    script = workflow['jobs']['public-python']['steps'][-1]['run']
    commands = [line.strip() for line in script.splitlines()]
    assert not any(line.startswith(('--ignore', '--deselect')) for line in commands)
    pytest_command = next(line for line in commands if line.startswith('uv run pytest '))
    assert '--ignore=tests/test_release_workflow_contract.py' in pytest_command
    assert '--deselect=tests/test_completion_matrix.py::test_completion_matrix_commands_execute' in pytest_command


def test_node_release_publishes_only_the_exact_commit_after_all_public_gates():
    workflow = (ROOT / '.github/workflows/publish-node-release.yml').read_text()
    assert "workflows: ['Public Product CI']" in workflow
    assert "conclusion == 'success'" in workflow
    assert "event == 'push'" in workflow
    assert "head_branch == 'main'" in workflow
    assert 'ref: ${{ github.event.workflow_run.head_sha }}' in workflow
    assert 'Require that the tested source is still public main' in workflow
    assert 'git fetch --no-tags origin main' in workflow
    assert '--source "$SOURCE_SHA"' in workflow
    assert 'contents: write' in workflow
    assert 'cancel-in-progress: false' in workflow


def test_haos_standby_release_runs_from_tested_public_source_and_uses_public_actions():
    workflow_path = ROOT / '.github/workflows/publish-haos-addon.yml'
    workflow = workflow_path.read_text()
    assert "workflows: ['Public Product CI']" in workflow
    assert "workflow_run.head_branch == 'main'" in workflow
    assert "workflow_run.head_sha" in workflow
    assert 'gptadmin-public, node-88' in workflow
    assert 'gptadmin-haos-addons.git' in workflow
    assert 'scripts/export_haos_app_repository.sh' in workflow
    assert "['git', 'clone', '--quiet', '--branch', 'main'" in workflow
    assert "['git', 'merge-base', '--is-ancestor'" in workflow
    assert "['git', 'diff', '--name-only', installed_sha, source_sha]" in workflow
    assert "'.github/workflows/publish-haos-addon.yml'" not in workflow
    assert 'secrets.' not in workflow
    source_root = ROOT if (ROOT / 'deploy/server01/gptadmin_hub_standby/public_publish_image_workflow.yml').is_file() else ROOT.parent.parent
    addon_image = (source_root / 'deploy/server01/gptadmin_hub_standby/public_publish_image_workflow.yml').read_text()
    assert 'packages: write' in addon_image
    assert 'github.token' in addon_image
    assert addon_image.index('docker/login-action') < addon_image.index('Reject an existing immutable app image version')
    assert 'SOURCE.json' in addon_image
    assert 'docker buildx build' in addon_image
    assert 'refusing to overwrite' in addon_image
    assert 'secrets.' not in addon_image
    image_config = yaml.safe_load((source_root / 'deploy/server01/gptadmin_hub_standby/public_config.yaml').read_text())
    image_workflow = yaml.safe_load(addon_image)
    dockerfile = (source_root / 'deploy/server01/gptadmin_hub_standby/Dockerfile.public').read_text()
    assert image_config['image'] == image_workflow['env']['IMAGE_NAME'] == 'ghcr.io/megamen32/gptadmin-haos-addons'
    assert 'org.opencontainers.image.source="https://github.com/megamen32/gptadmin-haos-addons"' in dockerfile
