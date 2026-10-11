#!/usr/bin/env python3
"""Real one-shot executable and native filesystem-lock bootstrap validation."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path)
    binary = parser.parse_args().binary.resolve()
    project = Path(__file__).resolve().parents[3]
    root = Path(tempfile.mkdtemp(prefix='auth-export-canary-', dir=project / '.tmp'))
    source = root / 'legacy'
    source.mkdir()
    ordinary, owner = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
    stores = {
        'mcp_tokens_state.json': {'tokens': {
            'ordinary': {'id': 'ordinary', 'client_id': 'ordinary', 'role': 'client',
                'token_value': ordinary, 'token_kind': 'configured_bearer', 'revoked_at': 42},
            'control': {'id': 'control', 'token_kind': 'legacy_ctl', 'token_value': owner},
            'alias': {'id': 'alias', 'token_kind': 'configured_bearer', 'token_digest': hashlib.sha256(owner.encode()).hexdigest()}}},
        'oauth_clients_state.json': {'clients': {}},
        'access_profiles_state.json': {'profiles': {}},
    }
    for name, content in stores.items():
        path = source / name
        path.write_text(json.dumps(content))
        path.chmod(0o600)
        (source / (name + '.lock')).touch(mode=0o600)
    def state():
        return {p.name: (p.read_bytes(), p.stat().st_mode, p.stat().st_mtime_ns) for p in source.iterdir()}
    before = state()
    env = dict(os.environ, CTL_TOKEN=owner)
    def command(output):
        return [str(binary), '--config-dir', str(source), '--writer-id', 'existing-primary-id', '--output', str(output)]
    # Every actual native writer lock must delay the one-shot until released.
    for index, name in enumerate(stores):
        output = root / f'snapshot-{index}.json'
        with (source / (name + '.lock')).open('rb') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            proc = subprocess.Popen(command(output), env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                time.sleep(.2)
                assert proc.poll() is None and not output.exists(), f'export bypassed native {name} lock'
            finally:
                fcntl.flock(lock, fcntl.LOCK_UN)
            stdout, stderr = proc.communicate(timeout=8)
        assert proc.returncode == 0 and not stdout
        assert owner.encode() not in stderr and ordinary.encode() not in stderr
        assert output.stat().st_mode & 0o777 == 0o600
        bundle = json.loads(output.read_bytes())
        assert bundle['writer_id'] == 'existing-primary-id' and bundle['generation'] == 1
        assert list(bundle['tokens']['tokens']) == ['ordinary']
        assert bundle['tokens']['tokens']['ordinary']['revoked_at'] == 42
        assert bundle['tokens']['tokens']['ordinary']['token_value'] == ordinary
        assert state() == before, 'export changed authoritative source'
        failed = subprocess.run(command(output), env=env, capture_output=True)
        assert failed.returncode != 0 and not failed.stdout, 'existing output overwritten'
    # No lock creation, symlink replacement, output in source, or enabled-store fallback.
    missing = source / 'mcp_tokens_state.json.lock'
    missing.unlink()
    denied = root / 'denied.json'
    assert subprocess.run(command(denied), env=env, capture_output=True).returncode != 0
    assert not denied.exists() and not missing.exists()
    missing.touch(mode=0o600)
    link = root / 'linked-output.json'
    link.symlink_to(source / 'mcp_tokens_state.json')
    assert subprocess.run(command(link), env=env, capture_output=True).returncode != 0
    assert subprocess.run(command(source / 'output.json'), env=env, capture_output=True).returncode != 0
    (source / 'auth-continuity').mkdir()
    assert subprocess.run(command(denied), env=env, capture_output=True).returncode != 0
    print(json.dumps({'ok': True, 'native_locks_checked': len(stores), 'source_unchanged': True,
        'private_output_only': True, 'revocation_preserved': True, 'ctl_excluded': True,
        'generation': 1, 'evidence_dir': str(root)}))


if __name__ == '__main__':
    main()
