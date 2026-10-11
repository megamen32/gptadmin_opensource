"""Real generator filesystem tests; no service installation or restart."""
import os
from pathlib import Path
import subprocess
import sys

import pytest


ROOT = Path(__file__).resolve().parents[1]
CANDIDATE = Path(os.environ.get('GPTADMIN_GUARD_TEST_CLI', str(ROOT / 'cli.py')))


@pytest.mark.parametrize('mode,location,unified', [
    ('writer', 'base', True), ('reader', 'primary', True),
    ('legacy', 'base', False), ('reader', 'canary', False),
])
def test_actual_frpc_unit_generation(tmp_path, mode, location, unified):
    config = tmp_path / 'config'
    config.mkdir()
    (config / 'gptadmin.env').write_text('GPTADMIN_AUTH_MODE=' + (mode if location == 'base' else 'legacy') + '\n')
    if location != 'base':
        (config / 'nodes').mkdir()
        (config / 'nodes' / (location + '.env')).write_text(f'GPTADMIN_AUTH_MODE="{mode}"\n')
    env = dict(os.environ, GPTADMIN_INSTALL_MODE='user', GPTADMIN_USER_HOME=str(tmp_path / 'user'),
        GPTADMIN_CONFIG_DIR=str(config), GPTADMIN_HOME=str(tmp_path / 'install'),
        GPTADMIN_CLI_PATH=str(tmp_path / 'bin/gptadmin'), PYTHONDONTWRITEBYTECODE='1')
    env.pop('GPTADMIN_AUTH_MODE', None)
    code = '''
import importlib.util, pathlib, sys
spec = importlib.util.spec_from_loader('candidate', loader=None)
cli = importlib.util.module_from_spec(spec)
exec(compile(pathlib.Path(sys.argv[1]).read_text(), sys.argv[1], 'exec'), cli.__dict__)
cli.BIN_DIR.mkdir(parents=True, exist_ok=True)
cli.write_frpc_unit('/nonexistent-test-frpc')
print(cli.UNIT_PATH_FRPC.read_text())
'''
    result = subprocess.run([sys.executable, '-c', code, str(CANDIDATE)], env=env,
        capture_output=True, text=True, timeout=10)
    assert result.returncode == 0, result.stderr
    if unified:
        assert 'gptadmin-hub.service' not in result.stdout
        assert 'After=network-online.target\n' in result.stdout
    else:
        assert 'BindsTo=gptadmin-hub.service' in result.stdout
        assert 'After=network-online.target gptadmin-hub.service' in result.stdout
