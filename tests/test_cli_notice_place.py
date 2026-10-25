"""Fast unit: one canonical Notice Place point, without damaging the client.

These checks prove the installer is non-destructive: comments, unrelated MCP
entries and native permission settings survive, aliases are disabled instead of
deleted, dry-run writes nothing, and a second apply changes nothing.
"""
import importlib.util
import json
import pathlib
import unittest

import yaml

spec = importlib.util.spec_from_file_location(
    'gptadmin_cli_notice_place', pathlib.Path(__file__).parents[1] / 'cli.py')
cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cli)


def _write_json(path: pathlib.Path, data) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2) + '\n')


def _write_yaml(path: pathlib.Path, data) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(yaml.safe_dump(data, allow_unicode=True, sort_keys=False))


class NoticePlaceInstallTests(unittest.TestCase):
    def setUp(self):
        import tempfile
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self._tmp = pathlib.Path(directory.name)
        self._home = self._tmp / 'home'
        self._original_home = cli.USER_HOME
        cli.USER_HOME = self._home
        self.addCleanup(lambda: setattr(cli, 'USER_HOME', self._original_home))
        self._original_local = cli._notice_place_local_server
        cli._notice_place_local_server = lambda: True
        self.addCleanup(lambda: setattr(cli, '_notice_place_local_server', self._original_local))

    # ---------- read-only status ----------

    def test_status_does_not_touch_any_file(self):
        config = self._home / '.config' / 'opencode' / 'opencode.json'
        _write_json(config, {'mcp': {'AskHuman': {'type': 'remote', 'url': 'https://notify.bezrabotnyi.com/mcp'}}})
        before = config.read_bytes()
        stat_before = config.stat().st_mtime_ns
        report = cli._notice_place_status('opencode')
        self.assertEqual(['AskHuman'], report['aliases'])
        self.assertEqual(before, config.read_bytes())
        self.assertEqual(stat_before, config.stat().st_mtime_ns)
        self.assertEqual([], list(config.parent.glob('*.bak.*')))

    def test_status_reports_every_exact_config_path(self):
        cli._notice_place_status('opencode')
        expected = {
            'opencode': ['opencode.json', 'opencode.jsonc'],
            'hermes': ['config.yaml'],
            'zcode': ['config.json'],
            'fast-agent': ['fast-agent.yaml'],
        }
        for client, names in expected.items():
            report = cli._notice_place_status(client)
            self.assertEqual(names, [pathlib.Path(p).name for p in report['files']], client)
        self.assertEqual(['config.toml'], [pathlib.Path(p).name for p in cli._notice_place_status('codex')['files']])

    # ---------- dry run ----------

    def test_dry_run_writes_nothing_and_reports_the_plan(self):
        config = self._home / '.config' / 'opencode' / 'opencode.json'
        _write_json(config, {'mcp': {'AskHuman': {'type': 'remote', 'url': 'https://notify.bezrabotnyi.com/mcp'}}})
        before = config.read_bytes()
        report = cli._notice_place_install_client('opencode', False)
        self.assertEqual('dry-run', report['status'])
        self.assertEqual(['AskHuman'], report['disabled'])
        self.assertEqual(before, config.read_bytes())
        self.assertEqual([], list(config.parent.glob('*.bak.*')))

    # ---------- apply: opencode ----------

    def test_opencode_alias_is_disabled_not_deleted_and_peers_survive(self):
        config = self._home / '.config' / 'opencode' / 'opencode.json'
        original = {
            '$schema': 'https://opencode.ai/config.json',
            'permission': {'edit': 'ask', 'bash': {'*': 'ask'}},
            'mcp': {
                'AskHuman': {'type': 'remote', 'url': 'https://notify.bezrabotnyi.com/mcp',
                             'headers': {'Authorization': 'Bearer unit-only'}},
                'noticeplace': {'type': 'remote', 'url': 'https://notify.bezrabotnyi.com/mcp',
                                'headers': {'Authorization': 'Bearer unit-only'}},
                'peer': {'type': 'remote', 'url': 'https://peer.example/mcp'},
                'UserIO': {'type': 'remote', 'url': 'https://msg.bezrabotnyi.com/mcp'},
            },
        }
        _write_json(config, original)
        report = cli._notice_place_install_client('opencode', True)
        self.assertEqual(['AskHuman'], report['disabled'])
        result = json.loads(config.read_text())
        # the alias stays in the file, switched off and reversible
        self.assertIn('AskHuman', result['mcp'])
        self.assertFalse(result['mcp']['AskHuman']['enabled'])
        self.assertEqual('https://notify.bezrabotnyi.com/mcp', result['mcp']['AskHuman']['url'])
        # the canonical point is the one credential-free Notice Place contract
        canonical = result['mcp']['noticeplace']
        self.assertEqual('local', canonical['type'])
        self.assertEqual(['/opt/noticeplace/bin/ask-human-mcp'], canonical['command'])
        self.assertEqual({'ASK_HUMAN_ENV_FILE': '/home/admin/.config/ask-human/env'},
                         canonical['environment'])
        self.assertNotIn('headers', canonical)
        # unrelated servers and native permissions are untouched
        self.assertEqual(original['mcp']['peer'], result['mcp']['peer'])
        self.assertEqual(original['mcp']['UserIO'], result['mcp']['UserIO'])
        self.assertEqual(original['permission'], result['permission'])
        self.assertEqual(original['$schema'], result['$schema'])
        self.assertEqual(1, len(list(config.parent.glob('*.bak.noticeplace.*'))))

    def test_opencode_jsonc_with_comments_is_never_rewritten(self):
        config = self._home / '.config' / 'opencode' / 'opencode.jsonc'
        config.parent.mkdir(parents=True, exist_ok=True)
        text = ('{\n  // keep this comment\n  "mcp": {\n    "notify": {\n'
                '      "type": "local",\n      "command": ["/opt/noticeplace/bin/ask-human-mcp"],\n'
                '      "enabled": true\n    }\n  }\n}\n')
        config.write_text(text)
        report = cli._notice_place_install_client('opencode', True)
        self.assertIn('comments', report['status'])
        self.assertEqual(text, config.read_text())
        self.assertEqual([], list(config.parent.glob('*.bak.*')))

    def test_jsonc_without_comments_is_supported(self):
        config = self._home / '.config' / 'opencode' / 'opencode.jsonc'
        _write_json(config, {'mcp': {'notify': {'type': 'local',
                                                 'command': ['/opt/noticeplace/bin/ask-human-mcp'],
                                                 'enabled': True}}})
        report = cli._notice_place_install_client('opencode', True)
        self.assertEqual(['notify'], report['disabled'])
        result = json.loads(config.read_text())
        self.assertFalse(result['mcp']['notify']['enabled'])
        self.assertIn('noticeplace', result['mcp'])

    # ---------- apply: hermes ----------

    def test_hermes_case_variant_is_renamed_to_the_canonical_name(self):
        config = self._home / '.hermes' / 'config.yaml'
        _write_yaml(config, {
            'model': 'original/model',
            'mcp_servers': {
                'AskHuman': {'url': 'https://notify.bezrabotnyi.com/mcp',
                             'headers': {'Authorization': 'Bearer unit-only'}},
                'NoticePlace': {'command': '/opt/noticeplace/bin/ask-human-mcp', 'enabled': True},
                'peer': {'url': 'https://peer.example/mcp'},
            },
        })
        report = cli._notice_place_install_client('hermes', True)
        self.assertIn('NoticePlace', report['renamed'])
        self.assertIn('AskHuman', report['disabled'])
        result = yaml.safe_load(config.read_text())
        self.assertIn('noticeplace', result['mcp_servers'])
        self.assertNotIn('NoticePlace', result['mcp_servers'])
        self.assertFalse(result['mcp_servers']['AskHuman']['enabled'])
        self.assertEqual('https://peer.example/mcp', result['mcp_servers']['peer']['url'])
        self.assertEqual('original/model', result['model'])

    def test_hermes_without_a_notice_place_entry_gets_the_canonical_one(self):
        config = self._home / '.hermes' / 'config.yaml'
        _write_yaml(config, {'mcp_servers': {'peer': {'url': 'https://peer.example/mcp'}}})
        report = cli._notice_place_install_client('hermes', True)
        self.assertEqual(['noticeplace'], report['added'])
        result = yaml.safe_load(config.read_text())
        self.assertEqual('/opt/noticeplace/bin/ask-human-mcp', result['mcp_servers']['noticeplace']['command'])
        self.assertEqual({'ASK_HUMAN_ENV_FILE': '/home/admin/.config/ask-human/env'},
                         result['mcp_servers']['noticeplace']['env'])

    # ---------- apply: codex ----------

    def test_codex_never_produces_a_duplicate_enabled_key(self):
        config = self._home / '.codex' / 'config.toml'
        config.parent.mkdir(parents=True, exist_ok=True)
        config.write_text(
            '[mcp_servers."AskHuman"]\n'
            'url = "https://notify.bezrabotnyi.com/mcp"\n'
            'enabled = false\n'
            '\n'
            '[mcp_servers."AskHuman".http_headers]\n'
            'Authorization = "Bearer unit-only"\n'
        )
        for run in range(3):
            report = cli._notice_place_install_client('codex', True)
            self.assertEqual([], report['disabled'])
        result = config.read_text()
        self.assertEqual(1, result.count('enabled = false'))
        # a duplicated key would make the whole Codex config unparsable
        self.assertEqual(1, result.count('[mcp_servers."AskHuman"]'))

    def test_codex_alias_table_is_disabled_and_other_tables_survive(self):
        config = self._home / '.codex' / 'config.toml'
        config.parent.mkdir(parents=True, exist_ok=True)
        text = (
            'approval_policy = "never"\n'
            'sandbox_mode = "danger-full-access"\n'
            '\n'
            '[mcp_servers."AskHuman"]\n'
            'url = "https://notify.bezrabotnyi.com/mcp"\n'
            '\n'
            '[mcp_servers."AskHuman".http_headers]\n'
            'Authorization = "Bearer unit-only"\n'
            '\n'
            '[mcp_servers.gptadmin]\n'
            'url = "https://mcp.bezrabotnyi.com/mcp"\n'
            '\n'
            '[hooks.state]\n'
            'ready = true\n'
        )
        config.write_text(text)
        report = cli._notice_place_install_client('codex', True)
        self.assertEqual(['AskHuman'], report['disabled'])
        result = config.read_text()
        self.assertIn('[mcp_servers."AskHuman"]', result)
        self.assertIn('enabled = false', result)
        self.assertIn('[mcp_servers.gptadmin]', result)
        self.assertIn('url = "https://mcp.bezrabotnyi.com/mcp"', result)
        self.assertIn('approval_policy = "never"', result)
        self.assertIn('sandbox_mode = "danger-full-access"', result)
        self.assertIn('[hooks.state]', result)
        self.assertEqual(1, len(list(config.parent.glob('*.bak.noticeplace.*'))))

    def test_codex_keeps_the_plugin_provided_canonical_point(self):
        config = self._home / '.codex' / 'config.toml'
        plugin = self._home / '.codex' / 'plugins' / 'cache' / 'noticeplace-local' / 'noticeplace' / '0.1.0'
        _write_json(plugin / 'mcp.codex.json', {'mcpServers': {
            'noticeplace': {'command': '/opt/noticeplace/bin/ask-human-mcp', 'args': [],
                            'env': {'ASK_HUMAN_ENV_FILE': '/home/admin/.config/ask-human/env'}}}})
        config.parent.mkdir(parents=True, exist_ok=True)
        config.write_text('[plugins."noticeplace@noticeplace-local"]\nenabled = true\n')
        report = cli._notice_place_install_client('codex', True)
        self.assertEqual([], report['added'])
        self.assertNotIn('[mcp_servers.noticeplace]', config.read_text())

    # ---------- apply: zcode ----------

    def test_zcode_disabled_alias_is_left_alone(self):
        config = self._home / '.zcode' / 'cli' / 'config.json'
        plugin = self._home / '.zcode' / 'cli' / 'plugins' / 'cache' / 'noticeplace-local' / 'noticeplace' / '0.1.0'
        _write_json(plugin / 'mcp.codex.json', {'mcpServers': {
            'noticeplace': {'command': '/opt/noticeplace/bin/ask-human-mcp', 'args': [],
                            'env': {'ASK_HUMAN_ENV_FILE': '/home/admin/.config/ask-human/env'}}}})
        _write_json(config, {'mcp': {'servers': {'AskHuman': {'url': 'https://notify.bezrabotnyi.com/mcp',
                                                              'enabled': False},
                                                'peer': {'url': 'https://peer.example/mcp'}}},
                              'plugins': {'enabledPlugins': {'noticeplace@noticeplace-local': True}}})
        report = cli._notice_place_install_client('zcode', True)
        self.assertEqual([], report['disabled'])
        self.assertEqual([], report['added'])
        result = json.loads(config.read_text())
        self.assertFalse(result['mcp']['servers']['AskHuman']['enabled'])
        self.assertEqual('https://peer.example/mcp', result['mcp']['servers']['peer']['url'])

    # ---------- idempotence ----------

    def test_alias_in_a_second_file_is_disabled_even_when_the_canonical_lives_in_the_first(self):
        first = self._home / '.config' / 'opencode' / 'opencode.json'
        second = self._home / '.config' / 'opencode' / 'opencode.jsonc'
        _write_json(first, {'mcp': {'noticeplace': {'type': 'local',
                                                    'command': ['/opt/noticeplace/bin/ask-human-mcp'],
                                                    'enabled': True,
                                                    'environment': {'ASK_HUMAN_ENV_FILE': '/home/admin/.config/ask-human/env'}}}})
        _write_json(second, {'mcp': {'notify': {'type': 'local',
                                                 'command': ['/opt/noticeplace/bin/ask-human-mcp'],
                                                 'enabled': True}}})
        report = cli._notice_place_install_client('opencode', True)
        self.assertEqual(['notify'], report['disabled'])
        self.assertEqual([], report['added'])
        self.assertFalse(json.loads(second.read_text())['mcp']['notify']['enabled'])
        self.assertEqual(1, len(json.loads(first.read_text())['mcp']))

    def test_canonical_entry_is_written_into_at_most_one_file(self):
        first = self._home / '.config' / 'opencode' / 'opencode.json'
        second = self._home / '.config' / 'opencode' / 'opencode.jsonc'
        _write_json(first, {'mcp': {'peer': {'type': 'remote', 'url': 'https://peer.example/mcp'}}})
        _write_json(second, {'mcp': {'peer2': {'type': 'remote', 'url': 'https://peer2.example/mcp'}}})
        report = cli._notice_place_install_client('opencode', True)
        self.assertEqual(['noticeplace'], report['added'])
        holders = [path for path in (first, second) if 'noticeplace' in json.loads(path.read_text())['mcp']]
        self.assertEqual(1, len(holders))

    def test_second_apply_changes_nothing(self):
        config = self._home / '.config' / 'opencode' / 'opencode.json'
        _write_json(config, {'mcp': {'AskHuman': {'type': 'remote', 'url': 'https://notify.bezrabotnyi.com/mcp'},
                                           'peer': {'type': 'remote', 'url': 'https://peer.example/mcp'}}})
        cli._notice_place_install_client('opencode', True)
        first = config.read_bytes()
        backups = sorted(config.parent.glob('*.bak.noticeplace.*'))
        report = cli._notice_place_install_client('opencode', True)
        self.assertEqual([], report['disabled'])
        self.assertEqual([], report['added'])
        self.assertEqual([], report['normalized'])
        self.assertEqual(first, config.read_bytes())
        self.assertEqual(backups, sorted(config.parent.glob('*.bak.noticeplace.*')))

    # ---------- detection ----------

    def test_backend_detection_matches_only_the_notice_place_server(self):
        self.assertEqual('endpoint', cli._notice_place_backend({'url': 'https://notify.bezrabotnyi.com/mcp'}))
        self.assertEqual('endpoint', cli._notice_place_backend({'url': 'https://notify.bezrabotnyi.com/mcp/'}))
        self.assertEqual('local', cli._notice_place_backend({'command': '/opt/noticeplace/bin/ask-human-mcp'}))
        self.assertEqual('local', cli._notice_place_backend({'command': ['/opt/noticeplace/bin/ask-human-mcp']}))
        self.assertEqual('local', cli._notice_place_backend({'command': '/opt/noticeplace/mcp/notify_mcp.py'}))
        self.assertIsNone(cli._notice_place_backend({'url': 'https://msg.bezrabotnyi.com/mcp'}))
        self.assertIsNone(cli._notice_place_backend({'url': 'https://pswd.bezrabotnyi.com/mcp'}))
        self.assertIsNone(cli._notice_place_backend({'command': '/home/admin/.local/bin/agent-herder'}))
        self.assertIsNone(cli._notice_place_backend(None))

    def test_host_without_the_notice_place_release_reuses_the_credential_it_already_has(self):
        cli._notice_place_local_server = lambda: False
        config = self._home / '.config' / 'opencode' / 'opencode.json'
        _write_json(config, {'mcp': {'AskHuman': {'type': 'remote', 'url': 'https://notify.bezrabotnyi.com/mcp',
                                                  'headers': {'Authorization': 'Bearer unit-only'}}}})
        report = cli._notice_place_install_client('opencode', True)
        self.assertEqual(['noticeplace'], report['added'])
        result = json.loads(config.read_text())
        canonical = result['mcp']['noticeplace']
        self.assertEqual('remote', canonical['type'])
        self.assertEqual('https://notify.bezrabotnyi.com/mcp', canonical['url'])
        # the credential is moved inside the same file, never invented
        self.assertEqual('Bearer unit-only', canonical['headers']['Authorization'])
        self.assertFalse(result['mcp']['AskHuman']['enabled'])

    def test_host_without_release_or_credential_is_left_untouched(self):
        cli._notice_place_local_server = lambda: False
        config = self._home / '.config' / 'opencode' / 'opencode.json'
        original = {'mcp': {'peer': {'type': 'remote', 'url': 'https://peer.example/mcp'}}}
        _write_json(config, original)
        report = cli._notice_place_install_client('opencode', True)
        self.assertIn('no canonical Notice Place point', report['status'])
        self.assertEqual([], report['added'])
        self.assertEqual(original, json.loads(config.read_text()))

    def test_alias_stays_live_when_no_canonical_point_can_be_created(self):
        cli._notice_place_local_server = lambda: False
        config = self._home / '.config' / 'opencode' / 'opencode.json'
        original = {'mcp': {'AskHuman': {'type': 'remote', 'url': 'https://notify.bezrabotnyi.com/mcp'}}}
        _write_json(config, original)
        report = cli._notice_place_install_client('opencode', True)
        self.assertIn('alias stays live', report['status'])
        self.assertEqual([], report['disabled'])
        result = json.loads(config.read_text())
        self.assertTrue(result['mcp']['AskHuman'].get('enabled', True))
        self.assertEqual(original, result)

    def test_codex_alias_stays_live_without_a_canonical_point(self):
        cli._notice_place_local_server = lambda: False
        config = self._home / '.codex' / 'config.toml'
        config.parent.mkdir(parents=True, exist_ok=True)
        original = ('[mcp_servers."AskHuman"]\n'
                    'url = "https://notify.bezrabotnyi.com/mcp"\n')
        config.write_text(original)
        report = cli._notice_place_install_client('codex', True)
        self.assertIn('alias stays live', report['status'])
        self.assertEqual([], report['disabled'])
        self.assertEqual(original, config.read_text())

    def test_codex_uses_the_credential_of_the_alias_it_replaces(self):
        cli._notice_place_local_server = lambda: False
        config = self._home / '.codex' / 'config.toml'
        config.parent.mkdir(parents=True, exist_ok=True)
        config.write_text('[mcp_servers."AskHuman"]\n'
                          'url = "https://notify.bezrabotnyi.com/mcp"\n'
                          '\n'
                          '[mcp_servers."AskHuman".http_headers]\n'
                          'Authorization = "Bearer unit-only"\n')
        report = cli._notice_place_install_client('codex', True)
        self.assertEqual(['AskHuman'], report['disabled'])
        self.assertEqual(['noticeplace'], report['added'])
        result = config.read_text()
        self.assertIn('[mcp_servers.noticeplace]', result)
        self.assertIn('Authorization = "Bearer unit-only"', result)
        self.assertEqual(1, result.count('enabled = false'))

    def test_status_never_prints_credentials(self):
        config = self._home / '.config' / 'opencode' / 'opencode.json'
        _write_json(config, {'mcp': {'AskHuman': {'type': 'remote', 'url': 'https://notify.bezrabotnyi.com/mcp',
                                                  'headers': {'Authorization': 'Bearer unit-secret-value'}}}})
        report = cli._notice_place_status('opencode')
        self.assertNotIn('unit-secret-value', json.dumps(report))


if __name__ == '__main__':
    unittest.main()
