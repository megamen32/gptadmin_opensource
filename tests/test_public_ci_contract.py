"""Fast units for the actual public CI definition; not runtime acceptance."""
import json
import os
from pathlib import Path
import re
import subprocess
import shlex
import sys
import yaml

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / '.github/workflows/public-ci.yml'


def test_public_product_gate_requires_all_actual_product_jobs():
    workflow = yaml.safe_load(WORKFLOW.read_text())
    gate = workflow['jobs']['ci-gate']
    expected = {'go-unit-tests', 'admin-ui-build', 'failover-e2e',
                'installer-e2e-linux', 'candidate-client-contract', 'macos-release-packages', 'macos-build',
                'windows-shellmcp', 'public-python', 'android-dns', 'node-artifact', 'release-platform'}
    assert set(gate['needs']) == expected
    assert set(gate['needs']) == set(workflow['jobs']) - {'ci-gate'}
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
    node = jobs['node-artifact']
    assert node['strategy']['fail-fast'] is False
    assert set(node['strategy']['matrix']['platform']) == {'linux', 'darwin'}
    assert set(node['strategy']['matrix']['arch']) == {'amd64', 'arm64'}
    build = next(step for step in node['steps'] if step.get('name') == 'Build manifest-pinned production Node package')
    assert shlex.split(build['run']) == ['python3', 'tools/build_node.py', '--platform', '$PLATFORM', '--arch', '$ARCH']
    assert build['env'] == {'PLATFORM': '${{ matrix.platform }}', 'ARCH': '${{ matrix.arch }}'}
    artifact = next(step for step in node['steps'] if step.get('uses', '').startswith('actions/upload-artifact@'))
    assert artifact['with']['name'] == 'gptadmin-node-${{ matrix.platform }}-${{ matrix.arch }}-${{ github.sha }}-${{ github.run_attempt }}'
    assert artifact['with']['if-no-files-found'] == 'error'
    steps = jobs['admin-ui-build']['steps']
    ui = '\n'.join(step.get('run', '') for step in steps)
    for command in ('npm ci', 'npm test', 'npm run lint', 'npm run build'):
        assert command in ui
    candidate = '\n'.join(step.get('run', '') for step in jobs['candidate-client-contract']['steps'])
    assert '--local-origin' in candidate
    assert 'tests/e2e/systemd/run-lifecycle.sh' in candidate
    token_config = (ROOT / 'bezhook/internal/bezhook/config.go').read_text()
    assert re.search(r'(?m)^\s*DefaultToken\s*=\s*""\s*(?://.*)?$', token_config)
    assert "FRPC_TOKEN_DEFAULT = os.environ.get('GPTADMIN_FRP_TOKEN', '').strip()" in (ROOT / 'cli.py').read_text()


def test_public_go_shards_wire_every_module_on_each_platform(tmp_path):
    """Exercise shell argument wiring only; real Go execution is a separate CI gate."""
    job = yaml.safe_load(WORKFLOW.read_text())['jobs']['go-unit-tests']
    matrix = job['strategy']['matrix']
    assert job['strategy']['fail-fast'] is False
    assert set(matrix['os']) == {'ubuntu-latest', 'macos-latest'}
    assert matrix['shard'] == [0, 1, 2, 3]
    step = next(step for step in job['steps'] if step.get('name') == 'Run every test in its deterministic shard')
    assert step['env']['SHARD'] == '${{ matrix.shard }}'
    capture = tmp_path / 'arguments.json'
    shim_dir = tmp_path / 'bin'
    shim_dir.mkdir()
    shim = shim_dir / 'python3'
    shim.write_text(f'#!{sys.executable}\nimport json, os, pathlib, sys\n'
                    'pathlib.Path(os.environ["CI_CAPTURE"]).write_text(json.dumps(sys.argv[1:]))\n')
    shim.chmod(0o755)
    for runner, modules in (
        ('Linux', ['go-hub', 'go-shellmcp', 'go-proxyrelay', 'bezhook']),
        ('macOS', ['go-hub', 'go-shellmcp']),
    ):
        for index in matrix['shard']:
            env = dict(os.environ, PATH=str(shim_dir) + os.pathsep + os.environ['PATH'],
                       CI_CAPTURE=str(capture), RUNNER_OS=runner, SHARD=str(index),
                       GITHUB_WORKSPACE=str(tmp_path))
            subprocess.run(['bash', '-e', '-c', step['run']], cwd=tmp_path, env=env,
                           check=True, capture_output=True, timeout=5)
            expected = ['tools/ci_test_shards.py', 'go', '--index', str(index),
                        '--count', str(len(matrix['shard']))]
            expected += [argument for module in modules for argument in ('--module', module)]
            assert json.loads(capture.read_text()) == expected


