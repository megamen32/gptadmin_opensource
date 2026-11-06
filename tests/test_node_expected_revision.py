"""Local real-filesystem CAS tests, not deployment acceptance."""
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from contextlib import ExitStack
from types import SimpleNamespace

from scripts import gptadmin_node_manage as node


class ExpectedRevisionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.layout = node.Layout(opt_root=pathlib.Path(self.tmp.name))
        self.layout.versions_root().mkdir()
        self.old = self.layout.opt_root / "old"
        self.new = self.layout.opt_root / "new"
        for path, revision in ((self.old, "a" * 40), (self.new, "b" * 40)):
            path.mkdir()
            (path / "manifest.json").write_text(json.dumps({"git_commit": revision}))
        node.switch_current(self.layout, self.old)

    def test_matching_revision_switches_atomically(self):
        node.switch_current(self.layout, self.new, expected_revision="a" * 40)
        self.assertEqual(node.current_target(self.layout), self.new)

    def test_stale_revision_preserves_current(self):
        before = self.layout.current_link().lstat().st_ino
        with self.assertRaisesRegex(node.PreflightError, "revision"):
            node.switch_current(self.layout, self.new, expected_revision="c" * 40)
        self.assertEqual(node.current_target(self.layout), self.old)
        self.assertEqual(self.layout.current_link().lstat().st_ino, before)

    def test_unreadable_manifest_fails_closed(self):
        (self.old / "manifest.json").write_text("invalid")
        with self.assertRaisesRegex(node.PreflightError, "revision"):
            node.switch_current(self.layout, self.new, expected_revision="a" * 40)
        self.assertEqual(node.current_target(self.layout), self.old)

    def test_concurrent_lifecycle_holder_blocks_switch(self):
        program = """import sys
from pathlib import Path
from scripts import gptadmin_node_manage as node
with node.lifecycle_lock(node.Layout(opt_root=Path(sys.argv[1]))):
    print("locked", flush=True)
    sys.stdin.readline()
"""
        child = subprocess.Popen([sys.executable, "-c", program, str(self.layout.opt_root)],
                                 stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
        try:
            self.assertEqual(child.stdout.readline().strip(), "locked")
            with self.assertRaisesRegex(node.PreflightError, "lock"):
                node.switch_current(self.layout, self.new, expected_revision="a" * 40)
            self.assertEqual(node.current_target(self.layout), self.old)
        finally:
            child.communicate("\n", timeout=10)
        self.assertEqual(child.returncode, 0)

    def test_interrupt_after_pointer_switch_uses_actual_installed_state(self):
        # Narrow local failure-path test. Process/network boundaries are mocked;
        # only the real filesystem pointer and revision guard are under test.
        original_switch = node.switch_current
        def extract(package, arch, target):
            target.mkdir()
            manifest = {"git_commit": "b" * 40, "build_version": 1}
            (target / "manifest.json").write_text(json.dumps(manifest))
            return manifest
        def interrupt_after_switch(layout, target, **kwargs):
            original_switch(layout, target, **kwargs)
            if target != self.old:
                raise node.RuntimeFailure("interrupt immediately after pointer switch")
        args = SimpleNamespace(name="unit", package="unit.tar.gz", wait_secs=1,
                               require_drain=True, expected_revision="a" * 40)
        with ExitStack() as stack:
            for name, value in (("require_root", None), ("resolve_instance_name", "unit"),
                                ("read_installed_env", {}), ("read_admin_token", "unit-only"),
                                ("origin_from_env", "http://unit.invalid"), ("host_arch", "amd64"),
                                ("check_disk_floor", None), ("verify_elf_binary", None),
                                ("prepare_update_drain", True), ("wait_healthy", None),
                                ("drain_cancel", True)):
                stack.enter_context(patch.object(node, name, return_value=value))
            stack.enter_context(patch.object(node, "verify_and_extract", side_effect=extract))
            stack.enter_context(patch.object(node, "switch_current", side_effect=interrupt_after_switch))
            restart = stack.enter_context(patch.object(node, "systemctl"))
            with self.assertRaisesRegex(node.RuntimeFailure, "rolled back"):
                node.cmd_update(args, self.layout, node.Runner())
        self.assertEqual(node.current_target(self.layout), self.old)
        restart.assert_called_once()

    def test_cli_exposes_expected_revision(self):
        args = node.build_parser().parse_args(["update", "--package", "test.tar.gz",
                                              "--expected-revision", "a" * 40])
        self.assertEqual(args.expected_revision, "a" * 40)


if __name__ == "__main__":
    unittest.main()
