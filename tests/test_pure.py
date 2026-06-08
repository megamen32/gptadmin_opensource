#!/usr/bin/env python3
"""Unit tests for gptadmin.pure (dependency-free rootd)"""
import os
import sys
import pytest

# Add src to path for imports
sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..', 'src'))

from gptadmin.pure import _truncate, system_info, system_health, run_cmd


def test_truncate_short_string():
    """Test that short strings are not truncated"""
    s = "hello world"
    result = _truncate(s)
    assert result == s
    assert "truncated" not in result


def test_truncate_long_string():
    """Test that long strings are truncated with marker"""
    # Set LOG_MAX to a small value for testing
    import gptadmin.pure as pure_module
    original_max = pure_module.LOG_MAX
    pure_module.LOG_MAX = 10
    
    try:
        s = "a" * 20
        result = _truncate(s)
        # Result should contain the truncation marker
        assert "truncated" in result
        # Result should start with the first LOG_MAX characters
        assert result.startswith("a" * 10)
        # Result should be longer than LOG_MAX due to marker
        assert len(result) > 10
    finally:
        pure_module.LOG_MAX = original_max


def test_system_info_structure():
    """Test that system_info returns expected keys"""
    info = system_info()
    assert isinstance(info, dict)
    assert 'host' in info
    assert 'platform' in info
    assert 'cores' in info
    assert 'mem_mb' in info
    assert 'uptime_s' in info
    
    # cores should be positive integer or None
    assert info['cores'] is None or isinstance(info['cores'], int)
    assert info['cores'] is None or info['cores'] > 0


def test_system_health_structure():
    """Test that system_health returns expected keys"""
    health = system_health()
    assert isinstance(health, dict)
    assert 'host' in health
    assert 'load_avg' in health
    assert 'disk' in health
    
    # disk should have total, used, free
    assert 'total' in health['disk']
    assert 'used' in health['disk']
    assert 'free' in health['disk']
    
    # load_avg should be a tuple or list (os.getloadavg returns tuple)
    assert isinstance(health['load_avg'], (list, tuple))
    # If it's a tuple/list, it should have 3 elements (1, 5, 15 min averages)
    if health['load_avg']:
        assert len(health['load_avg']) == 3


def test_run_cmd_echo():
    """Test run_cmd with simple echo command"""
    result = run_cmd("echo 'test output'", timeout=5)
    assert isinstance(result, dict)
    assert 'returncode' in result
    assert 'stdout' in result
    assert 'stderr' in result
    assert result['returncode'] == 0
    assert 'test output' in result['stdout']


def test_run_cmd_failure():
    """Test run_cmd with failing command"""
    result = run_cmd("exit 42", timeout=5)
    assert isinstance(result, dict)
    assert result['returncode'] == 42


def test_run_cmd_timeout():
    """Test run_cmd with timeout"""
    result = run_cmd("sleep 10", timeout=1)
    assert isinstance(result, dict)
    assert 'error' in result
    assert 'timeout' in result['error'].lower()


def test_run_cmd_with_cwd():
    """Test run_cmd with custom working directory"""
    result = run_cmd("pwd", cwd="/tmp", timeout=5)
    assert result['returncode'] == 0
    # On some systems /tmp might be a symlink, so check if it contains 'tmp'
    assert 'tmp' in result['stdout'].lower()


def test_run_cmd_with_env():
    """Test run_cmd with custom environment variables"""
    result = run_cmd("echo $TEST_VAR", env={"TEST_VAR": "hello123"}, timeout=5)
    assert result['returncode'] == 0
    assert 'hello123' in result['stdout']


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
