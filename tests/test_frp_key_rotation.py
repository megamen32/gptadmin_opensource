"""Fast unit: credential-only edits and fail-closed file drift; max 15 s.

Synthetic values below prove local file logic, never fleet acceptance.
"""
import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('rotation', Path(__file__).parents[1] / 'tools/frp_key_rotation.py')
rotation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rotation)


class RotationTest(unittest.TestCase):
    old = b'old_' + b'a' * 48
    new = b'new_' + b'b' * 48

    def test_other_credentials_and_crlf_are_preserved(self):
        raw = b'OTHER_TOKEN="unrelated"\r\nFRP_TOKEN="' + self.old + b'"\r\n'
        changed = rotation.replace_key(raw, 'env', rotation.digest(self.old), self.new)
        self.assertEqual(changed, b'OTHER_TOKEN="unrelated"\r\nFRP_TOKEN="' + self.new + b'"\r\n')

    def test_missing_or_duplicate_old_key_refused(self):
        for raw in [b'FRP_TOKEN="foreign"\n', (b'token="' + self.old + b'"\n') * 2]:
            with self.assertRaises(ValueError):
                rotation.replace_key(raw, 'toml', rotation.digest(self.old), self.new)

    def test_all_pins_checked_before_mutation(self):
        with tempfile.TemporaryDirectory() as directory:
            d = Path(directory)
            a, b = d / 'a', d / 'b'
            for p in [a, b]:
                p.write_bytes(self.old)
            plan = {'previous_key_sha256': rotation.digest(self.old), 'files': [
                {'path': str(a), 'kind': 'token-file', 'sha256': rotation.digest(self.old)},
                {'path': str(b), 'kind': 'token-file', 'sha256': 'drift'}]}
            with self.assertRaises(ValueError):
                rotation.apply(plan, self.new, d / 'backup')
            self.assertEqual(a.read_bytes(), self.old)
            self.assertFalse((d / 'backup').exists())

    def test_symlink_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            d = Path(directory)
            real = d / 'real'
            real.write_bytes(self.old)
            link = d / 'link'
            link.symlink_to(real)
            plan = {'previous_key_sha256': rotation.digest(self.old), 'files': [
                {'path': str(link), 'kind': 'token-file', 'sha256': rotation.digest(self.old)}]}
            with self.assertRaises(ValueError):
                rotation.apply(plan, self.new, d / 'backup')
            self.assertEqual(real.read_bytes(), self.old)

    def test_protected_backup_and_owner_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            d = Path(directory)
            p = d / 'key'
            p.write_bytes(self.old)
            p.chmod(0o640)
            info = p.stat()
            plan = {'previous_key_sha256': rotation.digest(self.old), 'files': [
                {'path': str(p), 'kind': 'token-file', 'sha256': rotation.digest(self.old)}]}
            receipts = rotation.apply(plan, self.new, d / 'backup')
            self.assertEqual(p.read_bytes(), self.new + b'\n')
            self.assertEqual((p.stat().st_uid, p.stat().st_gid), (info.st_uid, info.st_gid))
            self.assertEqual(p.stat().st_mode & 0o777, 0o640)
            self.assertEqual((d / 'backup/0').read_bytes(), self.old)
            self.assertEqual((d / 'backup').stat().st_mode & 0o777, 0o700)
            self.assertNotIn(self.new.decode(), str(receipts))
            with self.assertRaises(ValueError):
                rotation.apply(plan, self.new, d / 'backup')


if __name__ == '__main__':
    unittest.main()
