import subprocess
import time
import os
import pytest
import sys
from pathlib import Path
python = sys.executable

@pytest.fixture(scope="session", autouse=True)
def start_services():
    env_rootd = os.environ.copy()
    env_rootd["ROOTD_TOKEN"] = "secret_for_server"
    env_rootd["HUB_URL"] = "http://localhost:8000"  # если нужно

    env_hub = os.environ.copy()
    env_hub["CTL_TOKEN"] = "secret_for_chatgpt"

    repo_dir = Path(__file__).resolve().parents[1]
    hub_path = repo_dir / "services" / "hub_proxy.py"
    rootd_path = repo_dir / "services" / "rootd.py"

    hub = subprocess.Popen([python, str(hub_path)], env=env_hub)
    time.sleep(1)  # дать хабу стартовать

    rootd = subprocess.Popen([python, str(rootd_path)], env=env_rootd)
    time.sleep(1)  # дать rootd подконнектиться

    yield  # тесты пойдут после этого

    rootd.terminate()
    hub.terminate()
    rootd.wait()
    hub.wait()
