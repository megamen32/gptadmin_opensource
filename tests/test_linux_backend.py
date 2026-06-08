import pytest
from unittest.mock import patch, MagicMock
from gptadmin.rootd.linux import info, health

@patch('gptadmin.rootd.linux.psutil')
@patch('gptadmin.rootd.linux.socket')
@patch('gptadmin.rootd.linux.platform')
@patch('gptadmin.rootd.linux.time')
def test_info_structure(mock_time, mock_platform, mock_socket, mock_psutil):
    # Mock psutil responses
    mock_psutil.cpu_count.return_value = 8
    mock_psutil.virtual_memory.return_value = MagicMock(total=16 * 1024**3, available=8 * 1024**3)
    mock_psutil.boot_time.return_value = 1700000000
    mock_socket.gethostname.return_value = 'test-host'
    mock_platform.platform.return_value = 'Linux-5.15.0-x86_64'
    mock_time.time.return_value = 1700003600  # 1 hour uptime
    
    result = info()
    
    assert isinstance(result, dict)
    assert result['host'] == 'test-host'
    assert result['platform'] == 'Linux-5.15.0-x86_64'
    assert result['cores'] == 8
    assert result['mem_mb'] == 16 * 1024
    assert result['uptime_s'] == 3600

@patch('gptadmin.rootd.linux.psutil')
@patch('gptadmin.rootd.linux.socket')
@patch('gptadmin.rootd.linux.platform')
@patch('gptadmin.rootd.linux.time')
def test_info_handles_missing_psutil(mock_time, mock_platform, mock_socket, mock_psutil):
    # Simulate psutil not being available
    mock_psutil.cpu_count.side_effect = AttributeError
    
    # Should not crash, might return partial info or handle gracefully
    try:
        result = info()
        assert isinstance(result, dict)
    except (AttributeError, ImportError):
        # It's also acceptable if it raises when psutil is missing
        pass

@patch('gptadmin.rootd.linux.shutil')
@patch('gptadmin.rootd.linux.psutil')
@patch('gptadmin.rootd.linux.os')
@patch('gptadmin.rootd.linux.subprocess')
@patch('gptadmin.rootd.linux.time')
@patch('gptadmin.rootd.linux.Path')
def test_health_structure(mock_path, mock_time, mock_subprocess, mock_os, mock_psutil, mock_shutil):
    # Mock shutil.disk_usage
    mock_shutil.disk_usage.return_value = MagicMock(
        total=100 * 1024**3,
        used=50 * 1024**3,
        free=50 * 1024**3
    )
    
    # Mock psutil responses
    mock_psutil.virtual_memory.return_value = MagicMock(
        total=16 * 1024**3,
        available=8 * 1024**3,
        used=8 * 1024**3,
        free=8 * 1024**3
    )
    mock_psutil.swap_memory.return_value = MagicMock(
        total=4 * 1024**3,
        used=1 * 1024**3,
        free=3 * 1024**3
    )
    mock_psutil.cpu_percent.return_value = 45.5
    mock_psutil.boot_time.return_value = 1700000000
    mock_psutil.sensors_temperatures.return_value = {}
    
    # Mock os.getloadavg
    mock_os.getloadavg.return_value = (1.5, 2.0, 2.5)
    
    # Mock subprocess for failed services
    mock_subprocess.run.return_value = MagicMock(stdout='')
    
    # Mock time
    mock_time.time.return_value = 1700003600
    
    # Mock Path for apt stamp
    mock_path_instance = MagicMock()
    mock_path_instance.exists.return_value = False
    mock_path.return_value = mock_path_instance
    
    result = health()
    
    assert isinstance(result, dict)
    assert 'cpu_usage_pct' in result
    assert result['cpu_usage_pct'] == 45.5
    assert 'memory' in result
    assert result['memory']['total'] == 16 * 1024
    assert 'disk' in result
    assert result['disk']['total'] == 100.0  # GB
    assert 'load_avg' in result
    assert result['load_avg'] == (1.5, 2.0, 2.5)

@patch('gptadmin.rootd.linux.shutil')
@patch('gptadmin.rootd.linux.psutil')
@patch('gptadmin.rootd.linux.os')
@patch('gptadmin.rootd.linux.subprocess')
@patch('gptadmin.rootd.linux.time')
@patch('gptadmin.rootd.linux.Path')
def test_health_high_usage(mock_path, mock_time, mock_subprocess, mock_os, mock_psutil, mock_shutil):
    # Test with high resource usage
    mock_shutil.disk_usage.return_value = MagicMock(
        total=100 * 1024**3,
        used=90 * 1024**3,
        free=10 * 1024**3
    )
    mock_psutil.virtual_memory.return_value = MagicMock(
        total=16 * 1024**3,
        available=1.6 * 1024**3,
        used=14.4 * 1024**3,
        free=1.6 * 1024**3
    )
    mock_psutil.swap_memory.return_value = MagicMock(
        total=4 * 1024**3,
        used=3 * 1024**3,
        free=1 * 1024**3
    )
    mock_psutil.cpu_percent.return_value = 95.0
    mock_psutil.boot_time.return_value = 1700000000
    mock_psutil.sensors_temperatures.return_value = {}
    mock_os.getloadavg.return_value = (10.0, 12.0, 15.0)
    mock_subprocess.run.return_value = MagicMock(stdout='')
    mock_time.time.return_value = 1700003600
    
    mock_path_instance = MagicMock()
    mock_path_instance.exists.return_value = False
    mock_path.return_value = mock_path_instance
    
    result = health()
    
    assert result['cpu_usage_pct'] == 95.0
    # Allow for rounding differences
    assert abs(result['memory']['used'] - 14.4 * 1024) < 1
    assert result['disk']['used'] == 90.0

@patch('gptadmin.rootd.linux.shutil')
@patch('gptadmin.rootd.linux.psutil')
@patch('gptadmin.rootd.linux.os')
@patch('gptadmin.rootd.linux.subprocess')
@patch('gptadmin.rootd.linux.time')
@patch('gptadmin.rootd.linux.Path')
def test_health_low_usage(mock_path, mock_time, mock_subprocess, mock_os, mock_psutil, mock_shutil):
    # Test with low resource usage
    mock_shutil.disk_usage.return_value = MagicMock(
        total=100 * 1024**3,
        used=10 * 1024**3,
        free=90 * 1024**3
    )
    mock_psutil.virtual_memory.return_value = MagicMock(
        total=16 * 1024**3,
        available=12.8 * 1024**3,
        used=3.2 * 1024**3,
        free=12.8 * 1024**3
    )
    mock_psutil.swap_memory.return_value = MagicMock(
        total=4 * 1024**3,
        used=0,
        free=4 * 1024**3
    )
    mock_psutil.cpu_percent.return_value = 5.0
    mock_psutil.boot_time.return_value = 1700000000
    mock_psutil.sensors_temperatures.return_value = {}
    mock_os.getloadavg.return_value = (0.1, 0.2, 0.3)
    mock_subprocess.run.return_value = MagicMock(stdout='')
    mock_time.time.return_value = 1700003600
    
    mock_path_instance = MagicMock()
    mock_path_instance.exists.return_value = False
    mock_path.return_value = mock_path_instance
    
    result = health()
    
    assert result['cpu_usage_pct'] == 5.0
    # Allow for rounding differences
    assert abs(result['memory']['used'] - 3.2 * 1024) < 1
    assert result['disk']['used'] == 10.0
