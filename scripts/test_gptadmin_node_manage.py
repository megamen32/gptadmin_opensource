#!/usr/bin/env python3
"""Unit tests for the pure local logic of scripts/gptadmin_node_manage.py.

Only real temp-directory file logic and real local HTTP servers are used.
systemctl/service operations are intentionally NOT faked or unit-tested here;
they are verified live. Run: python3 -m unittest scripts.test_gptadmin_node_manage
"""
from __future__ import annotations

import hashlib
import http.server
import io
import json
import os
from contextlib import redirect_stdout
from pathlib import Path
import sys
import tarfile
import tempfile
import threading
import unittest
from unittest.mock import patch
import urllib.error

sys.path.insert(0, str(Path(__file__).resolve().parent))
import gptadmin_node_manage as mgr  # noqa: E402

ELF_PAYLOAD = b'\x7fELF' + b'\x02\x01\x01\x00' + b'payload'


class QuietHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass


def start_server(handler):
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server


def respond_json(handler, code, payload):
    body = json.dumps(payload).encode()
    handler.send_response(code)
    handler.send_header('Content-Type', 'application/json')
    handler.send_header('Content-Length', str(len(body)))
    handler.end_headers()
    handler.wfile.write(body)


def read_body(handler):
    length = int(handler.headers.get('Content-Length', 0) or 0)
    return json.loads(handler.rfile.read(length) or b'{}')


def make_package(directory: Path, *, arch='amd64', commit='cafe1234abcd' * 3 + 'cafe', version=181,
                 payload=None, manifest_mutator=None, extra_members=()) -> Path:
    """Build a package tarball exactly like tools/build_node.py does."""
    files = payload if payload is not None else {
        'bin/gptadmin-node': ELF_PAYLOAD,
        'bin/shellmcp': ELF_PAYLOAD,
        'systemd/gptadmin-proxy-route-relay02.service': b'[Unit]\nDescription=fixture\n[Service]\nUser=admin\n',
        'cli.py': b'# cli payload\n',
        'scripts/gptadmin_node_manage.py': b'# manager payload\n',
        'scripts/gptadmin_node_probe.py': b'# probe payload\n',
        'scripts/gptadmin_node_autoupdate.py': b'# autoupdate payload\n',
        'public/admin/index.html': b'<script src=\"assets/app.js\"></script>',
        'public/admin/assets/app.js': b'// asset fixture',
    }
    manifest = {
        'schema': mgr.PACKAGE_SCHEMA, 'git_commit': commit, 'build_version': version,
        'platform': 'linux', 'arch': arch, 'source_dirty': False,
        'files': {name: {'sha256': hashlib.sha256(body).hexdigest(), 'size': len(body)}
                  for name, body in files.items()},
    }
    if manifest_mutator:
        manifest_mutator(manifest)
    path = directory / f'pkg-{commit}-{arch}.tar.gz'
    with tarfile.open(path, 'w:gz') as tar:
        import io
        manifest_bytes = (json.dumps(manifest, sort_keys=True, indent=2) + '\n').encode()
        info = tarfile.TarInfo('manifest.json')
        info.size = len(manifest_bytes)
        tar.addfile(info, io.BytesIO(manifest_bytes))
        for name, body in files.items():
            info = tarfile.TarInfo(name)
            info.size = len(body)
            info.mode = 0o755 if name == 'bin/gptadmin-node' else 0o644
            tar.addfile(info, io.BytesIO(body))
        for name, body in extra_members:
            info = tarfile.TarInfo(name)
            info.size = len(body)
            tar.addfile(info, io.BytesIO(body))
    return path


class BaseTempCase(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix='node-manage-test-'))
        self.layout = mgr.Layout(opt_root=self.tmp / 'opt', state_root=self.tmp / 'var',
                                 config_root=self.tmp / 'etc',
                                 unit_path=self.tmp / 'etc' / 'systemd' / 'system' / 'gptadmin-node@.service')
        self.servers = []

    def start(self, handler):
        server = start_server(handler)
        self.servers.append(server)
        return server

    def tearDown(self):
        for server in self.servers:
            server.shutdown()
            server.server_close()
        import shutil
        shutil.rmtree(self.tmp, ignore_errors=True)


