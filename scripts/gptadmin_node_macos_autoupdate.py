#!/usr/bin/env python3
"""Opt-in, per-user updater for the immutable public GPTAdmin Node channel."""
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys
import tempfile
import urllib.parse
import urllib.request

REPO = 'megamen32/gptadmin_opensource'
ROOT = Path(os.environ.get('GPTADMIN_NODE_MACOS_ROOT', Path.home() / 'Library/Application Support/GPTAdmin/Node'))
API = f'https://api.github.com/repos/{REPO}/releases?per_page=100'
ALLOWED_HOSTS = {'api.github.com', 'github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com'}

def arch():
    value = platform.machine().lower()
    if value in ('arm64', 'aarch64'): return 'arm64'
    if value in ('x86_64', 'amd64'): return 'amd64'
    raise ValueError('unsupported macOS architecture')

def public_url(url):
    parsed = urllib.parse.urlsplit(url)
    if (parsed.scheme != 'https' or parsed.username or parsed.password or parsed.fragment
            or parsed.port not in (None, 443) or parsed.hostname not in ALLOWED_HOSTS):
        raise ValueError('unapproved release URL')

class PublicRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, url):
        public_url(url)
        return super().redirect_request(request, response, code, message, headers, url)

def fetch(url, limit):
    public_url(url)
    request = urllib.request.Request(url, headers={'User-Agent':'gptadmin-node-macos-updater/1'})
    opener = urllib.request.build_opener(PublicRedirect())
    with opener.open(request, timeout=20) as response:
        data = response.read(limit + 1)
    if len(data) > limit: raise ValueError('release response exceeds limit')
    return data

def update():
    state = json.loads((ROOT / 'state.json').read_text())
    config = json.loads((ROOT / 'autoupdate.json').read_text())
    if config.get('enabled') is not True: raise ValueError('automatic updates are disabled')
    releases = json.loads(fetch(API, 2 * 1024 * 1024))
    candidates=[]
    for release in releases:
        match=re.fullmatch(r'node-v([1-9][0-9]*)-([0-9a-f]{12})', release.get('tag_name',''))
        if match and not release.get('draft') and not release.get('prerelease'):
            candidates.append((int(match[1]), release, match[2]))
    if not candidates: raise ValueError('no public Node release found')
    version, release, prefix=max(candidates,key=lambda row:row[0])
    installed_version = int(state['build_version'])
    if version < installed_version: raise ValueError('public release is older than installed Node')
    installed_commit = state.get('git_commit', '')
    if version == installed_version and not (isinstance(installed_commit, str) and installed_commit.startswith(prefix)):
        raise ValueError('same Node build version has a different source')
    if version == installed_version:
        print(json.dumps({'updated':False,'reason':'already_current','build_version':version})); return
    source=prefix
    assets=[a for a in release.get('assets',[]) if a.get('name')==f'gptadmin-node-darwin-{arch()}.tar.gz']
    if len(assets)!=1: raise ValueError('Darwin asset missing or ambiguous')
    asset=assets[0]; url=asset.get('browser_download_url','')
    expected=f"https://github.com/{REPO}/releases/download/{release['tag_name']}/{asset['name']}"
    if url!=expected or not re.fullmatch(r'sha256:[0-9a-f]{64}',asset.get('digest','')): raise ValueError('release asset provenance invalid')
    with tempfile.TemporaryDirectory(prefix='node-update-',dir=ROOT) as tmp:
        package=Path(tmp)/asset['name']; package.write_bytes(fetch(url,128*1024*1024))
        if hashlib.sha256(package.read_bytes()).hexdigest()!=asset['digest'][7:]: raise ValueError('release asset digest mismatch')
        from gptadmin_node_macos_manage import validate_package
        stage=Path(tmp)/'verify'; manifest=validate_package(package,stage)
        if manifest['build_version']!=version or not manifest['git_commit'].startswith(prefix): raise ValueError('release tag does not bind package manifest')
        if version==int(state['build_version']):
            print(json.dumps({'updated':False,'reason':'already_current','build_version':version})); return
        manager=Path(__file__).with_name('gptadmin_node_macos_manage.py')
        subprocess.run([sys.executable,str(manager),'install','--package',str(package)],check=True,timeout=300)
    print(json.dumps({'updated':True,'build_version':version,'identity_preserved':True}))

if __name__=='__main__':
    update()
