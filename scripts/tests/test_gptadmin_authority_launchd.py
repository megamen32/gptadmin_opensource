"""Narrow launchd observation/one-stop logic, without production services."""
import importlib.util
from pathlib import Path
import subprocess
import unittest
from unittest import mock

BASE = Path(__file__).resolve().parents[1]
SOURCE = BASE / 'scripts/gptadmin_authority_controller.py'
if not SOURCE.exists():
    SOURCE = BASE / 'gptadmin_authority_controller.py'
spec = importlib.util.spec_from_file_location('authority_launchd_candidate', SOURCE)
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)


def result(rc=0, stdout=b'', stderr=b''):
    value = subprocess.CompletedProcess([], rc, stdout, stderr)
    value.safe_metadata = {'command': 'launchctl', 'stage': 'launchd.bootout',
                           'returncode': rc, 'timed_out': False, 'elapsed_seconds': .01}
    return value


class LaunchdObservations(unittest.TestCase):
    def setUp(self):
        self.uid = mock.patch.object(c.os, 'getuid', return_value=501)
        self.uid.start()
        self.addCleanup(self.uid.stop)
        self.life = c.Launchd({'uid': 501, 'label': 'com.gptadmin.fixture'})
        self.live = {'loaded': True, 'pid': 123, 'generation': 'old-start', 'domain': 'gui/501'}
        self.stopped = {'loaded': False, 'pid': 0, 'generation': '', 'domain': 'gui/501'}

    def missing(self, label='com.gptadmin.fixture', uid=501):
        return result(113, stderr=('Bad request.\nCould not find service "' + label +
                                  '" in domain for user gui: ' + str(uid) + '\n').encode())

    def test_exact_missing_service_requires_existing_domain(self):
        with mock.patch.object(c.subprocess, 'run', side_effect=[self.missing(), result()]) as run:
            self.assertEqual(self.life.job('com.gptadmin.fixture'), {'loaded': False, 'pid': 0})
            self.assertEqual(run.call_args_list[1].args[0], ['/bin/launchctl', 'print', 'gui/501'])

    def test_permission_or_transport_error_is_not_unloaded(self):
        for rc, message in [(1, b'Operation not permitted'), (5, b'Input/output error'), (113, b'Could not find domain')]:
            with self.subTest(rc=rc), mock.patch.object(c.subprocess, 'run', return_value=result(rc, stderr=message)):
                with self.assertRaises(c.CommandFailure):
                    self.life.job('com.gptadmin.fixture')

    def test_wrong_missing_label_or_domain_is_rejected(self):
        for reply in [self.missing('different'), self.missing(uid=502)]:
            with mock.patch.object(c.subprocess, 'run', return_value=reply):
                with self.assertRaises(c.CommandFailure):
                    self.life.job('com.gptadmin.fixture')

    def test_domain_failure_cannot_confirm_absence(self):
        with mock.patch.object(c.subprocess, 'run', side_effect=[self.missing(), result(1, stderr=b'permission denied')]):
            with self.assertRaises(c.CommandFailure):
                self.life.job('com.gptadmin.fixture')

    def test_loaded_without_pid_is_not_stopped(self):
        with mock.patch.object(c.subprocess, 'run', return_value=result(stdout=b'gui/501/com.gptadmin.fixture = {\n state = waiting\n}\n')):
            value = self.life.job('com.gptadmin.fixture')
            self.assertTrue(value['loaded'])
            self.assertEqual(value['pid'], 0)
            self.assertFalse(self.life.stopped(value))

    def test_process_missing_is_specific_empty_ps_exit_one(self):
        with mock.patch.object(c.subprocess, 'run', return_value=result(1)):
            self.assertIsNone(self.life.process_generation(123))
        for reply in [result(1, stderr=b'permission denied'), result(2), result(0)]:
            with mock.patch.object(c.subprocess, 'run', return_value=reply):
                with self.assertRaises(c.CommandFailure):
                    self.life.process_generation(123)

    def test_pid_exit_between_manager_and_ps_reloads_manager(self):
        with mock.patch.object(self.life, 'job', side_effect=[{'loaded': True, 'pid': 123}, {'loaded': False, 'pid': 0}]) as job, \
                mock.patch.object(self.life, 'process_generation', return_value=None), \
                mock.patch.object(c, 'command', return_value=result(stdout=b'1\n')):
            value = self.life.info()
            self.assertTrue(self.life.stopped(value))
            self.assertEqual(job.call_count, 2)

    def test_stale_loaded_pid_cannot_become_false_stopped(self):
        with mock.patch.object(self.life, 'job', return_value={'loaded': True, 'pid': 123}), \
                mock.patch.object(self.life, 'process_generation', return_value=None), \
                mock.patch.object(c, 'command', return_value=result(stdout=b'1\n')):
            value = self.life.info()
            self.assertEqual(value['generation'], '')
            self.assertFalse(self.life.stopped(value))

    def test_replacement_pid_is_observed_as_new_generation(self):
        with mock.patch.object(self.life, 'job', side_effect=[{'loaded': True, 'pid': 123}, {'loaded': True, 'pid': 456}]), \
                mock.patch.object(self.life, 'process_generation', side_effect=[None, 'new-start']), \
                mock.patch.object(c, 'command', return_value=result(stdout=b'1\n')):
            value = self.life.info()
            self.assertEqual((value['pid'], value['generation']), (456, 'new-start'))
            self.assertFalse(self.life.stopped(value))

    def test_one_bootout_nonzero_can_be_resolved_only_by_strong_reads(self):
        with mock.patch.object(self.life, 'info', side_effect=[self.live, self.stopped]), \
                mock.patch.object(self.life, 'process_generation', return_value=None), \
                mock.patch.object(c, 'command', return_value=result(5)) as command:
            value = self.life.stop(dict(self.live))
            self.assertEqual(command.call_count, 1)
            self.assertEqual(command.call_args.args[0], ['/bin/launchctl', 'bootout', 'gui/501/com.gptadmin.fixture'])
            self.assertTrue(value['stop_confirmation']['prior_process_gone'])
            self.assertEqual(value['stop_confirmation']['command']['returncode'], 5)

    def test_lost_bootout_response_is_read_not_resent(self):
        timeout = c.CommandFailure('deadline', {'stage': 'launchd.bootout', 'timed_out': True, 'returncode': None})
        with mock.patch.object(self.life, 'info', side_effect=[self.live, self.stopped]), \
                mock.patch.object(self.life, 'process_generation', return_value=None), \
                mock.patch.object(c, 'command', side_effect=timeout) as command:
            value = self.life.stop(dict(self.live))
            self.assertTrue(value['stop_confirmation']['command']['timed_out'])
            self.assertEqual(command.call_count, 1)

    def test_job_absent_but_original_process_alive_is_not_confirmed(self):
        with mock.patch.object(self.life, 'info', side_effect=[self.live, self.stopped]), \
                mock.patch.object(self.life, 'process_generation', return_value='old-start'), \
                mock.patch.object(c, 'command', return_value=result()), \
                mock.patch.object(c.time, 'monotonic', side_effect=[0, 0, 16]), \
                mock.patch.object(c.time, 'sleep'):
            with self.assertRaises(c.CommandFailure):
                self.life.stop(dict(self.live))
            self.assertNotIn('prior_process_gone', self.life.last_stop_metadata)

    def test_unknown_readback_never_authorizes_stopped(self):
        unknown = c.CommandFailure('unknown manager', {'stage': 'launchd.print_job', 'returncode': 5})
        with mock.patch.object(self.life, 'info', side_effect=[self.live, unknown]), \
                mock.patch.object(c, 'command', return_value=result()) as command, \
                mock.patch.object(c.time, 'monotonic', side_effect=[0, 0, 16]), \
                mock.patch.object(c.time, 'sleep'):
            with self.assertRaises(c.CommandFailure):
                self.life.stop(dict(self.live))
            self.assertEqual(command.call_count, 1)
            self.assertEqual(self.life.last_stop_metadata['readback_failure']['returncode'], 5)

    def test_changed_fenced_generation_rejects_before_bootout(self):
        with mock.patch.object(self.life, 'info', return_value=self.live), mock.patch.object(c, 'command') as command:
            with self.assertRaises(c.Refused):
                self.life.stop(dict(self.live, generation='different'))
            command.assert_not_called()


if __name__ == '__main__':
    unittest.main()