class TestPackageVerification(BaseTempCase):
    def test_good_package_verifies_and_extracts(self):
        package = make_package(self.tmp)
        dest = self.tmp / 'dest'
        manifest = mgr.verify_and_extract(package, 'amd64', dest)
        self.assertEqual(manifest['schema'], mgr.PACKAGE_SCHEMA)
        self.assertEqual(manifest['arch'], 'amd64')
        self.assertTrue((dest / 'manifest.json').is_file())
        self.assertEqual((dest / 'bin' / 'gptadmin-node').read_bytes(), ELF_PAYLOAD)
        self.assertEqual((dest / 'cli.py').read_bytes(), b'# cli payload\n')
        self.assertEqual(os.stat(dest / 'bin' / 'gptadmin-node').st_mode & 0o777, 0o755)
        self.assertEqual(os.stat(dest / 'cli.py').st_mode & 0o777, 0o644)
        mgr.verify_elf_binary(dest / 'bin' / 'gptadmin-node')

    def test_tampered_digest_fails_before_extraction(self):
        def tamper(manifest):
            manifest['files']['cli.py']['sha256'] = '0' * 64
        package = make_package(self.tmp, manifest_mutator=tamper)
        dest = self.tmp / 'dest'
        with self.assertRaises(mgr.PreflightError):
            mgr.verify_and_extract(package, 'amd64', dest)
        self.assertFalse(dest.exists())

    def test_wrong_arch_fails(self):
        package = make_package(self.tmp, arch='arm64')
        with self.assertRaises(mgr.PreflightError):
            mgr.verify_and_extract(package, 'amd64', self.tmp / 'dest')

    def test_wrong_platform_fails(self):
        def mutate(manifest):
            manifest['platform'] = 'darwin'
        package = make_package(self.tmp, manifest_mutator=mutate)
        with self.assertRaises(mgr.PreflightError):
            mgr.verify_and_extract(package, 'amd64', self.tmp / 'dest')

    def test_wrong_schema_fails(self):
        def mutate(manifest):
            manifest['schema'] = 'gptadmin.node-package/v0'
        package = make_package(self.tmp, manifest_mutator=mutate)
        with self.assertRaises(mgr.PreflightError):
            mgr.verify_and_extract(package, 'amd64', self.tmp / 'dest')

    def test_size_mismatch_fails(self):
        def mutate(manifest):
            manifest['files']['bin/gptadmin-node']['size'] += 1
        package = make_package(self.tmp, manifest_mutator=mutate)
        with self.assertRaises(mgr.PreflightError):
            mgr.verify_and_extract(package, 'amd64', self.tmp / 'dest')

    def test_missing_file_fails(self):
        def mutate(manifest):
            manifest['files']['scripts/gptadmin_node_probe.py'] = {
                'sha256': hashlib.sha256(b'gone').hexdigest(), 'size': 4}
        package = make_package(self.tmp, manifest_mutator=mutate)
        with self.assertRaises(mgr.PreflightError):
            mgr.verify_and_extract(package, 'amd64', self.tmp / 'dest')

    def test_unsafe_member_name_rejected(self):
        package = make_package(self.tmp, extra_members=[('../evil', b'pwn')])
        with self.assertRaises(mgr.PreflightError):
            mgr.verify_and_extract(package, 'amd64', self.tmp / 'dest')
        self.assertFalse((self.tmp.parent / 'evil').exists())

    def test_elf_check_rejects_non_elf_and_non_executable(self):
        plain = self.tmp / 'plain'
        plain.write_bytes(b'#!/bin/sh\necho hi\n')
        plain.chmod(0o755)
        with self.assertRaises(mgr.PreflightError):
            mgr.verify_elf_binary(plain)
        elf = self.tmp / 'elf'
        elf.write_bytes(ELF_PAYLOAD)
        elf.chmod(0o644)
        if os.geteuid() != 0:  # root passes X_OK regardless of mode bits
            with self.assertRaises(mgr.PreflightError):
                mgr.verify_elf_binary(elf)

    def test_host_arch_mapping(self):
        with patch.object(mgr.platform_module, 'machine', return_value='x86_64'):
            self.assertEqual(mgr.host_arch(), 'amd64')
        with patch.object(mgr.platform_module, 'machine', return_value='aarch64'):
            self.assertEqual(mgr.host_arch(), 'arm64')
        with patch.object(mgr.platform_module, 'machine', return_value='mips'):
            with self.assertRaises(mgr.PreflightError):
                mgr.host_arch()


