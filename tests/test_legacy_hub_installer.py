"""Local preflight logic only; stubbed commands never install or restart services."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


INSTALLER = Path(__file__).resolve().parents[1] / 'deploy/install_hub_and_client.sh'


class LegacyHubInstallerTests(unittest.TestCase):
    def run_preflight(self, exec_start, show_status=0):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            commands = root / 'commands'
            commands.mkdir()
            sudo_log = root / 'sudo.log'
            # Stop at the very first privileged command even if the guard regresses.
            (commands / 'sudo').write_text(
                '#!/bin/sh\nprintf "%s\\n" "$*" >> "$SUDO_LOG"\nexit 97\n')
            (commands / 'systemctl').write_text(
                '#!/bin/sh\n'
                '[ "$*" = "show gptadmin-hub.service -p ExecStart --value" ] || exit 98\n'
                'printf "%s\\n" "$PRIMARY_EXEC_START"\nexit "$SHOW_STATUS"\n')
            for command in commands.iterdir():
                command.chmod(0o755)
            result = subprocess.run(
                ['/bin/bash', str(INSTALLER)], capture_output=True, text=True,
                env={**os.environ, 'PATH': str(commands) + ':' + os.environ['PATH'],
                     'PRIMARY_EXEC_START': exec_start, 'SHOW_STATUS': str(show_status),
                     'SUDO_LOG': str(sudo_log)}, timeout=5)
            return result, sudo_log.read_text() if sudo_log.exists() else ''

    def test_unified_node_refuses_before_any_privileged_mutation(self):
        for path in ('/opt/gptadmin/bin/gptadmin-node',
                     '/opt/gptadmin-node/current/bin/gptadmin-node'):
            with self.subTest(path=path):
                result, privileged = self.run_preflight(
                    '{ path=' + path + ' ; argv[]=' + path + ' ; ignore_errors=no ; }')
                self.assertEqual(result.returncode, 1)
                self.assertIn('unified Node', result.stderr)
                self.assertEqual(privileged, '')

    def test_unknown_effective_service_refuses_before_mutation(self):
        result, privileged = self.run_preflight('', show_status=1)
        self.assertEqual(result.returncode, 1)
        self.assertIn('ExecStart', result.stderr)
        self.assertEqual(privileged, '')

    def test_genuine_legacy_service_reaches_existing_installer(self):
        result, privileged = self.run_preflight(
            '{ path=/opt/gptadmin/bin/gptadmin_hub ; argv[]=/opt/gptadmin/bin/gptadmin_hub ; }')
        self.assertEqual(result.returncode, 97)
        self.assertTrue(privileged.startswith('cp '))


if __name__ == '__main__':
    unittest.main()
