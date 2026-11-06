#!/usr/bin/env python3
"""Opt-in, per-user updater for the immutable public GPTAdmin Node channel."""
import hashlib
import http.client
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys
import tempfile
import urllib.parse
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET

REPO = 'megamen32/gptadmin_opensource'
ROOT = Path(os.environ.get('GPTADMIN_NODE_MACOS_ROOT', Path.home() / 'Library/Application Support/GPTAdmin/Node'))
API = f'https://api.github.com/repos/{REPO}/releases?per_page=100'
ATOM = f'https://github.com/{REPO}/releases.atom'
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

def release_index():
    # Discard a truncated response and retry this read once. Installation is
    # not retried: its outcome must be verified separately by the manager.
    try:
        return fetch(API, 2 * 1024 * 1024)
    except http.client.IncompleteRead:
        return fetch(API, 2 * 1024 * 1024)

def select_public_release():
    try:
        releases = json.loads(release_index())
    except urllib.error.HTTPError as error:
        body = error.read(4096).lower()
        if error.code != 403 or b'rate limit exceeded' not in body:
            raise
        feed = fetch(ATOM, 2 * 1024 * 1024)
        if b'<!DOCTYPE' in feed.upper() or b'<!ENTITY' in feed.upper():
            raise ValueError('invalid public Node release feed')
        try:
            root = ET.fromstring(feed)
        except ET.ParseError:
            raise ValueError('invalid public Node release feed') from None
        ns = '{http://www.w3.org/2005/Atom}'
        if root.tag != ns+'feed': raise ValueError('invalid public Node release feed')
        entries = root.findall(ns+'entry')
        if len(entries) > 100: raise ValueError('public Node release feed exceeds limit')
        candidates = []
        for entry in entries:
            identity = entry.findtext(ns+'id', '')
            match = re.fullmatch(r'tag:github\.com,[0-9]+:Repository/[0-9]+/(node-v([1-9][0-9]*)-([0-9a-f]{12}))', identity)
            if match and entry.findtext(ns+'title', '') == 'GPTAdmin Node '+match[2]:
                candidates.append((int(match[2]), {'tag_name':match[1]}, match[3]))
        fallback = True
    else:
        if not isinstance(releases, list) or len(releases)>100: raise ValueError('invalid public Node release index')
        candidates=[]
        for release in releases:
            if not isinstance(release, dict): raise ValueError('invalid public Node release index')
            match=re.fullmatch(r'node-v([1-9][0-9]*)-([0-9a-f]{12})', release.get('tag_name',''))
            if match and not release.get('draft') and not release.get('prerelease'):
                candidates.append((int(match[1]), release, match[2]))
        fallback = False
    if not candidates: raise ValueError('no public Node release found')
    version=max(row[0] for row in candidates)
    latest=[row for row in candidates if row[0]==version]
    if len(latest)!=1: raise ValueError('ambiguous public Node release version')
    _, release, prefix=latest[0]
    return version, release, prefix, fallback

def release_checksum(url, name):
    raw = fetch(url+'.sha256', 256)
    try: value = raw.decode('ascii').strip()
    except UnicodeDecodeError: raise ValueError('invalid public Node checksum') from None
    match = re.fullmatch(r'([0-9a-f]{64})  '+re.escape(name), value)
    if not match: raise ValueError('invalid public Node checksum')
    return match[1]

def release_source(prefix):
    if not re.fullmatch(r'[0-9a-f]{12}', prefix): raise ValueError('invalid public Node source prefix')
    patch = fetch(f'https://github.com/{REPO}/commit/{prefix}.patch', 256 * 1024)
    header, separator, _ = patch.partition(b'\n---\n')
    if not separator: raise ValueError('invalid public Node source patch')
    try: lines = [line.strip() for line in header.decode('utf-8', 'strict').splitlines() if line.strip()]
    except UnicodeDecodeError: raise ValueError('invalid public Node source patch') from None
    first = re.fullmatch(r'From ([0-9a-f]{40}) .+', lines[0]) if lines else None
    trailers = [line for line in lines if line.startswith('Source-Commit:')]
    if (not first or not first[1].startswith(prefix) or len(trailers)!=1 or lines[-1]!=trailers[0]
            or not re.fullmatch(r'Source-Commit: [0-9a-f]{40}', trailers[0])):
        raise ValueError('public Node source does not match release tag')
    return first[1]

def update():
    state = json.loads((ROOT / 'state.json').read_text())
    config = json.loads((ROOT / 'autoupdate.json').read_text())
    if config.get('enabled') is not True: raise ValueError('automatic updates are disabled')
    version, release, prefix, fallback = select_public_release()
    installed_version = int(state['build_version'])
    if version < installed_version: raise ValueError('public release is older than installed Node')
    installed_commit = state.get('git_commit', '')
    if version == installed_version and not (isinstance(installed_commit, str) and installed_commit.startswith(prefix)):
        raise ValueError('same Node build version has a different source')
    if version == installed_version:
        print(json.dumps({'updated':False,'reason':'already_current','build_version':version})); return
    source = release_source(prefix) if fallback else ''
    identity_path = ROOT / 'config/node-identity.json'
    identity_before = hashlib.sha256(identity_path.read_bytes()).digest() if identity_path.is_file() else None
    name=f'gptadmin-node-darwin-{arch()}.tar.gz'
    expected=f"https://github.com/{REPO}/releases/download/{release['tag_name']}/{name}"
    if fallback:
        asset={'name':name, 'browser_download_url':expected, 'digest':'sha256:'+release_checksum(expected,name)}
    else:
        assets=[a for a in release.get('assets',[]) if a.get('name')==name]
        if len(assets)!=1: raise ValueError('Darwin asset missing or ambiguous')
        asset=assets[0]
    url=asset.get('browser_download_url','')
    if url!=expected or not re.fullmatch(r'sha256:[0-9a-f]{64}',asset.get('digest','')): raise ValueError('release asset provenance invalid')
    with tempfile.TemporaryDirectory(prefix='node-update-',dir=ROOT) as tmp:
        package=Path(tmp)/asset['name']; package.write_bytes(fetch(url,128*1024*1024))
        if hashlib.sha256(package.read_bytes()).hexdigest()!=asset['digest'][7:]: raise ValueError('release asset digest mismatch')
        from gptadmin_node_macos_manage import validate_package
        stage=Path(tmp)/'verify'; manifest=validate_package(package,stage)
        if (manifest['build_version']!=version or not manifest['git_commit'].startswith(prefix)
                or (source and manifest['git_commit']!=source)):
            raise ValueError('release tag does not bind package manifest')
        if version==int(state['build_version']):
            print(json.dumps({'updated':False,'reason':'already_current','build_version':version})); return
        manager=Path(__file__).with_name('gptadmin_node_macos_manage.py')
        subprocess.run([sys.executable,str(manager),'install','--package',str(package)],check=True,timeout=300)
    identity_preserved = (hashlib.sha256(identity_path.read_bytes()).digest() == identity_before
                          if identity_before is not None and identity_path.is_file()
                          else False if identity_before is not None else None)
    print(json.dumps({'updated':True,'build_version':version,'identity_preserved':identity_preserved}))

if __name__=='__main__':
    update()