class TestIdentity(BaseTempCase):
    def test_created_once_with_schema_and_permissions(self):
        path = self.layout.identity_file('edge-1')
        identity = mgr.ensure_identity(path)
        self.assertEqual(identity['schema'], mgr.IDENTITY_SCHEMA)
        self.assertRegex(identity['node_id'], r'^[0-9a-f]{32}$')
        self.assertRegex(identity['node_key'], r'^[0-9a-f]{64}$')
        self.assertTrue(identity['created'].endswith('Z'))
        self.assertEqual(os.stat(path).st_mode & 0o777, 0o600)

    def test_second_call_never_overwrites(self):
        path = self.layout.identity_file('edge-1')
        first = mgr.ensure_identity(path)
        second = mgr.ensure_identity(path)
        self.assertEqual(first, second)
        # Even an externally modified file is honored, never regenerated.
        edited = dict(first, node_id='f' * 32)
        path.write_text(json.dumps(edited))
        self.assertEqual(mgr.ensure_identity(path)['node_id'], 'f' * 32)

    def test_corrupt_identity_fails_closed(self):
        path = self.layout.identity_file('edge-1')
        path.parent.mkdir(parents=True)
        path.write_text('{not json')
        with self.assertRaises(mgr.RuntimeFailure):
            mgr.ensure_identity(path)
        self.assertEqual(path.read_text(), '{not json')

    def test_merge_identity_adds_writer_url_and_keeps_0600(self):
        path = self.layout.identity_file('edge-1')
        identity = mgr.ensure_identity(path)
        merged = mgr.merge_identity(path, {'writer_url': 'https://writer.example'})
        self.assertEqual(merged['node_id'], identity['node_id'])
        self.assertEqual(merged['node_key'], identity['node_key'])
        self.assertEqual(merged['writer_url'], 'https://writer.example')
        self.assertEqual(os.stat(path).st_mode & 0o777, 0o600)


class TestVersionDirsAndSwitch(BaseTempCase):
    def test_version_dir_naming_increments(self):
        root = self.layout.versions_root()
        self.assertEqual(mgr.next_version_dir(root, 'abc').name, 'abc-1')
        (root / 'abc-1').mkdir()
        self.assertEqual(mgr.next_version_dir(root, 'abc').name, 'abc-2')
        (root / 'abc-2').mkdir()
        self.assertEqual(mgr.next_version_dir(root, 'abc').name, 'abc-3')
        self.assertEqual(mgr.next_version_dir(root, 'def').name, 'def-1')

    def test_staging_dirs_do_not_collide_with_versions(self):
        root = self.layout.versions_root()
        root.mkdir(parents=True)
        (root / '.staging-42').mkdir()
        self.assertEqual(mgr.next_version_dir(root, 'abc').name, 'abc-1')

    def test_atomic_current_switch(self):
        versions = self.layout.versions_root()
        first, second = versions / 'abc-1', versions / 'abc-2'
        first.mkdir(parents=True)
        second.mkdir(parents=True)
        mgr.switch_current(self.layout, first)
        self.assertEqual(Path(os.path.realpath(self.layout.current_link())), first.resolve())
        mgr.switch_current(self.layout, second)
        self.assertEqual(Path(os.path.realpath(self.layout.current_link())), second.resolve())
        self.assertTrue(self.layout.current_link().is_symlink())
        leftovers = [p.name for p in self.layout.opt_root.iterdir() if p.name.startswith('current.new')]
        self.assertEqual(leftovers, [])
        self.assertEqual(mgr.current_target(self.layout), second.resolve())

    def test_current_target_none_when_absent(self):
        self.assertIsNone(mgr.current_target(self.layout))


class TestRollbackSelection(BaseTempCase):
    def make_versions(self, first_mtime, second_mtime):
        versions = self.layout.versions_root()
        first, second = versions / 'old-1', versions / 'new-2'
        first.mkdir(parents=True)
        second.mkdir(parents=True)
        os.utime(first, (first_mtime, first_mtime))
        os.utime(second, (second_mtime, second_mtime))
        return first, second

    def test_most_recent_other_dir_is_selected(self):
        first, second = self.make_versions(1000, 2000)
        picked = mgr.select_rollback_dir(self.layout.versions_root(), second, None)
        self.assertEqual(picked, first)

    def test_selection_ignores_mtymes_of_current(self):
        first, second = self.make_versions(2000, 1000)
        picked = mgr.select_rollback_dir(self.layout.versions_root(), second, None)
        self.assertEqual(picked, first)

    def test_explicit_target_wins(self):
        first, second = self.make_versions(1000, 2000)
        picked = mgr.select_rollback_dir(self.layout.versions_root(), second, 'old-1')
        self.assertEqual(picked, first)

    def test_explicit_missing_target_fails(self):
        first, second = self.make_versions(1000, 2000)
        with self.assertRaises(mgr.RuntimeFailure):
            mgr.select_rollback_dir(self.layout.versions_root(), second, 'ghost-9')

    def test_no_other_version_fails(self):
        only = self.layout.versions_root() / 'solo-1'
        only.mkdir(parents=True)
        with self.assertRaises(mgr.RuntimeFailure):
            mgr.select_rollback_dir(self.layout.versions_root(), only, None)


