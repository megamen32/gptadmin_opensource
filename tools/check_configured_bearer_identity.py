#!/usr/bin/env python3
"""CI-only causal RED/GREEN for durable identity; no credentials or deployment."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import time

ROOT = Path(__file__).resolve().parents[1]
OUT = Path(os.environ['IDENTITY_OUTPUT']).resolve()
START = time.monotonic()
GUARD = '''\t// A configured copy of an existing durable key is not another principal.
\t// Let the original verifier retain its identity, policy and lifecycle;
\t// revoked/expired durable records must never fall back to the alias.
\tfor _, record := range s.managedMCP {
\t\tif record.TokenKind == "durable" && record.TokenDigest != "" && hmac.Equal([]byte(record.TokenDigest), []byte(digest)) {
\t\t\treturn nil, false
\t\t}
\t}
'''


def run(label, args, expected_exit=None):
    remaining = 150 - (time.monotonic() - START)
    if remaining <= 0:
        raise RuntimeError('combined focused deadline expired')
    with (OUT / (label + '.log')).open('wb') as f:
        result = subprocess.run(args, cwd=ROOT / 'go-hub', stdout=f,
                                stderr=subprocess.STDOUT, timeout=remaining)
    if expected_exit is not None and result.returncode != expected_exit:
        raise RuntimeError(label + ' unexpected exit')
    return result.returncode


def events(label):
    return [json.loads(line) for line in (OUT / (label + '.log')).read_text().splitlines()
            if line.startswith('{')]


assert os.environ.get('GITHUB_REPOSITORY') == 'megamen32/gptadmin_opensource'
assert os.environ.get('GITHUB_EVENT_NAME') == 'workflow_dispatch'
OUT.mkdir(parents=True, exist_ok=True)
source = ROOT / 'go-hub/internal/hub/server.go'
original = source.read_bytes()
text = original.decode()
assert text.count(GUARD) == 1
command = ['go', 'test', '-json', '-trimpath', '-p', '1', '-parallel', '1',
           '-count=1', '-mod=readonly', '-timeout=30s', '-run', '^TestConfigured(Bearer|OnlyBearer)', './internal/hub']
assert sys.argv[1:] in ([], ['--green-only'], ['--build-only'])
failures = set()
if not sys.argv[1:]:
    try:
        source.write_text(text.replace(GUARD, '', 1))
        assert run('identity-red', command) != 0
        failures = {e['Test'] for e in events('identity-red')
                    if e.get('Action') == 'fail' and e.get('Test')}
        assert {'TestConfiguredBearerPreservesDurableIdentity',
                'TestConfiguredBearerCannotResurrectDurableLifecycle/revoked',
                'TestConfiguredBearerCannotResurrectDurableLifecycle/expired',
                'TestConfiguredBearerPreservesDurableProfile'} <= failures
    finally:
        source.write_bytes(original)
passes = set()
if sys.argv[1:] != ['--build-only']:
    run('identity-green', command, 0)
    passes = {e['Test'] for e in events('identity-green')
              if e.get('Action') == 'pass' and e.get('Test')}
    assert len(passes) == 6
    assert any(e.get('Action') == 'pass' and not e.get('Test') for e in events('identity-green'))
    run('identity-vet', ['go', 'vet', '-trimpath', '-p', '1', './internal/hub'], 0)
commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
version = (ROOT / 'VERSION').read_text().strip()
flags = '-s -w'
for package in ['go-hub/internal/hub', 'go-shellmcp/internal/server']:
    flags += f' -X github.com/megamen32/gptadmin/{package}.BuildVersion={version}'
    flags += f' -X github.com/megamen32/gptadmin/{package}.GitCommit={commit}'
binary = OUT / 'gptadmin-node'
run('identity-build', ['go', 'build', '-trimpath', '-p', '1', '-mod=readonly',
                      '-ldflags', flags, '-o', str(binary), './cmd/gptadmin-node'], 0)
# Compiler objects only, never accept cached tests; cap the reusable carrier.
cache = Path(os.environ['GOCACHE']).resolve()
cache_bytes = cache_files = 0
with tarfile.open(OUT / 'compiler-objects.tgz', 'w:gz') as archive:
    for p in sorted(cache.rglob('*')):
        if not p.is_file() or p.is_symlink():
            continue
        with p.open('rb') as f:
            if f.read(4) == b'\x7fELF':
                continue
        cache_bytes += p.stat().st_size
        cache_files += 1
        assert cache_bytes <= 256 * 1024**2 and cache_files <= 10000
        archive.add(p, arcname=str(p.relative_to(cache)), recursive=False)
receipt = {'status': 'PASS', 'source': commit, 'category': 'fast unit',
           'expected_seconds': 120, 'maximum_seconds': 150,
           'elapsed_seconds': time.monotonic() - START,
           'red_failures': sorted(failures), 'green_passes': sorted(passes),
           'red_in_this_run': not sys.argv[1:],
           'green_in_this_run': sys.argv[1:] != ['--build-only'],
           'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
           'compiler_bytes': cache_bytes, 'compiler_files': cache_files,
           'runtime_acceptance': False, 'model_calls': 0, 'secret_inputs': False}
assert receipt['elapsed_seconds'] < 150
(OUT / 'identity-proof.json').write_text(json.dumps(receipt, indent=2) + '\n')
print(json.dumps(receipt))
