#!/usr/bin/env python3
"""Publish one verified full release from the tested sanitized public commit."""
from __future__ import annotations

import argparse
import json
import re
import subprocess
from pathlib import Path

try:
    from . import publish_node_release as node
    from . import verify_macos_release as macos
except ImportError:
    import publish_node_release as node
    import verify_macos_release as macos


REPOSITORY = 'megamen32/gptadmin_opensource'


def checked_assets(directory: Path, source: str, version: int) -> list[Path]:
    matrix_path = directory / 'gptadmin-release-matrix.json'
    checksums_path = directory / 'gptadmin-checksums.txt'
    matrix = json.loads(matrix_path.read_text())
    if (matrix.get('schema') != 'gptadmin.release-matrix/v2'
            or matrix.get('git_commit') != source or matrix.get('build_version') != version):
        raise ValueError('release matrix is not bound to the approved public source and VERSION')
    expected = {
        f'gptadmin-{platform}-{arch}-{edition}.{extension}'
        for platform, extension in (('windows', 'zip'), ('macos', 'tar.gz'),
                                    ('ubuntu', 'tar.gz'), ('android', 'tar.gz'))
        for arch in ('x64', 'arm64') for edition in ('full', 'client')
    }
    entries = matrix.get('artifacts')
    if not isinstance(entries, list) or len(entries) != len(expected):
        raise ValueError('release matrix does not contain exactly 16 bundles')
    if {entry.get('file') for entry in entries} != expected:
        raise ValueError('release bundle names do not match the full matrix')
    checksums = {}
    for line in checksums_path.read_text().splitlines():
        parts = line.split()
        if len(parts) != 2:
            raise ValueError('invalid checksum entry')
        name = parts[1].lstrip('*')
        if name.startswith('build/'):
            name = name[6:]
        if name not in expected or name in checksums:
            raise ValueError('unexpected or duplicated checksum entry')
        checksums[name] = parts[0]
    if set(checksums) != expected:
        raise ValueError('checksum list does not cover the release matrix')
    assets = []
    for entry in entries:
        name = entry['file']
        match = re.fullmatch(r'gptadmin-(windows|macos|ubuntu|android)-(x64|arm64)-(full|client)\.(zip|tar\.gz)', name)
        if not match or (entry.get('platform'), entry.get('arch'), entry.get('edition')) != match.group(1, 2, 3):
            raise ValueError('release matrix target disagrees with filename')
        path = directory / name
        if path.is_symlink() or not path.is_file():
            raise ValueError('missing or unsafe release bundle')
        checksum = node.digest(path)
        if (entry.get('sha256') != checksum or entry.get('size') != path.stat().st_size
                or checksums[name] != checksum):
            raise ValueError('release archive digest or size mismatch')
        assets.append(path)
    macos.verify(directory, 1074)
    return sorted(assets) + [checksums_path, matrix_path]


def verify_ci_and_node(repository: str, source: str, private_source: str,
                       version: int, ci_run: int, node_dir: Path) -> None:
    if repository != REPOSITORY or not re.fullmatch(r'[0-9a-f]{40}', source):
        raise ValueError('invalid public source or repository')
    if not re.fullmatch(r'[0-9a-f]{40}', private_source):
        raise ValueError('invalid approved private Source-Commit')
    node.public_mirror_commit(repository, source, source)
    response = json.loads(node.gh('api', f'repos/{repository}/git/commits/{source}'))
    lines = [line.strip() for line in response['message'].splitlines() if line.strip()]
    if not lines or lines[-1] != 'Source-Commit: ' + private_source:
        raise ValueError('sanitized snapshot does not bind the approved private Source-Commit')
    current = json.loads(node.gh('api', f'repos/{repository}/git/ref/heads/main'))
    if current.get('object', {}).get('sha') != source:
        raise ValueError('tested public source is no longer main')
    run = json.loads(node.gh('api', f'repos/{repository}/actions/runs/{ci_run}'))
    if (run.get('name') != 'Public Product CI' or run.get('head_sha') != source
            or run.get('event') != 'push' or run.get('head_branch') != 'main'
            or run.get('status') != 'completed' or run.get('conclusion') != 'success'):
        raise ValueError('public product CI did not pass for the exact source push')
    jobs = json.loads(node.gh('api', f'repos/{repository}/actions/runs/{ci_run}/jobs?per_page=100'))['jobs']
    for name in ('Public product CI gate', 'Verify both macOS ShellMCP release packages'):
        if not any(job.get('name') == name and job.get('conclusion') == 'success' for job in jobs):
            raise ValueError('public CI did not pass required job: ' + name)
    tag = f'node-v{version}-{source[:12]}'
    if node.tag_commit(repository, tag) != source:
        raise ValueError('Node release tag does not bind the tested public source')
    release = json.loads(node.gh('api', f'repos/{repository}/releases/tags/{tag}'))
    if release.get('draft') or release.get('prerelease') or release.get('tag_name') != tag:
        raise ValueError('matching Node release is not published')
    targets = [(platform, arch) for platform in ('linux', 'darwin')
               for arch in ('amd64', 'arm64')]
    packages = [(node_dir / f'gptadmin-node-{platform}-{arch}.tar.gz', platform, arch)
                for platform, arch in targets]
    assets = [p for path, _, _ in packages
              for p in (path, path.with_name(path.name + '.sha256'))]
    remote = {asset['name']: asset for asset in release.get('assets', [])}
    if (len(remote) != len(release.get('assets', []))
            or set(remote) != {p.name for p in assets}
            or not all(node.asset_matches(remote[p.name], p) for p in assets)):
        raise ValueError('downloaded Node release assets do not match published digests')
    for path, platform, arch in packages:
        if node.verify_package(path, arch, source, platform)['build_version'] != version:
            raise ValueError('Node package version differs from full release')