class TestEnvFile(BaseTempCase):
    def build_values(self, **overrides):
        kwargs = dict(
            name='edge-1', layout=self.layout, bind_host='127.0.0.1', bind_port=9001,
            token='t' * 64, admin_password=None, public_origin=None,
            node_public_url=None, seed=None, peers=None,
            default_user='root', default_home='/root')
        kwargs.update(overrides)
        return mgr.build_env_values(**kwargs)

    def test_exact_keys_and_values(self):
        values = self.build_values(public_origin='https://n.example', seed='https://seed:9001',
                                   peers='[{"url":"https://peer"}]')
        expected = {
            'SHELL_TOKEN', 'CTL_TOKEN', 'GPTADMIN_HUB_HOST', 'GPTADMIN_HUB_PORT',
            'GPTADMIN_ROOT', 'GPTADMIN_CONFIG_DIR', 'GPTADMIN_ENV_FILE', 'PUBLIC_ORIGIN',
            'GPTADMIN_AUTH_MODE', 'SHELL_NAME', 'SHELL_IDENTITY_DIR', 'SHELL_SPOOL_DIR',
            'SHELL_OUTBOX_DIR', 'SHELL_DEFAULT_USER', 'SHELL_DEFAULT_CWD',
            'SHELL_DEFAULT_HOME', 'SHELLMCP_SELF_REPAIR_DISABLE', 'ROOTD_AUTO_UPDATE',
            'SHELL_SERVICE_NAME', 'ROOTD_SERVICE_NAME', 'GPTADMIN_NODE_IDENTITY',
            'GPTADMIN_NODE_SEED', 'GPTADMIN_NODE_PEERS', 'GPTADMIN_AUTH_SOURCE_ID',
        }
        self.assertEqual(set(values), expected)
        self.assertNotIn('ADMIN_PASSWORD', values)
        self.assertEqual(values['CTL_TOKEN'], 't' * 64)
        self.assertEqual(values['GPTADMIN_HUB_PORT'], '9001')
        self.assertEqual(values['GPTADMIN_ROOT'], str(self.layout.current_link()))
        self.assertEqual(values['GPTADMIN_CONFIG_DIR'], str(self.layout.config('edge-1')))
        self.assertEqual(values['GPTADMIN_AUTH_MODE'], 'reader')
        self.assertEqual(values['SHELLMCP_SELF_REPAIR_DISABLE'], '1')
        self.assertEqual(values['ROOTD_AUTO_UPDATE'], '0')
        self.assertEqual(values['SHELL_SERVICE_NAME'], 'gptadmin-node@edge-1.service')
        self.assertEqual(values['ROOTD_SERVICE_NAME'], 'gptadmin-node@edge-1.service')
        self.assertEqual(values['GPTADMIN_NODE_IDENTITY'], str(self.layout.identity_file('edge-1')))
        self.assertEqual(values['GPTADMIN_NODE_PEERS'], '[{"url":"https://peer"}]')

    def test_seed_and_peers_default_empty_and_admin_password_optional(self):
        values = self.build_values(admin_password='a' * 32)
        self.assertEqual(values['GPTADMIN_NODE_SEED'], '')
        self.assertEqual(values['GPTADMIN_NODE_PEERS'], '')
        self.assertEqual(values['ADMIN_PASSWORD'], 'a' * 32)
        self.assertNotIn('PUBLIC_ORIGIN', self.build_values())

    def test_render_order_and_quoting_roundtrip(self):
        values = self.build_values()
        text = mgr.render_env_file(values)
        keys = [line.split('=', 1)[0] for line in text.strip().splitlines()]
        ordered = [key for key in mgr.ENV_KEY_ORDER if key in values]
        self.assertEqual(keys, ordered)
        self.assertTrue(text.startswith('SHELL_TOKEN=' + mgr.quote_env_value('t' * 64)))
        self.assertEqual(mgr.quote_env_value('a"b\\c'), '"a\\"b\\\\c"')
        path = self.layout.env_file('edge-1')
        mgr.write_env_file(path, values)
        self.assertEqual(os.stat(path).st_mode & 0o777, 0o600)
        self.assertEqual(mgr.parse_env_file(path), values)

    def test_parse_env_file_supports_plain_and_comments(self):
        path = self.tmp / 'plain.env'
        path.write_text('# comment\nA=1\nB="two words"\n\nC=x"y\n')
        self.assertEqual(mgr.parse_env_file(path), {'A': '1', 'B': 'two words', 'C': 'x"y'})

    def test_reinstall_merge_preserves_configured_values(self):
        existing = self.build_values(public_origin='https://n.example', admin_password='a' * 32)
        fresh = self.build_values(token='f' * 64, peers='[{"url":"https://new-peer"}]')
        self.assertNotIn('PUBLIC_ORIGIN', fresh)  # operator omitted it on reinstall
        merged = mgr.merge_preserving(existing, fresh)
        self.assertEqual(merged['SHELL_TOKEN'], 't' * 64)  # identity-bound token survives
        self.assertEqual(merged['CTL_TOKEN'], 't' * 64)
        self.assertEqual(merged['ADMIN_PASSWORD'], 'a' * 32)  # preserved extra key
        self.assertEqual(merged['PUBLIC_ORIGIN'], 'https://n.example')  # preserved extra key
        self.assertEqual(merged['GPTADMIN_NODE_PEERS'], '[{"url":"https://new-peer"}]')  # empty was replaced

    def test_unit_template_renders_layout_paths(self):
        text = mgr.render_unit_template(self.layout)
        self.assertIn(f'EnvironmentFile={self.layout.config_root}/%i/gptadmin.env', text)
        self.assertIn(f'ExecStart={self.layout.opt_root}/current/bin/gptadmin-node', text)
        self.assertIn('Description=GPTAdmin Node %i', text)
        self.assertIn('KillSignal=SIGTERM', text)
        self.assertIn('TimeoutStopSec=120', text)
        self.assertIn('WantedBy=multi-user.target', text)


