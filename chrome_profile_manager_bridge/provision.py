#!/usr/bin/env python3
"""Provision a per-profile unpacked bridge without touching Chrome profiles."""
import argparse
import base64
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import tempfile

HOST = Path(__file__).resolve().parent / 'host.py'
SOURCE = Path(__file__).resolve().parent / 'extension'
NAME = 'com.gptadmin.extension_bridge'
ORIGIN = re.compile(r'chrome-extension://[a-p]{32}/\Z')


def private_dir(path):
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    st = path.lstat()
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o077:
        raise ValueError('unsafe private directory: ' + str(path))


def read_json(path, private=False):
    if not os.path.lexists(path):
        return {}
    st = path.lstat()
    if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or (private and st.st_mode & 0o077):
        raise ValueError('unsafe file: ' + str(path))
    with path.open(encoding='utf-8') as stream:
        result = json.load(stream)
    if not isinstance(result, dict):
        raise ValueError('expected JSON object: ' + str(path))
    return result


def atomic(path, data, mode=0o600):
    fd, name = tempfile.mkstemp(prefix='.bridge-', dir=path.parent)
    try:
        os.fchmod(fd, mode)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try: os.fsync(directory)
        finally: os.close(directory)
    finally:
        if os.path.exists(name): os.unlink(name)


def save(path, value):
    atomic(path, (json.dumps(value, indent=2, sort_keys=True) + '\n').encode())


def extension_origin(key):
    digest = hashlib.sha256(base64.b64decode(key, validate=True)).digest()[:16]
    return 'chrome-extension://' + ''.join(chr(97 + n) for b in digest for n in (b >> 4, b & 15)) + '/'


def public_key():
    # The private key exists only in the subprocess pipe and is never persisted.
    private = subprocess.run(['openssl', 'genpkey', '-algorithm', 'RSA', '-pkeyopt', 'rsa_keygen_bits:2048'],
                             stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=True).stdout
    public = subprocess.run(['openssl', 'pkey', '-pubout', '-outform', 'DER'], input=private,
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=True).stdout
    return base64.b64encode(public).decode('ascii')


def provision(profile_id, user_data_dir):
    if not isinstance(profile_id, str) or not profile_id or len(profile_id) > 128 or any(ord(c) < 32 or ord(c) == 127 for c in profile_id):
        raise ValueError('invalid profile id')
    data = Path(user_data_dir).expanduser()
    if not data.is_absolute() or not data.is_dir() or data.is_symlink():
        raise ValueError('user-data-dir must be an existing absolute directory')
    data = data.resolve(strict=True)
    config_home = Path(os.environ.get('XDG_CONFIG_HOME') or Path.home() / '.config')
    if not config_home.is_absolute(): raise ValueError('XDG_CONFIG_HOME must be absolute')
    root = config_home / 'gptadmin-extension-bridge'
    private_dir(root)
    lock_path = root / 'provision.lock'
    fd = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'r+') as lock:
        st = os.fstat(lock.fileno())
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o077:
            raise ValueError('unsafe lock file')
        fcntl.flock(lock, fcntl.LOCK_EX)
        registry_path, bindings_path = root / 'profiles.json', root / 'bindings.json'
        registry, bindings = read_json(registry_path, True), read_json(bindings_path, True)
        roots_path = root / 'roots.json'
        roots = read_json(roots_path, True)
        if any(not isinstance(k, str) or not isinstance(v, str) or not os.path.isabs(v) for k, v in roots.items()):
            raise ValueError('invalid root registry')
        if profile_id in roots and roots[profile_id] != str(data):
            raise ValueError('profile root conflict')
        if profile_id in bindings and profile_id not in roots:
            raise ValueError('bound profile root unknown')
        if any(not isinstance(k, str) or not ORIGIN.fullmatch(k) or not isinstance(v, str) or not re.fullmatch('[a-f0-9]{64}', v) for k, v in registry.items()):
            raise ValueError('invalid registry')
        if any(not isinstance(k, str) or not isinstance(v, str) or not ORIGIN.fullmatch(v) for k, v in bindings.items()) or len(set(bindings.values())) != len(bindings):
            raise ValueError('invalid bindings')
        artifacts = root / 'extensions'
        private_dir(artifacts)
        artifact = artifacts / hashlib.sha256(profile_id.encode()).hexdigest()
        private_dir(artifact)
        manifest_path = artifact / 'manifest.json'
        if os.path.lexists(manifest_path):
            manifest = read_json(manifest_path, True)
            origin = extension_origin(manifest['key'])
        else:
            if profile_id in bindings: raise ValueError('bound extension artifact missing')
            manifest = json.loads((SOURCE / 'manifest.json').read_text())
            manifest['key'] = public_key()
            origin = extension_origin(manifest['key'])
            save(manifest_path, manifest)
        if profile_id in bindings and bindings[profile_id] != origin: raise ValueError('profile binding conflict')
        if origin in (v for k, v in bindings.items() if k != profile_id): raise ValueError('origin already bound')
        for name in ('background.js', 'popup.js', 'popup.html'):
            atomic(artifact / name, (SOURCE / name).read_bytes())
        atomic(artifact / 'config.js', b'const AUTO_CONNECT = true;\n')
        if origin not in registry:
            if profile_id in bindings: raise ValueError('bound capability missing')
            registry[origin] = secrets.token_hex(32)
        if len(set(registry.values())) != len(registry): raise ValueError('duplicate capability')
        bindings[profile_id] = origin
        native_dir = data / 'NativeMessagingHosts'
        native_dir.mkdir(mode=0o700, exist_ok=True)
        st = native_dir.lstat()
        if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.getuid(): raise ValueError('unsafe native messaging directory')
        native_path = native_dir / (NAME + '.json')
        native = read_json(native_path)
        if native and (native.get('name') != NAME or native.get('type') != 'stdio' or native.get('path') != str(HOST)):
            raise ValueError('native host registration conflict')
        origins = native.get('allowed_origins', [])
        if not isinstance(origins, list) or any(not isinstance(x, str) or not ORIGIN.fullmatch(x) for x in origins):
            raise ValueError('invalid allowed origins')
        native.update(name=NAME, description=native.get('description', 'Profile extension bridge'), path=str(HOST), type='stdio', allowed_origins=list(dict.fromkeys(origins + [origin])))
        save(registry_path, registry)
        roots[profile_id] = str(data)
        save(roots_path, roots)
        save(bindings_path, bindings)
        save(native_path, native)
        return artifact


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--profile-id', required=True)
    parser.add_argument('--user-data-dir', required=True)
    args = parser.parse_args()
    try:
        print(provision(args.profile_id, args.user_data_dir))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, 'Provisioning failed: ' + str(exc) + '\n')


if __name__ == '__main__': main()
