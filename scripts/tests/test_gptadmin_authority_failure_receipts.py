"""Local receipt/command logic only; these tests do not claim fleet acceptance."""
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest

BASE = Path(__file__).resolve().parents[1]
SOURCE = BASE / 'scripts/gptadmin_authority_controller.py'
if not SOURCE.exists():
    SOURCE = BASE / 'gptadmin_authority_controller.py'
spec = importlib.util.spec_from_file_location('authority_failure_receipts_candidate', SOURCE)
controller = importlib.util.module_from_spec(spec)
spec.loader.exec_module(controller)


class FailureReceipts(unittest.TestCase):
    def make_controller(self, work, action):
        value = controller.Controller.__new__(controller.Controller)
        value.work = Path(work)
        value.work.chmod(0o700)
        value.manifest_hash = 'a' * 64
        value.lifecycle = type('Lifecycle', (), {})()
        value.freeze = action
        return value

    def test_real_failed_command_metadata_excludes_output_argv_and_environment(self):
        marker = 'SYNTHETIC_PRIVATE_MARKER_NOT_FOR_RECEIPT'
        calls = []

        def fail():
            calls.append(1)
            return controller.command(
                [sys.executable, '-c', 'import sys;print(sys.argv[1]);print(sys.argv[1],file=sys.stderr);sys.exit(7)', marker],
                env={'TEST_PRIVATE_TOKEN': marker}, stage='fixture.command_failure')

        with tempfile.TemporaryDirectory() as tmp:
            value = self.make_controller(tmp, fail)
            with self.assertRaises(controller.CommandFailure):
                value.execute('one-admission', 'freeze', [])
            raw = (Path(tmp) / 'step-one-admission.json').read_text()
            receipt = json.loads(raw)
            self.assertNotIn(marker, raw)
            self.assertNotIn('stdout', raw)
            self.assertNotIn('stderr', raw)
            self.assertNotIn('argv', raw)
            self.assertEqual(receipt['status'], 'started')
            self.assertEqual(receipt['execution_status'], 'failed')
            self.assertEqual(receipt['failure']['outcome'], 'unknown_requires_readback')
            self.assertEqual(receipt['failure']['command']['stage'], 'fixture.command_failure')
            self.assertEqual(receipt['failure']['command']['returncode'], 7)
            self.assertFalse(receipt['failure']['command']['timed_out'])
            self.assertGreaterEqual(receipt['failure']['command']['elapsed_seconds'], 0)
            with self.assertRaises(controller.Refused):
                value.execute('one-admission', 'freeze', [])
            self.assertEqual(calls, [1])
            self.assertEqual((Path(tmp) / 'step-one-admission.json').read_text(), raw)

    def test_real_timeout_is_durable_and_does_not_replay_command(self):
        marker = 'SYNTHETIC_PRIVATE_TIMEOUT_MARKER'
        calls = []

        def fail():
            calls.append(1)
            return controller.command([sys.executable, '-c',
                                       'import sys,time;print(sys.argv[1],flush=True);time.sleep(3)', marker],
                                      timeout=.05, stage='fixture.command_timeout')

        with tempfile.TemporaryDirectory() as tmp:
            value = self.make_controller(tmp, fail)
            with self.assertRaises(controller.CommandFailure):
                value.execute('one-timeout', 'freeze', [])
            raw = (Path(tmp) / 'step-one-timeout.json').read_text()
            failure = json.loads(raw)['failure']
            self.assertNotIn(marker, raw)
            self.assertTrue(failure['command']['timed_out'])
            self.assertIsNone(failure['command']['returncode'])
            self.assertEqual(failure['command']['timeout_seconds'], .05)
            with self.assertRaises(controller.Refused):
                value.execute('one-timeout', 'freeze', [])
            self.assertEqual(calls, [1])

    def test_unknown_existing_intent_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as tmp:
            value = self.make_controller(tmp, lambda: self.fail('action must not run'))
            path = Path(tmp) / 'step-existing.json'
            original = {'manifest_sha256': value.manifest_hash, 'action': 'freeze',
                        'arguments': [], 'status': 'started', 'pid': 1234}
            controller.private_json(path, original, True)
            before = path.read_bytes()
            with self.assertRaises(controller.Refused):
                value.execute('existing', 'freeze', [])
            self.assertEqual(path.read_bytes(), before)

    def test_success_keeps_normal_completed_receipt(self):
        with tempfile.TemporaryDirectory() as tmp:
            value = self.make_controller(tmp, lambda: {'accepted': True})
            receipt = value.execute('completed', 'freeze', [])
            self.assertEqual(receipt['status'], 'completed')
            self.assertNotIn('failure', receipt)
            self.assertEqual(receipt['result'], {'accepted': True})


if __name__ == '__main__':
    unittest.main()
