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
