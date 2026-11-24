"""Package staging for unit tests of Node update and rollback."""
from contextlib import contextmanager
from pathlib import Path
from shutil import copyfile
from tempfile import TemporaryDirectory
from unittest.mock import patch

from scripts import gptadmin_node_manage as manager
from tools import build_node


@contextmanager
def staged_update_package(extract):
    def extract_with_helpers(package, arch, target):
        manifest = extract(package, arch, target)
        for member in build_node.LINUX_PAYLOAD:
            if member.startswith("scripts/"):
                path = target / member
                path.parent.mkdir(parents=True, exist_ok=True)
                copyfile(build_node.ROOT / member, path)
        return manifest

    # Exercise the manager's real helper replacement inside each unit's fixture.
    with TemporaryDirectory() as destination, \
            patch.object(manager, "HELPER_INSTALL_DIR", Path(destination)), \
            patch.object(manager, "verify_and_extract", side_effect=extract_with_helpers):
        yield
