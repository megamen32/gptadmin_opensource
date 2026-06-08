import pytest
import sys
from gptadmin.cli import gen_hex, gen_subdomain, ensure_https, detect_arch

def test_gen_hex_length():
    assert len(gen_hex(8)) == 16  # 8 bytes = 16 hex chars
    assert len(gen_hex(16)) == 32

def test_gen_hex_is_hex():
    result = gen_hex(16)
    assert all(c in '0123456789abcdef' for c in result)

def test_gen_hex_randomness():
    results = {gen_hex(16) for _ in range(10)}
    assert len(results) == 10

def test_gen_subdomain_format():
    result = gen_subdomain()
    assert isinstance(result, str)
    assert len(result) > 0
    assert all(c.isalnum() or c == '-' for c in result)
    assert result == result.lower()

def test_ensure_https_valid():
    # Should not raise for valid URLs
    ensure_https('https://example.com')
    ensure_https('https://gptadmin.example.com:8080')
    ensure_https('https://example.com/path')

def test_ensure_https_invalid():
    # Should call sys.exit for invalid URLs
    with pytest.raises(SystemExit):
        ensure_https('http://example.com')
    with pytest.raises(SystemExit):
        ensure_https('example.com')
    with pytest.raises(SystemExit):
        ensure_https('')

def test_detect_arch():
    arch = detect_arch()
    # Should return one of the supported architectures
    assert arch in ['amd64', 'arm64', 'arm']
