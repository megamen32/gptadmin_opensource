import argparse
import os
import stat
from contextlib import redirect_stdout
from io import StringIO
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

import cli


class StartupInstructionsCLITest(unittest.TestCase):
    def test_set_startup_instructions_file_is_private_and_does_not_print(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'owner.md'
            source.write_text('owner secret-like instruction', encoding='utf-8')
            destination = root / 'config' / 'startup_instructions.md'
            output = StringIO()

            with redirect_stdout(output):
                result = cli.set_startup_instructions_file(source, destination)

            self.assertEqual(result, destination)
            self.assertEqual(destination.read_text(encoding='utf-8'), 'owner secret-like instruction')
            self.assertEqual(stat.S_IMODE(destination.stat().st_mode), 0o600)
            self.assertEqual(output.getvalue(), '')

    def test_set_startup_instructions_rejects_oversized_source(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'too-large.md'
            source.write_bytes(b'x' * (cli.STARTUP_INSTRUCTIONS_MAX_BYTES + 1))

            with self.assertRaises(SystemExit):
                cli.set_startup_instructions_file(source, root / 'startup_instructions.md')

    def test_instructions_path_does_not_disclose_content(self):
        with TemporaryDirectory() as directory:
            path = Path(directory) / 'startup_instructions.md'
            path.write_text('do not disclose me', encoding='utf-8')
            old_path = cli.STARTUP_INSTRUCTIONS_FILE
            cli.STARTUP_INSTRUCTIONS_FILE = path
            output = StringIO()
            try:
                with redirect_stdout(output):
                    cli.cmd_instructions(argparse.Namespace(instructions_cmd='path'))
            finally:
                cli.STARTUP_INSTRUCTIONS_FILE = old_path

            self.assertIn(str(path), output.getvalue())
            self.assertNotIn('do not disclose me', output.getvalue())

    def test_instructions_show_is_explicit_content_output(self):
        with TemporaryDirectory() as directory:
            path = Path(directory) / 'startup_instructions.md'
            path.write_text('explicit output', encoding='utf-8')
            os.chmod(path, 0o600)
            old_path = cli.STARTUP_INSTRUCTIONS_FILE
            cli.STARTUP_INSTRUCTIONS_FILE = path
            output = StringIO()
            try:
                with redirect_stdout(output):
                    cli.cmd_instructions(argparse.Namespace(instructions_cmd='show'))
            finally:
                cli.STARTUP_INSTRUCTIONS_FILE = old_path

            self.assertEqual(output.getvalue(), 'explicit output')

    def test_instructions_set_file_prints_path_and_restart_not_content(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'source.md'
            source.write_text('private owner content', encoding='utf-8')
            destination = root / 'config' / 'startup_instructions.md'
            old_path = cli.STARTUP_INSTRUCTIONS_FILE
            cli.STARTUP_INSTRUCTIONS_FILE = destination
            output = StringIO()
            try:
                with redirect_stdout(output):
                    cli.cmd_instructions(argparse.Namespace(instructions_cmd='set-file', source=str(source)))
            finally:
                cli.STARTUP_INSTRUCTIONS_FILE = old_path

            self.assertIn(str(destination), output.getvalue())
            self.assertIn('gptadmin hub restart', output.getvalue())
            self.assertNotIn('private owner content', output.getvalue())
            self.assertEqual(stat.S_IMODE(destination.stat().st_mode), 0o600)


if __name__ == '__main__':
    unittest.main()
