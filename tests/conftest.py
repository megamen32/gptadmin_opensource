import os
import subprocess
import sys
import time
from pathlib import Path

import pytest

python = sys.executable


@pytest.fixture(scope="session", autouse=True)
def start_services():
    """Start live hub/rootd only for legacy integration tests.

    Most tests are unit tests and should not spawn network services implicitly.
    Set GPTADMIN_INTEGRATION_TESTS=1 when the old request-based tests need a
    local hub/rootd pair.
    """
    if os.environ.get("GPTADMIN_INTEGRATION_TESTS") != "1":
        yield
        return

    env_rootd = os.environ.copy()
    env_rootd["ROOTD_TOKEN"] = "srv_secret"
    env_rootd["HUB_URL"] = "http://localhost:8000"

    env_hub = os.environ.copy()
    env_hub["CTL_TOKEN"] = "chatgpt_secret"

    repo_dir = Path(__file__).resolve().parents[1]
    hub_path = repo_dir / "services" / "hub_proxy.py"
    rootd_path = repo_dir / "services" / "rootd.py"

    hub = subprocess.Popen([python, str(hub_path)], env=env_hub)
    time.sleep(1)

    rootd = subprocess.Popen([python, str(rootd_path)], env=env_rootd)
    time.sleep(1)

    yield

    rootd.terminate()
    hub.terminate()
    rootd.wait()
    hub.wait()