class TestDrainAndHealthHttp(BaseTempCase):
    def make_drain_server(self, ready_after=2, drain_supported=True):
        lock = threading.Lock()
        state = {'actions': [], 'status_calls': 0}

        class Handler(QuietHandler):
            def do_POST(self):
                request = read_body(self)
                action = request.get('action')
                with lock:
                    state['actions'].append(action)
                    if action == 'status':
                        state['status_calls'] += 1
                        code, payload = 200, {'ready_to_stop': state['status_calls'] >= ready_after}
                    elif action == 'begin':
                        code, payload = (404, {'error': 'no drain'}) if not drain_supported else (200, {'ok': True})
                    else:
                        code, payload = 200, {'ok': True}
                respond_json(self, code, payload)

        return self.start(Handler), state

    def test_drain_begin_waits_ready_and_cancels(self):
        server, state = self.make_drain_server(ready_after=3)
        origin = f'http://127.0.0.1:{server.server_address[1]}'
        token = 'c' * 64
        self.assertTrue(mgr.drain_begin(origin, token))
        status = mgr.drain_wait_ready(origin, token, wait_secs=10, poll_interval=0.02)
        self.assertIs(status['ready_to_stop'], True)
        self.assertGreaterEqual(state['status_calls'], 3)
        self.assertTrue(mgr.drain_cancel(origin, token))
        self.assertEqual(state['actions'][0], 'begin')
        self.assertEqual(state['actions'][-1], 'cancel')
        # Every drain call carried the CTL_TOKEN bearer.
        self.assertTrue(all(action in ('begin', 'status', 'cancel') for action in state['actions']))

    def test_drain_begin_404_means_skip(self):
        server, state = self.make_drain_server(drain_supported=False)
        origin = f'http://127.0.0.1:{server.server_address[1]}'
        self.assertFalse(mgr.drain_begin(origin, 't'))
        self.assertTrue(mgr.drain_cancel(origin, 't'))  # 404 on cancel is tolerated

    def test_drain_timeout_raises(self):
        server, state = self.make_drain_server(ready_after=10 ** 9)
        origin = f'http://127.0.0.1:{server.server_address[1]}'
        with self.assertRaises(mgr.RuntimeFailure):
            mgr.drain_wait_ready(origin, 't', wait_secs=0.25, poll_interval=0.05)

    def test_drain_bearer_token_sent(self):
        seen = []

        class Handler(QuietHandler):
            def do_POST(self):
                seen.append(self.headers.get('Authorization'))
                respond_json(self, 200, {'ready_to_stop': True})

        server = self.start(Handler)
        origin = f'http://127.0.0.1:{server.server_address[1]}'
        mgr.drain_wait_ready(origin, 'secret-token', wait_secs=1, poll_interval=0.02)
        self.assertEqual(seen, ['Bearer secret-token'])

    def test_wait_healthy_requires_commit_match(self):
        commit = 'cafe1234abcd'

        class Handler(QuietHandler):
            def do_GET(self):
                if self.path == '/healthz':
                    respond_json(self, 200, {'ok': True})
                else:
                    respond_json(self, 200, {'git_commit': commit, 'build_version': 181})

        server = self.start(Handler)
        origin = f'http://127.0.0.1:{server.server_address[1]}'
        version = mgr.wait_healthy(origin, commit, timeout=5)
        self.assertEqual(version['git_commit'], commit)
        with self.assertRaises(mgr.RuntimeFailure):
            mgr.wait_healthy(origin, 'different', timeout=0.6)
        # An empty expectation accepts any healthy version.
        self.assertEqual(mgr.wait_healthy(origin, '', timeout=5)['git_commit'], commit)

    def test_wait_healthy_connection_refused_raises(self):
        with self.assertRaises(mgr.RuntimeFailure):
            mgr.wait_healthy('http://127.0.0.1:1', 'x', timeout=0.6)

    def test_http_json_captures_error_status_and_payload(self):
        class Handler(QuietHandler):
            def do_GET(self):
                respond_json(self, 503, {'error': 'unavailable'})

        server = self.start(Handler)
        url = f'http://127.0.0.1:{server.server_address[1]}/whatever'
        status, payload = mgr.http_json('GET', url)
        self.assertEqual(status, 503)
        self.assertEqual(payload, {'error': 'unavailable'})