def latest_release_tag(repository: str) -> str | None:
    """Read the current stable latest release; API errors remain UNKNOWN/fatal."""
    try:
        response = json.loads(node.gh(
            'api', 'graphql', '-f',
            'query=query($owner:String!, $name:String!){repository(owner:$owner,name:$name){latestRelease{tagName}}}',
            '-F', 'owner=' + repository.split('/', 1)[0],
            '-F', 'name=' + repository.split('/', 1)[1],
        ))
    except Exception as exc:
        raise ValueError('latest release readback is UNKNOWN; do not publish or retry blindly') from exc
    try:
        repo = response['data']['repository']
        release = repo['latestRelease']
    except (KeyError, TypeError) as exc:
        raise ValueError('latest release readback is UNKNOWN; do not publish or retry blindly') from exc
    if release is None:
        return None
    tag = release.get('tagName')
    if not isinstance(tag, str) or not tag:
        raise ValueError('latest release readback is UNKNOWN; do not publish or retry blindly')
    return tag


def publish(directory: Path, source: str, private_source: str, version: int,
            ci_run: int, node_dir: Path) -> None:
    if version < 1074:
        raise ValueError('full release build must be at least 1074')
    repository = REPOSITORY
    assets = checked_assets(directory, source, version)
    verify_ci_and_node(repository, source, private_source, version, ci_run, node_dir)
    latest_before = latest_release_tag(repository)
    tag = f'v{version}'
    if node.tag_commit(repository, tag, allow_missing=True) is not None:
        raise ValueError('full release tag already exists; reconcile it without replay')
    existing = node.gh('api', f'repos/{repository}/releases', '--paginate', '--jq', '.[].tag_name').decode().splitlines()
    if tag in existing:
        raise ValueError('full release or draft already exists; reconcile it without replay')
    # Exact refs are created once. A failed/unknown mutation is never replayed.
    node.gh('api', f'repos/{repository}/git/refs', '--method', 'POST',
            '-f', 'ref=refs/tags/' + tag, '-f', 'sha=' + source)
    node.verify_tag(repository, tag, source)
    release = json.loads(node.gh('api', f'repos/{repository}/releases', '--method', 'POST',
        '-f', 'tag_name=' + tag, '-f', 'target_commitish=' + source, '-F', 'draft=true',
        '-f', 'make_latest=false', '-f', 'name=GPTAdmin ' + tag,
        '-f', 'body=Public source: ' + source + '\nSource-Commit: ' + private_source
        + '\nPublic CI run: ' + str(ci_run)
        + '\nmacOS binaries are not Apple Developer ID signed or notarized.'))
    if type(release.get('id')) is not int or release.get('tag_name') != tag or not release.get('draft'):
        raise ValueError('unexpected draft release response')
    uploaded = {}
    for path in assets:
        asset = json.loads(node.gh('api', f'https://uploads.github.com/repos/{repository}/releases/{release["id"]}/assets?name={path.name}',
            '--method', 'POST', '-H', 'Content-Type: application/octet-stream', '--input', str(path)))
        if not node.asset_matches(asset, path):
            raise ValueError('GitHub uploaded asset digest mismatch')
        uploaded[path.name] = asset
    # Recheck CI, Node and public main immediately before publishing the draft.
    verify_ci_and_node(repository, source, private_source, version, ci_run, node_dir)
    node.verify_tag(repository, tag, source)
    final = json.loads(node.gh('api', f'repos/{repository}/releases/{release["id"]}',
                               '--method', 'PATCH', '-F', 'draft=false', '-f', 'make_latest=false'))
    remote = {asset['name']: asset for asset in final.get('assets', [])}
    if (final.get('id') != release['id'] or final.get('tag_name') != tag or final.get('draft') is not False
            or set(remote) != set(uploaded) or not all(node.asset_matches(remote[p.name], p) for p in assets)):
        raise ValueError('published release identity or asset closure was not confirmed; do not retry blindly')
    latest_after = latest_release_tag(repository)
    if latest_after != latest_before:
        raise ValueError('GitHub latest changed despite make_latest=false; publication is complete but latest requires reconciliation')
    print(json.dumps({'published': True, 'tag': tag, 'public_source': source,
                      'private_source': private_source, 'latest_requested': False,
                      'latest_before': latest_before, 'latest_after': latest_after,
                      'assets': [p.name for p in assets]}))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', required=True, type=Path)
    parser.add_argument('--node-directory', required=True, type=Path)
    parser.add_argument('--source', required=True)
    parser.add_argument('--private-source', required=True)
    parser.add_argument('--version', required=True, type=int)
    parser.add_argument('--ci-run', required=True, type=int)
    args = parser.parse_args()
    publish(args.directory, args.source, args.private_source, args.version,
            args.ci_run, args.node_directory)


if __name__ == '__main__':
    main()
