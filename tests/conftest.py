import os
import subprocess
import sys
import time
from pathlib import Path

import pytest

python = sys.executable


@pytest.fixture(scope="session", autouse=True)
def start_services():
    """Start live hub/shellmcp only for legacy integration tests.

    Most tests are unit tests and should not spawn network services implicitly.
    Set GPTADMIN_INTEGRATION_TESTS=1 when the old request-based tests need a
    local hub/shellmcp pair.
    """
    if os.environ.get("GPTADMIN_INTEGRATION_TESTS") != "1":
        yield
        return

    env_shellmcp = os.environ.copy()
    env_shellmcp["SHELLMCP_TOKEN"] = "srv_secret"
    env_shellmcp["HUB_URL"] = "http://localhost:8000"

    env_hub = os.environ.copy()
    env_hub["CTL_TOKEN"] = "chatgpt_secret"

    repo_dir = Path(__file__).resolve().parents[1]
    hub_path = repo_dir / "services" / "hub_proxy.py"
    shellmcp_path = repo_dir / "services" / "shellmcp.py"

    hub = subprocess.Popen([python, str(hub_path)], env=env_hub)
    time.sleep(1)

    shellmcp = subprocess.Popen([python, str(shellmcp_path)], env=env_shellmcp)
    time.sleep(1)

    yield

    shellmcp.terminate()
    hub.terminate()
    shellmcp.wait()
    hub.wait()