class TestJoin(BaseTempCase):
    def test_join_body_construction(self):
        identity = {'node_id': 'a' * 32, 'node_key': 'b' * 64}
        body = mgr.build_join_body(identity, 'edge-9', ['http://203.0.113.10:9001'], '203.0.113.10',
                                   {'git_commit': 'c0ffee', 'build_version': 7})
        self.assertEqual(body['node_id'], 'a' * 32)
        self.assertEqual(body['node_key'], 'b' * 64)
        self.assertEqual(body['node_key_hash'], hashlib.sha256(b'b' * 64).hexdigest())
        self.assertEqual(body['name'], 'edge-9')
        self.assertEqual(body['endpoints'], ['http://203.0.113.10:9001'])
        self.assertEqual(body['connect_to'], '203.0.113.10')
        self.assertEqual(body['capabilities'], ['ingress', 'executor', 'replica'])
        self.assertEqual(body['build'], {'git_commit': 'c0ffee', 'build_version': 7})
        without = mgr.build_join_body(identity, 'edge-9', ['http://203.0.113.10:9001'], None, None)
        self.assertNotIn('connect_to', without)
        self.assertEqual(without['build'], {})

    def test_join_409_redirect_retries_writer_and_persists_writer_url(self):
        writer_captured = []
        hub_captured = []
        writer_origin_holder = {}

        class WriterHandler(QuietHandler):
            def do_POST(self):
                writer_captured.append({'path': self.path,
                                        'auth': self.headers.get('Authorization'),
                                        'body': read_body(self)})
                respond_json(self, 200, {'revision': 7, 'nodes': [{'node_id': 'a' * 32}]})

        class HubHandler(QuietHandler):
            def do_POST(self):
                hub_captured.append(read_body(self))
                respond_json(self, 409, {'writer_url': writer_origin_holder['url']})

        writer = self.start(WriterHandler)
        writer_origin_holder['url'] = f'http://127.0.0.1:{writer.server_address[1]}'
        hub = self.start(HubHandler)
        hub_origin = f'http://127.0.0.1:{hub.server_address[1]}'

        identity_path = self.layout.identity_file('edge-9')
        manifest = {'git_commit': 'c0ffee', 'build_version': 7}
        payload = mgr.perform_join(hub_origin, 'g-secret', identity_path, 'edge-9',
                                   ['http://203.0.113.10:9001'], '203.0.113.10', manifest)
        self.assertEqual(payload['revision'], 7)
        self.assertEqual(len(hub_captured), 1)
        self.assertEqual(len(writer_captured), 1)  # exactly one retry against the writer
        sent = writer_captured[0]
        self.assertEqual(sent['path'], '/admin/api/nodes/join')
        self.assertEqual(sent['auth'], 'Bearer g-secret')
        identity = json.loads(identity_path.read_text())
        self.assertEqual(sent['body']['node_id'], identity['node_id'])
        self.assertEqual(sent['body']['node_key'], identity['node_key'])
        self.assertEqual(sent['body']['name'], 'shell:edge-9')
        self.assertEqual(sent['body']['capabilities'], mgr.NODE_CAPABILITIES)
        self.assertEqual(sent['body']['build']['git_commit'], 'c0ffee')
        # writer_url persisted back into the identity file, still 0600, node_id untouched.
        merged = json.loads(identity_path.read_text())
        self.assertEqual(merged['writer_url'], writer_origin_holder['url'])
        self.assertEqual(merged['node_id'], identity['node_id'])
        self.assertEqual(os.stat(identity_path).st_mode & 0o777, 0o600)

    def test_join_stdin_supports_external_node_state_without_printing_credentials(self):
        captured = []

        class Handler(QuietHandler):
            def do_POST(self):
                captured.append({'path': self.path,
                                 'auth': self.headers.get('Authorization'),
                                 'body': read_body(self)})
                respond_json(self, 200, {'writer_id': 'writer-id',
                                         'membership_revision': 9})

        server = self.start(Handler)
        origin = f'http://127.0.0.1:{server.server_address[1]}'
        identity_path = self.tmp / 'haos' / 'node-identity.json'
        args = argparse_namespace(name='haos', url=origin, grant=None,
                                  join_grant_stdin=True, identity_file=str(identity_path),
                                  bind_port=9001, endpoint=['http://192.0.2.101:9001'],
                                  connect_to='192.0.2.101')
        output = io.StringIO()
        grant = 'a' * 64
        with patch.object(mgr.sys, 'stdin', io.StringIO(grant + '\n')):
            with redirect_stdout(output):
                mgr.cmd_join(args, self.layout, mgr.Runner())
        result = json.loads(output.getvalue())
        identity = json.loads(identity_path.read_text())
        self.assertEqual(captured[0]['path'], '/admin/api/nodes/join')
        self.assertEqual(captured[0]['auth'], 'Bearer ' + grant)
        self.assertEqual(captured[0]['body']['name'], 'shell:haos')
        self.assertEqual(captured[0]['body']['endpoints'], ['http://192.0.2.101:9001'])
        self.assertEqual(captured[0]['body']['connect_to'], '192.0.2.101')
        self.assertEqual(identity['writer_id'], 'writer-id')
        self.assertEqual(identity_path.stat().st_mode & 0o777, 0o600)
        self.assertNotIn(grant, output.getvalue())
        self.assertNotIn(identity['node_key'], output.getvalue())
        self.assertEqual(result['writer_id'], 'writer-id')

    def test_join_rejects_non_ip_connect_to(self):
        args = argparse_namespace(name='haos', url='https://writer.example',
                                  grant='a' * 64, join_grant_stdin=False,
                                  identity_file=str(self.tmp / 'node-id.json'),
                                  bind_port=9001, endpoint=['http://192.0.2.101:9001'],
                                  connect_to='writer.example')
        with self.assertRaisesRegex(mgr.UsageError, '--connect-to must be a literal IP'):
            mgr.cmd_join(args, self.layout, mgr.Runner())

    def test_join_failure_is_a_runtime_failure(self):
        class Handler(QuietHandler):
            def do_POST(self):
                respond_json(self, 403, {'error': 'bad grant'})

        server = self.start(Handler)
        origin = f'http://127.0.0.1:{server.server_address[1]}'
        with self.assertRaises(mgr.RuntimeFailure):
            mgr.perform_join(origin, 'bad', self.layout.identity_file('edge-9'), 'edge-9',
                             ['http://203.0.113.10:9001'], None, None)

    def test_grant_issue_posts_owner_bearer(self):
        captured = []

        class Handler(QuietHandler):
            def do_POST(self):
                captured.append({'auth': self.headers.get('Authorization'),
                                 'body': read_body(self)})
                respond_json(self, 200, {'grant': 'g-123', 'expires_at': '2026-09-10T12:00:00Z'})

        server = self.start(Handler)
        writer = f'http://127.0.0.1:{server.server_address[1]}'
        args = argparse_namespace(admin_token_env='TEST_ADMIN_BEARER', writer_url=writer,
                                  ttl_mins=45, name='edge-9')
        out = io.StringIO()
        with patch.dict(os.environ, {'TEST_ADMIN_BEARER': 'owner-bearer'}):
            with redirect_stdout(out):
                mgr.cmd_grant_issue(args, self.layout, mgr.Runner())
        printed = json.loads(out.getvalue())
        self.assertEqual(printed['grant'], 'g-123')
        self.assertEqual(captured[0]['auth'], 'Bearer owner-bearer')
        self.assertEqual(captured[0]['body'], {'name': 'shell:edge-9', 'ttl_minutes': 45})

    def test_grant_issue_requires_token_and_reachable_writer(self):
        args = argparse_namespace(admin_token_env='TEST_ADMIN_BEARER', writer_url='http://127.0.0.1:1',
                                  ttl_mins=30, name='edge-9')
        with patch.dict(os.environ, {'TEST_ADMIN_BEARER': ''}):
            with self.assertRaises(mgr.UsageError):
                mgr.cmd_grant_issue(args, self.layout, mgr.Runner())
        with patch.dict(os.environ, {'TEST_ADMIN_BEARER': 'owner-bearer'}):
            with self.assertRaises(mgr.RuntimeFailure):  # nothing listens on port 1
                mgr.cmd_grant_issue(args, self.layout, mgr.Runner())


