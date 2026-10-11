"""Guard tests only; these do not claim updater/deployment integration."""
import importlib.util
import os
from pathlib import Path
import subprocess
import sys

import pytest

ROOT = Path(__file__).resolve().parents[1]
CLI_CANDIDATE = Path(os.environ.get('GPTADMIN_GUARD_TEST_CLI', str(ROOT / 'cli.py')))
spec = importlib.util.spec_from_file_location('cli_unified_guard', ROOT / 'cli.py')
cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cli)


def snapshot(root):
    return {str(p.relative_to(root)): (p.read_bytes(), p.stat().st_mtime_ns)
            for p in root.rglob('*') if p.is_file()}


@pytest.mark.parametrize('mode,location', [('writer', 'base'), ('reader', 'primary')])
@pytest.mark.parametrize('options', [['--auto'], [], ['--force', '--no-shellmcp']])
def test_real_cli_rejects_unified_before_update_writes(tmp_path, mode, location, options):
    config = tmp_path / 'config'
    config.mkdir()
    text = 'INSTALL_HUB=true\nGPTADMIN_AUTO_UPDATE=false\n'
    if location == 'base':
        text += f'GPTADMIN_AUTH_MODE={mode}\n'
    else:
        (config / 'nodes').mkdir()
        (config / 'nodes' / 'primary.env').write_text(f'GPTADMIN_AUTH_MODE="{mode}"\n')
    (config / 'gptadmin.env').write_text(text)
    env = dict(os.environ, GPTADMIN_INSTALL_MODE='user', GPTADMIN_USER_HOME=str(tmp_path / 'user'),
               GPTADMIN_CONFIG_DIR=str(config), GPTADMIN_HOME=str(tmp_path / 'install'),
               GPTADMIN_CLI_PATH=str(tmp_path / 'bin' / 'gptadmin'),
               TMPDIR=str(tmp_path), PYTHONDONTWRITEBYTECODE='1')
    env.pop('GPTADMIN_AUTH_MODE', None)
    before = snapshot(tmp_path)
    # The original RED used --auto with automatic updates disabled, safely
    # exiting before download. Manual/force cases exercise the repaired guard.
    result = subprocess.run([sys.executable, str(CLI_CANDIDATE), '--user', 'update', *options],
                            env=env, capture_output=True, text=True, timeout=10)
    assert result.returncode != 0 and 'unified node' in result.stderr.lower(), result.stdout + result.stderr
    assert snapshot(tmp_path) == before
    assert not (tmp_path / 'install').exists()


@pytest.mark.parametrize('executable,blocked', [('/opt/gptadmin/bin/gptadmin-node', True),
                                               ('/opt/gptadmin/bin/gptadmin_hub', False)])
def test_effective_systemd_execstart_unit_only(tmp_path, monkeypatch, executable, blocked):
    """Synthetic systemctl output tests detection only, not service integration."""
    env_file = tmp_path / 'gptadmin.env'
    env_file.write_text('GPTADMIN_AUTH_MODE=legacy\n')
    unit = tmp_path / 'gptadmin-hub.service'
    unit.write_text('[Service]\nExecStart=/opt/gptadmin/bin/gptadmin_hub\n')
    monkeypatch.setattr(cli, 'ENV_FILE', env_file)
    monkeypatch.setattr(cli, 'UNIT_PATH_HUB', unit)
    monkeypatch.setattr(cli, 'IS_MACOS', False)
    monkeypatch.setattr(cli, 'IS_USER_INSTALL', False)
    monkeypatch.delenv('GPTADMIN_AUTH_MODE', raising=False)
    calls = []
    def inspect(command, **kwargs):
        calls.append(command)
        return subprocess.CompletedProcess(command, 0, stdout='{ path=' + executable + ' ; argv[]=' + executable + ' ; }')
    monkeypatch.setattr(cli.subprocess, 'run', inspect)
    if blocked:
        with pytest.raises(SystemExit):
            cli._reject_legacy_update_for_unified_node()
    else:
        cli._reject_legacy_update_for_unified_node()
    assert calls == [['systemctl', 'show', 'gptadmin-hub.service', '--property=ExecStart', '--value']]


def test_guard_precedes_transaction_snapshot(tmp_path, monkeypatch):
    env_file = tmp_path / 'gptadmin.env'
    env_file.write_text('GPTADMIN_AUTH_MODE=writer\n')
    monkeypatch.setattr(cli, 'ENV_FILE', env_file)
    monkeypatch.setattr(cli, 'need_root', lambda: None)
    def forbidden(*args, **kwargs):
        raise AssertionError('update crossed the transaction boundary')
    monkeypatch.setattr(cli, '_UpdateRuntimeSnapshot', forbidden)
    with pytest.raises(SystemExit):
        cli._transactional_update(forbidden)(None)


def test_unrelated_canary_config_does_not_select_primary(tmp_path, monkeypatch):
    env_file = tmp_path / 'gptadmin.env'
    env_file.write_text('GPTADMIN_AUTH_MODE=legacy\n')
    (tmp_path / 'nodes').mkdir()
    (tmp_path / 'nodes' / 'canary.env').write_text('GPTADMIN_AUTH_MODE=reader\n')
    monkeypatch.setattr(cli, 'ENV_FILE', env_file)
    monkeypatch.setattr(cli, 'UNIT_PATH_HUB', tmp_path / 'absent.unit')
    monkeypatch.delenv('GPTADMIN_AUTH_MODE', raising=False)
    cli._reject_legacy_update_for_unified_node()


def test_update_command_skips_hint_before_guard(monkeypatch):
    from types import SimpleNamespace
    monkeypatch.setattr(cli, 'env_read', lambda: {'GPTADMIN_AUTO_UPDATE': 'false'})
    def forbidden(*args, **kwargs):
        raise AssertionError('update hint touched cache/network before the guard')
    monkeypatch.setattr(cli, '_read_update_cache', forbidden)
    cli.maybe_update_hint(SimpleNamespace(cmd='update', auto=False))
