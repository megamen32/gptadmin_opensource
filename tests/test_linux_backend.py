import sys
import importlib
import pytest
from pathlib import Path
from unittest.mock import patch, MagicMock

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

import client.shellmcp_linux as linux

def test_info_with_psutil():
    with patch.dict('sys.modules', {'psutil': MagicMock()}):
        importlib.reload(linux)

        assert linux.HAS_PSUTIL is True

        linux.psutil.cpu_count.return_value = 8
        linux.psutil.virtual_memory.return_value.total = 16 * 1024**3
        linux.psutil.boot_time.return_value = 1000.0

        with patch('time.time', return_value=2000.0):
            info = linux.info()

        assert info['cores'] == 8
        assert info['mem_mb'] == 16 * 1024
        assert info['uptime_s'] == 1000

def test_info_without_psutil():
    with patch.dict('sys.modules', {'psutil': None}):
        importlib.reload(linux)

        assert linux.HAS_PSUTIL is False

        with patch('client.shellmcp_linux._fallback_cpu_count', return_value=4), \
             patch('client.shellmcp_linux._fallback_virtual_memory') as mock_vm, \
             patch('client.shellmcp_linux._fallback_uptime_s', return_value=1000):

            mock_vm.return_value.total = 16 * 1024**3
            info = linux.info()

        assert info['cores'] == 4
        assert info['mem_mb'] == 16 * 1024
        assert info['uptime_s'] == 1000

def test_health_with_psutil():
    with patch.dict('sys.modules', {'psutil': MagicMock()}):
        importlib.reload(linux)

        assert linux.HAS_PSUTIL is True

        linux.psutil.virtual_memory.return_value.total = 16 * 1024**3
        linux.psutil.virtual_memory.return_value.available = 8 * 1024**3
        linux.psutil.virtual_memory.return_value.used = 8 * 1024**3
        linux.psutil.virtual_memory.return_value.free = 8 * 1024**3

        linux.psutil.swap_memory.return_value.total = 4 * 1024**3
        linux.psutil.swap_memory.return_value.used = 1 * 1024**3
        linux.psutil.swap_memory.return_value.free = 3 * 1024**3

        linux.psutil.cpu_percent.return_value = 50.0
        linux.psutil.boot_time.return_value = 1000.0
        linux.psutil.sensors_temperatures.return_value = {}

        with patch('time.time', return_value=2000.0), \
             patch('shutil.disk_usage') as mock_du, \
             patch('os.getloadavg', return_value=(1.0, 2.0, 3.0)), \
             patch('subprocess.run') as mock_run, \
             patch('pathlib.Path.exists', return_value=False):

            mock_du.return_value.total = 100 * 1024**3
            mock_du.return_value.used = 50 * 1024**3
            mock_du.return_value.free = 50 * 1024**3
            mock_run.return_value.stdout = ""

            health = linux.health()

        assert health['cpu_usage_pct'] == 50.0
        assert health['memory']['total'] == 16 * 1024
        assert health['load_avg'] == (1.0, 2.0, 3.0)

def test_health_without_psutil():
    with patch.dict('sys.modules', {'psutil': None}):
        importlib.reload(linux)

        assert linux.HAS_PSUTIL is False

        with patch('client.shellmcp_linux._fallback_virtual_memory') as mock_vm, \
             patch('client.shellmcp_linux._fallback_swap_memory') as mock_swap, \
             patch('client.shellmcp_linux._fallback_cpu_percent', return_value=25.0), \
             patch('client.shellmcp_linux._fallback_uptime_s', return_value=1000), \
             patch('client.shellmcp_linux._fallback_sensors_temperatures', return_value={}), \
             patch('shutil.disk_usage') as mock_du, \
             patch('os.getloadavg', return_value=(1.0, 2.0, 3.0)), \
             patch('subprocess.run') as mock_run, \
             patch('pathlib.Path.exists', return_value=False):

            mock_vm.return_value.total = 16 * 1024**3
            mock_vm.return_value.available = 12 * 1024**3
            mock_vm.return_value.used = 4 * 1024**3
            mock_vm.return_value.free = 12 * 1024**3

            mock_swap.return_value.total = 4 * 1024**3
            mock_swap.return_value.used = 1 * 1024**3
            mock_swap.return_value.free = 3 * 1024**3

            mock_du.return_value.total = 100 * 1024**3
            mock_du.return_value.used = 50 * 1024**3
            mock_du.return_value.free = 50 * 1024**3
            mock_run.return_value.stdout = ""

            health = linux.health()

        assert health['cpu_usage_pct'] == 25.0
        assert health['uptime_s'] == 1000
        assert health['memory']['total'] == 16 * 1024
        assert health['memory']['available'] == 12 * 1024
        assert health['swap']['total'] == 4 * 1024
        assert health['load_avg'] == (1.0, 2.0, 3.0)

def test_fallback_functions_directly():
    with patch.dict('sys.modules', {'psutil': None}):
        importlib.reload(linux)

        assert linux.HAS_PSUTIL is False

        with patch('os.cpu_count', return_value=8):
            assert linux._fallback_cpu_count() == 8

        with patch('builtins.open', create=True) as mock_open:
            mock_open.return_value.__enter__.return_value.readline.return_value = "1234.56 7890.12\n"
            assert linux._fallback_uptime_s() == 1235

        meminfo_data = [
            "MemTotal:       16384000 kB\n",
            "MemFree:         8192000 kB\n",
            "MemAvailable:   12288000 kB\n",
            "Buffers:         1024000 kB\n",
            "Cached:          2048000 kB\n",
        ]
        with patch('builtins.open', create=True) as mock_open:
            mock_open.return_value.__enter__.return_value.__iter__.return_value = iter(meminfo_data)
            vm = linux._fallback_virtual_memory()
            assert vm.total == 16384000 * 1024
            assert vm.available == 12288000 * 1024
            assert vm.free == 8192000 * 1024

        meminfo_data = [
            "SwapTotal:       4194304 kB\n",
            "SwapFree:        3145728 kB\n"
        ]
        with patch('builtins.open', create=True) as mock_open:
            mock_open.return_value.__enter__.return_value.__iter__.return_value = iter(meminfo_data)
            swap = linux._fallback_swap_memory()
            assert swap.total == 4194304 * 1024
            assert swap.free == 3145728 * 1024
            assert swap.used == (4194304 - 3145728) * 1024
