#!/usr/bin/env python3
"""Publish accepted public Node artifacts without changing the legacy latest release."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile
import time

REPOSITORY = 'megamen32/gptadmin_opensource'
FILES = {'bin/gptadmin-node', 'cli.py', 'scripts/gptadmin_node_manage.py',
         'scripts/gptadmin_node_probe.py'}
MAX_PACKAGE = 128 * 1024**2
MAX_PAYLOAD = 256 * 1024**2
DEADLINE = None


def digest(path):
    h = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(1024**2), b''):
            h.update(block)
    return h.hexdigest()


def verify_package(path, arch, source):
    path = Path(path)
    if path.is_symlink() or not path.is_file() or path.stat().st_size > MAX_PACKAGE:
        raise ValueError('invalid bounded Node package')
    with tarfile.open(path, 'r:gz') as archive:
        members = {}
        size = 0
        for member in archive:
            size += member.size
            if (not member.isfile() or member.name in members
                    or member.name not in FILES | {'manifest.json'}
                    or size > MAX_PAYLOAD or len(members) >= 5):
                raise ValueError('invalid Node archive closure')
            members[member.name] = member
        if set(members) != FILES | {'manifest.json'} or members['manifest.json'].size > 65536:
            raise ValueError('incomplete Node archive closure')
        manifest = json.load(archive.extractfile(members['manifest.json']))
        if (manifest.get('schema') != 'gptadmin.node-package/v1'
                or manifest.get('platform') != 'linux' or manifest.get('arch') != arch
                or manifest.get('git_commit') != source or manifest.get('source_dirty') is not False
                or set(manifest.get('files', {})) != FILES
                or type(manifest.get('build_version')) is not int or manifest['build_version'] < 1):
            raise ValueError('Node manifest does not bind accepted source')
        for name in FILES:
            h = hashlib.sha256()
            with archive.extractfile(members[name]) as stream:
                for block in iter(lambda: stream.read(1024**2), b''):
                    h.update(block)
            if manifest['files'][name] != {'sha256': h.hexdigest(), 'size': members[name].size}:
                raise ValueError('Node payload differs from manifest')
    checksum = path.with_name(path.name + '.sha256')
    if checksum.is_symlink() or checksum.stat().st_size > 256:
        raise ValueError('invalid Node checksum file')
    if checksum.read_text().strip() != digest(path) + '  ' + path.name:
        raise ValueError('Node archive checksum mismatch')
    return manifest


def gh(*args):
    remaining = 150 if DEADLINE is None else DEADLINE - time.monotonic()
    if remaining <= 0:
        raise RuntimeError('bounded Node publication deadline')
    result = subprocess.run(['gh', *args], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=min(90, remaining), check=False)
    if result.returncode:
        raise RuntimeError('GitHub Node publication failed')
    return result.stdout


def asset_matches(asset, path):
    return (asset.get('name') == path.name and asset.get('size') == path.stat().st_size
            and asset.get('digest') == 'sha256:' + digest(path))


def verify_tag(repository, tag, source, allow_missing=False):
    refs = json.loads(gh('api', 'repos/' + repository + '/git/matching-refs/tags/' + tag))
    exact = [r for r in refs if r.get('ref') == 'refs/tags/' + tag]
    if not exact and allow_missing:
        return
    if len(exact) != 1:
        raise ValueError('Node release tag is missing or ambiguous')
    obj = exact[0]['object']
    for _ in range(4):
        sha = obj.get('sha')
        if not isinstance(sha, str) or not re.fullmatch('[0-9a-f]{40}', sha):
            raise ValueError('invalid Node tag object')
        if obj.get('type') == 'commit':
            if sha != source:
                raise ValueError('Node release tag differs from accepted source; never rewrite')
            return
        if obj.get('type') != 'tag':
            raise ValueError('Node release tag does not resolve to a commit')
        obj = json.loads(gh('api', 'repos/' + repository + '/git/tags/' + sha))['object']
    raise ValueError('unbounded annotated Node tag chain')


def publish(directory, source, repository):
    global DEADLINE
    DEADLINE = time.monotonic() + 150
    if repository != REPOSITORY or not re.fullmatch('[0-9a-f]{40}', source):
        raise ValueError('only the approved public source/repository may publish')
    directory = Path(directory).resolve()
    packages = [directory / ('gptadmin-node-linux-' + arch + '.tar.gz') for arch in ('amd64', 'arm64')]
    manifests = [verify_package(path, arch, source) for path, arch in zip(packages, ('amd64', 'arm64'))]
    if manifests[0]['build_version'] != manifests[1]['build_version']:
        raise ValueError('Node architecture versions disagree')
    version = manifests[0]['build_version']
    tag = 'node-v' + str(version) + '-' + source[:12]
    assets = [p for path in packages for p in (path, path.with_name(path.name + '.sha256'))]
    verify_tag(repository, tag, source, allow_missing=True)
    # Listing distinguishes an absent release from API/auth failure. Never
    # interpret failed authentication as permission to create another release.
    releases = json.loads(gh('api', 'repos/' + repository + '/releases?per_page=100'))
    release = next((r for r in releases if r.get('tag_name') == tag), None)
    if release is None:
        gh('release', 'create', tag, '--repo', repository, '--target', source,
           '--draft', '--latest=false', '--title', 'GPTAdmin Node ' + str(version),
           '--notes', 'Manifest-verified Linux amd64/arm64 Node binaries. Source: ' + source
           + '. Separate Node channel; the full-platform latest release is preserved.')
        release = json.loads(gh('api', 'repos/' + repository + '/releases/tags/' + tag))
    verify_tag(repository, tag, source)
    present = {a['name']: a for a in release.get('assets', [])}
    if set(present) - {p.name for p in assets}:
        raise ValueError('unexpected asset in immutable Node release')
    for path in assets:
        if path.name in present:
            if not asset_matches(present[path.name], path):
                raise ValueError('existing Node release asset differs; never overwrite')
        else:
            if not release.get('draft'):
                raise ValueError('published Node release is incomplete; never mutate it')
            gh('release', 'upload', tag, str(path), '--repo', repository)
    final = json.loads(gh('api', 'repos/' + repository + '/releases/tags/' + tag))
    present = {a['name']: a for a in final.get('assets', [])}
    if set(present) != {p.name for p in assets} or not all(asset_matches(present[p.name], p) for p in assets):
        raise ValueError('uploaded Node release digests differ')
    verify_tag(repository, tag, source)
    if final.get('draft'):
        gh('release', 'edit', tag, '--repo', repository, '--draft=false', '--latest=false')
    print(json.dumps({'published': True, 'tag': tag, 'source': source, 'assets': [p.name for p in assets],
                      'legacy_latest_preserved': True}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', required=True, type=Path)
    parser.add_argument('--source', required=True)
    parser.add_argument('--repository', required=True)
    args = parser.parse_args()
    publish(args.directory, args.source, args.repository)
