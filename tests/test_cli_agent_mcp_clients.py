"""Fast unit: preserve real client file formats; not runtime/provider proof."""
import importlib.util
import json
import pathlib
import stat
import tempfile
import unittest
import yaml

spec = importlib.util.spec_from_file_location('gptadmin_cli_agent_clients', pathlib.Path(__file__).parents[1] / 'cli.py')
cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cli)

class AgentClientConfigTests(unittest.TestCase):
    def test_yaml_peer_model_card_and_backup_are_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            p = root / 'fast-agent.yaml'
            original = {'default_model': 'exact-original-route', 'mcp': {'servers': {'peer': {'url': 'https://peer.example/mcp'}}}, 'plugins': {'enabled': ['existing']}}
            text = yaml.safe_dump(original)
            p.write_text(text)
            self.assertEqual('ok', cli._write_agent_mcp_config(p, 'https://hub.example/mcp', 'unit-only', yaml_format=True))
            result = yaml.safe_load(p.read_text())
            self.assertEqual(original['default_model'], result['default_model'])
            self.assertEqual(original['mcp']['servers']['peer'], result['mcp']['servers']['peer'])
            self.assertEqual(original['plugins'], result['plugins'])
            self.assertEqual('http', result['mcp']['servers']['gptadmin']['transport'])
            self.assertEqual('legacy', result['mcp']['servers']['gptadmin']['protocol_mode'])
            self.assertEqual('text', result['mcp']['servers']['gptadmin']['headers']['X-GPTAdmin-Result-Mode'])
            self.assertEqual(text, next(root.glob('*.bak.gptadmin-mcp.*')).read_text())
            self.assertEqual(0o600, stat.S_IMODE(p.stat().st_mode))
            self.assertEqual([], list(root.glob('.*.gptadmin-*')))
            card = root / 'card.yaml'
            card.write_text('model: original\nservers: [peer]\n')
            for _ in range(2):
                self.assertEqual('ok', cli._write_agent_mcp_config(card, '', '', yaml_format=True, agent_card=True))
            self.assertEqual({'model': 'original', 'servers': ['peer', 'gptadmin']}, yaml.safe_load(card.read_text()))

    def test_json_native_zcode_shape_keeps_provider_hooks_and_peers(self):
        with tempfile.TemporaryDirectory() as directory:
            p = pathlib.Path(directory) / 'config.json'
            original = {'model': {'main': 'account:zai/GLM-5.3-Flash$high'}, 'provider': {'original': True}, 'hooks': ['hook'], 'mcp': {'servers': {'peer': {'enabled': True}}}}
            p.write_text(json.dumps(original))
            self.assertEqual('ok', cli._write_agent_mcp_config(p, 'https://hub.example/mcp', 'unit-only'))
            result = json.loads(p.read_text())
            for key in ['model', 'provider', 'hooks']:
                self.assertEqual(original[key], result[key])
            self.assertEqual(original['mcp']['servers']['peer'], result['mcp']['servers']['peer'])
            self.assertEqual('http', result['mcp']['servers']['gptadmin']['type'])
            self.assertEqual('legacy', result['mcp']['servers']['gptadmin']['protocolVersion'])
            self.assertEqual('text', result['mcp']['servers']['gptadmin']['headers']['X-GPTAdmin-Result-Mode'])

    def test_bad_shape_does_not_overwrite_or_echo_source(self):
        with tempfile.TemporaryDirectory() as directory:
            for text in ['[]', '{"mcp": []}', '{"mcp": {"servers": []}}', 'sensitive-invalid-text']:
                p = pathlib.Path(directory) / 'config.json'
                p.write_text(text)
                result = cli._write_agent_mcp_config(p, 'https://hub.example/mcp', 'unit-only')
                self.assertTrue(result.startswith('error:'))
                self.assertNotIn(text, result)
                self.assertEqual(text, p.read_text())
                self.assertEqual([], list(p.parent.glob('*.bak.*')))

if __name__ == '__main__':
    unittest.main()
