#!/usr/bin/env python3
"""Update explicitly configured managed Node instances from the verified public channel."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request

REPOSITORY = 'megamen32/gptadmin_opensource'
API = 'https://api.github.com/repos/' + REPOSITORY + '/releases?per_page=100'
MANAGER = Path('/usr/local/lib/gptadmin/gptadmin_node_manage.py')
CONFIG = Path('/etc/gptadmin-node/autoupdate.json')
STATE = Path('/var/lib/gptadmin-node/autoupdate')


def record(path, value):
    """Durable intent/disposition before mutation; no power-loss rearming."""
    temp=path.with_name(path.name+'.new-'+str(os.getpid()))
    fd=os.open(temp,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
    try:
        with os.fdopen(fd,'w') as stream:
            json.dump(value,stream);stream.write('\n');stream.flush();os.fsync(stream.fileno())
        os.replace(temp,path)
        directory=os.open(path.parent,os.O_RDONLY|os.O_DIRECTORY)
        try:os.fsync(directory)
        finally:os.close(directory)
    finally:
        temp.unlink(missing_ok=True)


def clear_hold():
    (STATE/'hold.json').unlink()
    directory=os.open(STATE,os.O_RDONLY|os.O_DIRECTORY)
    try:os.fsync(directory)
    finally:os.close(directory)


def public_url(url):
    parsed = urllib.parse.urlsplit(url)
    if (parsed.scheme != 'https' or parsed.username or parsed.password or parsed.fragment
            or parsed.port not in (None, 443) or parsed.hostname not in
            ('api.github.com', 'github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com')):
        raise ValueError('unapproved public release URL')


class PublicRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, url):
        public_url(url)
        return super().redirect_request(request, response, code, message, headers, url)


def get(url, maximum, deadline, destination=None):
    public_url(url)
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise RuntimeError('Node automatic update deadline')
    opener = urllib.request.build_opener(PublicRedirect())
    request = urllib.request.Request(url, headers={'User-Agent': 'gptadmin-node-updater/1',
                                                  'Accept': 'application/vnd.github+json' if urllib.parse.urlsplit(url).hostname == 'api.github.com' else 'application/octet-stream'})
    with opener.open(request, timeout=min(20, remaining)) as response:
        if response.status != 200:
            raise RuntimeError('public Node artifact is unavailable')
        h = hashlib.sha256(); size = 0; parts = []
        output = destination.open('xb') if destination is not None else None
        try:
            while True:
                if time.monotonic() >= deadline:
                    raise RuntimeError('Node automatic update deadline')
                block = response.read(65536)
                if not block:
                    break
                size += len(block)
                if size > maximum:
                    raise ValueError('public Node response exceeds byte budget')
                h.update(block)
                if output is None: parts.append(block)
                else: output.write(block)
        finally:
            if output is not None: output.close()
    return (b''.join(parts) if destination is None else h.hexdigest())


def select_release(releases, arch):
    if arch not in ('amd64', 'arm64') or not isinstance(releases, list) or len(releases) > 100:
        raise ValueError('invalid public Node release index')
    candidates = []
    for entry in releases:
        if entry.get('draft') or entry.get('prerelease'):
            continue
        match = re.fullmatch(r'node-v([1-9][0-9]*)-([0-9a-f]{12})', entry.get('tag_name', ''))
        if match:
            candidates.append((int(match[1]), entry.get('published_at', ''), entry, match[2]))
    if not candidates:
        raise RuntimeError('no accepted public Node release exists')
    _, _, release, short = max(candidates, key=lambda row: row[:2])
    name = 'gptadmin-node-linux-' + arch + '.tar.gz'
    matching = [a for a in release.get('assets', []) if a.get('name') == name]
    if len(matching) != 1:
        raise ValueError('accepted Node release lacks exact architecture asset')
    asset = matching[0]
    url = asset.get('browser_download_url', '')
    expected = 'https://github.com/' + REPOSITORY + '/releases/download/' + release['tag_name'] + '/' + name
    if (url != expected or type(asset.get('size')) is not int or not 0 < asset['size'] <= 128 * 1024**2
            or not re.fullmatch(r'sha256:[0-9a-f]{64}', asset.get('digest', ''))):
        raise ValueError('Node asset URL/digest/size is not authoritative')
    return release, asset, short


def validate_config(config):
    if not isinstance(config, dict) or config.get('enabled') is not True:
        raise ValueError('Node automatic update is not explicitly enabled')
    names = config.get('names')
    if (not isinstance(names, list) or not 1 <= len(names) <= 8 or len(set(names)) != len(names)
            or any(not isinstance(n, str) or not re.fullmatch(r'[a-zA-Z0-9_-]{1,128}', n) for n in names)):
        raise ValueError('explicit bounded managed Node names required')
    return names


def resolve_tag(tag, deadline):
    obj = json.loads(get('https://api.github.com/repos/'+REPOSITORY+'/git/ref/tags/'+tag,65536,deadline))['object']
    for _ in range(4):
        sha = obj.get('sha')
        if not isinstance(sha,str) or not re.fullmatch('[0-9a-f]{40}',sha):
            raise ValueError('invalid accepted Node tag')
        if obj.get('type') == 'commit':return sha
        if obj.get('type') != 'tag':raise ValueError('Node tag does not refer to a commit')
        obj=json.loads(get('https://api.github.com/repos/'+REPOSITORY+'/git/tags/'+sha,65536,deadline))['object']
    raise ValueError('unbounded Node tag chain')


def live_matches(manager, name, source):
    env=manager.read_installed_env(manager.DEFAULT_LAYOUT,name)
    origin=manager.origin_from_env(env)
    status,_=manager.http_json('GET',origin+'/healthz')
    if not 200 <= status < 300:
        return False
    status,version=manager.http_json('GET',origin+'/version')
    return status==200 and isinstance(version,dict) and version.get('git_commit')==source


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='verify the public package without restarting a service')
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise ValueError('root is required for the existing native Node manager')
    os.umask(0o077)
    if (CONFIG.is_symlink() or CONFIG.stat().st_uid != 0 or CONFIG.stat().st_mode & 0o022
            or CONFIG.stat().st_size > 65536):
        raise ValueError('Node automatic update config is not root-owned/protected')
    names = validate_config(json.loads(CONFIG.read_text()))
    if MANAGER.is_symlink() or MANAGER.stat().st_uid != 0 or MANAGER.stat().st_mode & 0o022:
        raise ValueError('existing Node manager is not protected')
    STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
    if STATE.is_symlink() or STATE.stat().st_uid != 0 or STATE.stat().st_mode & 0o077:
        raise ValueError('Node updater state is not protected')
    fd = os.open(STATE/'update.lock', os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW, 0o600)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX|fcntl.LOCK_NB)
        if (STATE/'hold.json').exists():
            raise RuntimeError('previous manager failure requires verified recovery before automatic retry')
        # Shared code/current is changed only when all active instances have
        # been explicitly enrolled. No hidden update of another owner's node.
        units = subprocess.check_output(['systemctl','list-units','gptadmin-node@*.service',
                                          '--state=active','--plain','--no-legend'], timeout=5).decode()
        active = {line.split()[0][len('gptadmin-node@'):-len('.service')]
                  for line in units.splitlines() if line.strip()}
        if active != set(names):
            raise ValueError('active managed Node ownership differs from explicit update enrollment')
        if shutil.disk_usage(STATE).free < 512*1024**2:
            raise ValueError('Node updater disk reserve unavailable')
        deadline = time.monotonic() + 165
        import importlib.util
        spec = importlib.util.spec_from_file_location('node_manager',MANAGER)
        manager = importlib.util.module_from_spec(spec);spec.loader.exec_module(manager)
        arch = manager.host_arch()
        release, asset, short = select_release(json.loads(get(API,2*1024**2,deadline)),arch)
        source = resolve_tag(release['tag_name'],deadline)
        if not source.startswith(short):raise ValueError('accepted Node tag differs from manifest source prefix')
        current = manager.current_target(manager.DEFAULT_LAYOUT)
        previous = manager.manifest_summary(current) if current else None
        if (previous and previous.get('git_commit') == source
                and previous.get('build_version') == int(release['tag_name'].split('-')[1][1:])
                and all(live_matches(manager,n,source) for n in names)):
            receipt={'updated':False,'complete':True,'reason':'already_current',
                     'tag':release['tag_name'],'source':source,'instances':names}
            record(STATE/'last-update.json',receipt)
            print(json.dumps(receipt));return
        with tempfile.TemporaryDirectory(prefix='package-',dir=STATE) as temp:
            root = Path(temp);package = root / asset['name']
            sha = get(asset['browser_download_url'],128*1024**2,deadline,package)
            if sha != asset['digest'].removeprefix('sha256:') or package.stat().st_size != asset['size']:
                raise ValueError('public Node artifact digest/size differs')
            manifest = manager.verify_and_extract(package,arch,root/'verify')
            if (manifest.get('source_dirty') is not False or not re.fullmatch('[0-9a-f]{40}',manifest.get('git_commit',''))
                    or manifest['git_commit'] != source
                    or manifest.get('build_version') != int(release['tag_name'].split('-')[1][1:])):
                raise ValueError('public Node manifest does not bind accepted tag')
            manager.verify_elf_binary(root/'verify/bin/gptadmin-node')
            if args.check:
                print(json.dumps({'verified':True,'updated':False,'tag':release['tag_name'],'source':manifest['git_commit']}));return
            # Reuse the supported drain/update/verify/rollback lifecycle. No
            # role, profile, token, service limit or consumer config is changed.
            results=[]
            for name in names:
                if live_matches(manager,name,source):
                    results.append(name);continue
                remaining=deadline-time.monotonic()
                if remaining < 145:
                    receipt={'updated':bool(results),'complete':False,'source':source,
                             'verified_instances':results,'reason':'rollback_reserve_unavailable'}
                    record(STATE/'partial-update.json',receipt)
                    print(json.dumps(receipt));return
                # The manager raises a catchable deadline and retains 80s to
                # roll back. Parent timeout includes an additional 15s margin;
                # never SIGKILL it at the ordinary operation deadline.
                record(STATE/'hold.json',{'complete':False,'source':source,
                    'verified_instances':results,'reason':'manager_in_progress','instance':name})
                try:
                    result=subprocess.run(['python3',str(MANAGER),'update','--name',name,'--package',str(package),'--wait-secs','20','--require-drain','--operation-deadline-secs','50'],
                                          stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=145)
                except subprocess.TimeoutExpired:
                    receipt={'complete':False,'source':source,'verified_instances':results,
                             'reason':'manager_cleanup_unverified','instance':name}
                    record(STATE/'partial-update.json',receipt)
                    record(STATE/'hold.json',receipt)
                    raise RuntimeError('manager cleanup unverified; no further automatic update') from None
                (STATE/(name+'-last-private.log')).write_bytes(result.stdout+result.stderr)
                if result.returncode:
                    record(STATE/'hold.json',{'complete':False,'source':source,
                        'verified_instances':results,'reason':'manager_update_failed_review_required','instance':name})
                    raise RuntimeError('native Node update failed; manager rollback/disposition retained')
                if not live_matches(manager,name,source):
                    raise RuntimeError('manager returned success but live source not verified; hold retained')
                results.append(name)
                # Preserve successful slices even if a later peer refuses.
                record(STATE/'partial-update.json',{'source':source,'verified_instances':results,'complete':False})
                clear_hold()
            if not all(live_matches(manager,n,source) for n in names):
                raise RuntimeError('one managed instance did not load accepted Node source')
            receipt={'updated':True,'tag':release['tag_name'],'source':manifest['git_commit'],'instances':results,'sha256':sha}
            record(STATE/'last-update.json',receipt)
            print(json.dumps(receipt))
    finally:
        os.close(fd)


if __name__ == '__main__':
    main()