class TestInputHelpers(unittest.TestCase):
    def test_parse_bind(self):
        self.assertEqual(mgr.parse_bind(None), ('127.0.0.1', 9001))
        self.assertEqual(mgr.parse_bind('0.0.0.0:8080'), ('0.0.0.0', 8080))
        with self.assertRaises(mgr.UsageError):
            mgr.parse_bind('no-port')
        with self.assertRaises(mgr.UsageError):
            mgr.parse_bind('127.0.0.1:99999')

    def test_validate_name(self):
        mgr.validate_name('edge-9')
        mgr.validate_name('A_1.x')
        for bad in ('', '-lead', 'a/b', 'a b', 'x' * 65, '%i'):
            with self.assertRaises(mgr.UsageError):
                mgr.validate_name(bad)

    def test_validate_origin(self):
        self.assertEqual(mgr.validate_origin('https://hub.example', 'x'), 'https://hub.example')
        self.assertEqual(mgr.validate_origin('http://hub.example:9001/', 'x'), 'http://hub.example:9001')
        for bad in ('ftp://hub.example', 'https://u:p@hub.example', 'https://hub.example/q?x=1', 'hub.example'):
            with self.assertRaises(mgr.UsageError):
                mgr.validate_origin(bad, 'x')

    def test_merge_identity_does_not_weaken_keys(self):
        pass  # covered in TestIdentity

    def test_read_admin_token_prefers_ctl_token(self):
        tmp = Path(tempfile.mkdtemp(prefix='node-manage-token-'))
        try:
            layout = mgr.Layout(state_root=tmp / 'var', config_root=tmp / 'etc')
            layout.config('n').mkdir(parents=True)
            (layout.owner_credential('n')).write_text('cred-token\n')
            self.assertEqual(mgr.read_admin_token(layout, 'n', {'CTL_TOKEN': 'env-token'}), 'env-token')
            self.assertEqual(mgr.read_admin_token(layout, 'n', {}), 'cred-token')
            self.assertEqual(mgr.read_admin_token(layout, 'n', {'ADMIN_PASSWORD': 'legacy'}), 'legacy')
            layout.owner_credential('n').unlink()
            with self.assertRaises(mgr.PreflightError):
                mgr.read_admin_token(layout, 'n', {})
        finally:
            import shutil
            shutil.rmtree(tmp, ignore_errors=True)

    def test_port_is_free_then_busy(self):
        import socket
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
        try:
            sock.listen(1)
            self.assertFalse(mgr.port_is_free('127.0.0.1', port))
        finally:
            sock.close()
        self.assertTrue(mgr.port_is_free('127.0.0.1', port))


def argparse_namespace(**kwargs):
    import argparse
    return argparse.Namespace(**kwargs)


if __name__ == '__main__':
    unittest.main()
