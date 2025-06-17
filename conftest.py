import subprocess
import time
import os
import pytest
import sys
python = sys.executable

@pytest.fixture(scope="session", autouse=True)
def start_services():
    env_rootd = os.environ.copy()
    env_rootd["ROOTD_TOKEN"] = "srv_secret"
    env_rootd["HUB_URL"] = "http://localhost:8000"  # если нужно

    env_hub = os.environ.copy()
    env_hub["CTL_TOKEN"] = "chatgpt_secret"

    hub = subprocess.Popen([python, "hub_proxy.py"], env=env_hub)
    time.sleep(1)  # дать хабу стартовать

    rootd = subprocess.Popen([python, "rootd.py"], env=env_rootd)
    time.sleep(1)  # дать rootd подконнектиться

    yield  # тесты пойдут после этого

    rootd.terminate()
    hub.terminate()
    rootd.wait()
    hub.wait()
