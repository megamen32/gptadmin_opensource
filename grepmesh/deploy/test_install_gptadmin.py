import importlib.util
from pathlib import Path
import sys
import unittest


MODULE_PATH = Path(__file__).with_name("install_gptadmin.py")
sys.path.insert(0, str(MODULE_PATH.parent))
SPEC = importlib.util.spec_from_file_location("install_gptadmin", MODULE_PATH)
installer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(installer)
CLIENT_MODULE_PATH = Path(__file__).with_name("install_clients.py")
CLIENT_SPEC = importlib.util.spec_from_file_location("install_clients", CLIENT_MODULE_PATH)
clients = importlib.util.module_from_spec(CLIENT_SPEC)
CLIENT_SPEC.loader.exec_module(clients)


class ReplaceInstructionSectionTests(unittest.TestCase):
    def test_first_install_and_rerun_are_idempotent(self):
        original = "Other startup guidance.\n"
        installed = installer.replace_instruction_section(original)
        self.assertTrue(installed.startswith(original + "\n"))
        self.assertEqual(installer.replace_instruction_section(installed), installed)
        self.assertEqual(installed.count(installer.BEGIN_MARKER), 1)

    def test_marked_section_replacement_preserves_surrounding_text(self):
        original = (
            "Before.\n\n" + installer.BEGIN_MARKER + "\nold\n"
            + installer.END_MARKER + "\n\nAfter.\n"
        )
        updated = installer.replace_instruction_section(original)
        self.assertTrue(updated.startswith("Before.\n\n" + installer.BLOCK))
        self.assertTrue(updated.endswith("\n\nAfter.\n"))
        self.assertEqual(updated.count(installer.BEGIN_MARKER), 1)

    def test_legacy_section_is_replaced_without_losing_next_section(self):
        original = (
            "Before.\n\n## GrepMesh search routing\n\n"
            + installer.LEGACY_INSTRUCTIONS[0]
            + "\n\n### Local note\nPreserve this text.\n\n## Other section\nKeep me.\n"
        )
        updated = installer.replace_instruction_section(original)
        self.assertIn("## GrepMesh search routing\n\n" + installer.BLOCK, updated)
        self.assertIn("### Local note\nPreserve this text.\n", updated)
        self.assertIn("## Other section\nKeep me.\n", updated)
        self.assertNotIn(installer.LEGACY_INSTRUCTIONS[0], updated)
        self.assertEqual(installer.replace_instruction_section(updated), updated)

    def test_duplicate_or_unrecognized_sections_fail_closed(self):
        duplicated = installer.SECTION + "\n" + installer.SECTION
        with self.assertRaises(ValueError):
            installer.replace_instruction_section(duplicated)
        with self.assertRaises(ValueError):
            installer.replace_instruction_section("## GrepMesh search routing\n\nCustom text\n")

    def test_client_installer_migrates_and_updates_prompt_block(self):
        old_prompt = "Keep this.\n\n" + clients.CLIENT_LEGACY_INSTRUCTION + "\n\nOther prompt."
        migrated = clients.replace_prompt_section(old_prompt)
        self.assertIn("Keep this.", migrated)
        self.assertIn("Other prompt.", migrated)
        self.assertNotIn(clients.CLIENT_LEGACY_INSTRUCTION, migrated)
        self.assertEqual(clients.replace_prompt_section(migrated), migrated)

    def test_client_installer_migrates_instruction_file_legacy_policy(self):
        old_file = (
            "## GrepMesh search routing\n\n"
            + clients.CLIENT_LEGACY_INSTRUCTION
            + "\n\n## Following section\nPreserve.\n"
        )
        migrated = installer.replace_instruction_section(old_file)
        self.assertIn(installer.BLOCK, migrated)
        self.assertIn("## Following section\nPreserve.\n", migrated)
        self.assertNotIn(clients.CLIENT_LEGACY_INSTRUCTION, migrated)


if __name__ == "__main__":
    unittest.main()