def test_macos_cross_compile_keeps_the_full_package_gate_in_the_smoke_job(tmp_path):
    """Exercise the relocated shell command, retaining every Linux package build."""
    jobs = yaml.safe_load(WORKFLOW.read_text())['jobs']
    assert not any(step.get('name') == 'Cross-compile Linux artifacts from Mac'
                   for step in jobs['go-unit-tests']['steps'])
    job = jobs['macos-build']
    assert job['runs-on'] == 'macos-latest'
    assert job['needs'] == 'macos-release-packages'
    assert any(step.get('uses', '').startswith('actions/setup-go@') for step in job['steps'])
    step = next(step for step in job['steps']
                if step.get('name') == 'Cross-compile Linux artifacts from Mac')
    assert not step.get('continue-on-error', False)
    capture = tmp_path / 'cross-arguments.json'
    shim_dir = tmp_path / 'bin'
    shim_dir.mkdir()
    (tmp_path / 'go-hub').mkdir()
    shim = shim_dir / 'go'
    shim.write_text(f'#!{sys.executable}\nimport json, os, pathlib, sys\n'
                    'pathlib.Path(os.environ["CI_CAPTURE"]).write_text(json.dumps({'
                    '"args": sys.argv[1:], "goos": os.environ.get("GOOS"), '
                    '"goarch": os.environ.get("GOARCH")}))\n'
                    'sys.exit(int(os.environ.get("CI_GO_STATUS", "0")))\n')
    shim.chmod(0o755)
    env = dict(os.environ, PATH=str(shim_dir) + os.pathsep + os.environ['PATH'],
               CI_CAPTURE=str(capture), RUNNER_TEMP=str(tmp_path),
               GITHUB_WORKSPACE=str(tmp_path))
    subprocess.run(['bash', '-e', '-c', step['run']], cwd=tmp_path, env=env,
                   check=True, capture_output=True, timeout=5)
    actual = json.loads(capture.read_text())
    assert actual['args'][0] == 'build' and actual['args'][-1] == './...'
    assert actual['goos'] == 'linux' and actual['goarch'] == 'amd64'
    failed = subprocess.run(['bash', '-e', '-c', step['run']], cwd=tmp_path,
                            env=dict(env, CI_GO_STATUS='37'), capture_output=True, timeout=5)
    assert failed.returncode == 37


def test_public_python_scope_flags_are_in_the_same_shell_command():
    workflow = yaml.safe_load(WORKFLOW.read_text())
    script = workflow['jobs']['public-python']['steps'][-1]['run']
    commands = [line.strip() for line in script.splitlines()]
    assert not any(line.startswith(('--ignore', '--deselect')) for line in commands)
    pytest_command = next(line for line in commands
                          if line.startswith('uv run python tools/ci_test_shards.py pytest '))
    arguments = shlex.split(pytest_command)
    matrix = workflow['jobs']['public-python']['strategy']['matrix']
    assert matrix['shard'] == [0, 1]
    assert arguments[arguments.index('--count') + 1] == str(len(matrix['shard']))
    assert arguments[arguments.index('--index') + 1] == '$SHARD'
    assert arguments[arguments.index('--') + 1] == 'tests/'
    assert '--ignore=tests/test_release_workflow_contract.py' in pytest_command
    assert '--deselect=tests/test_completion_matrix.py::test_completion_matrix_commands_execute' in pytest_command


def test_node_release_publishes_only_the_exact_commit_after_all_public_gates():
    workflow = (ROOT / '.github/workflows/publish-node-release.yml').read_text()
    assert "workflows: ['Public Product CI']" in workflow
    assert "tools/wait_ci_release.py success" in workflow
    assert workflow.index('verify_ci_release_artifacts') < workflow.index('git tag -f')
    assert workflow.index('git tag -f') < workflow.index('python3 tools/publish_node_release.py')
    assert "event == 'push'" in workflow
    assert "head_branch == 'main'" in workflow
    assert 'ref: ${{ github.event.workflow_run.head_sha }}' in workflow
    assert 'Require that the tested source is still public main' in workflow
    assert 'git fetch --no-tags origin main' in workflow
    assert 'Check for an unpublished Node version' in workflow
    assert 'gh api "repos/$GITHUB_REPOSITORY/releases?per_page=100"' in workflow
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
    assert "'go-hub/'" in workflow
    assert "'go-shellmcp/'" in workflow
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


def test_public_source_layout_allows_only_curated_marketplace(tmp_path):
    import subprocess
    import sys

    workflow = yaml.safe_load(WORKFLOW.read_text())
    script = workflow['jobs']['public-python']['steps'][-1]['run']
    guard = script.split("python3 - <<'PY'\n", 1)[1].split("\nPY\n", 1)[0]
    def check():
        return subprocess.run([sys.executable, '-c', guard], cwd=tmp_path,
                              capture_output=True).returncode
    assert check() == 0
    marketplace = tmp_path / '.agents/plugins/marketplace.json'
    marketplace.parent.mkdir(parents=True)
    marketplace.write_text('{}')
    assert check() == 0
    unexpected = tmp_path / '.agents/internal.txt'
    unexpected.write_text('private')
    assert check() != 0
    unexpected.unlink()
    marketplace.unlink()
    marketplace.symlink_to(tmp_path / 'missing.json')
    assert check() != 0
    marketplace.unlink()
    marketplace.write_text('{}')
    (tmp_path / 'private').mkdir()
    assert check() != 0
