"""Signals received by the update manager must enter its rollback path."""
import subprocess
import sys
import unittest


class NodeUpdateInterruptTests(unittest.TestCase):
    def test_termination_and_interrupt_are_catchable_during_update(self):
        program = r'''
import os, signal, sys
from scripts.gptadmin_node_manage import RuntimeFailure, UpdateBudget
chosen = getattr(signal, sys.argv[1])
previous = signal.getsignal(chosen)
try:
    with UpdateBudget(None) as budget:
        try:
            os.kill(os.getpid(), chosen)
        except RuntimeFailure:
            budget.cleanup()
            os.kill(os.getpid(), chosen)
            print("rollback path")
        else:
            raise SystemExit("signal was not handled")
    assert signal.getsignal(chosen) == previous
except RuntimeFailure as error:
    raise SystemExit(str(error))
else:
    print("signal handler restored")
'''
        for name in ("SIGTERM", "SIGINT"):
            with self.subTest(signal=name):
                result = subprocess.run([sys.executable, "-c", program, name],
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("rollback path", result.stdout)
                self.assertIn("signal handler restored", result.stdout)


if __name__ == "__main__":
    unittest.main()
