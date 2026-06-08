"""
Tests for tunnel backends.
"""
import pytest
from unittest.mock import Mock, patch, MagicMock
from pathlib import Path
import subprocess

from gptadmin.tunnels import CloudflareTunnel, NgrokTunnel, FrpTunnel
from gptadmin.tunnels.base import TunnelInfo


class TestCloudflareTunnel:
    """Test Cloudflare Quick Tunnel."""
    
    def test_name(self):
        tunnel = CloudflareTunnel()
        assert tunnel.name == "Cloudflare Quick Tunnel"
    
    def test_is_available_with_cloudflared(self):
        tunnel = CloudflareTunnel()
        with patch("shutil.which", return_value="/usr/bin/cloudflared"):
            assert tunnel.is_available() is True
    
    def test_is_available_without_cloudflared(self):
        tunnel = CloudflareTunnel()
        with patch("shutil.which", return_value=None):
            assert tunnel.is_available() is False
    
    def test_start_raises_if_not_available(self):
        tunnel = CloudflareTunnel()
        with patch("shutil.which", return_value=None):
            with pytest.raises(RuntimeError, match="cloudflared not found"):
                tunnel.start(9001)
    
    def test_start_parses_url_from_output(self):
        tunnel = CloudflareTunnel()
        
        # Mock subprocess.Popen
        mock_process = Mock()
        mock_process.poll.return_value = None  # Process still running
        mock_process.stdout = MagicMock()
        mock_process.stdout.readline.side_effect = [
            "Some log line\n",
            "Your quick Tunnel has been created! Try it out: https://test-tunnel-123.trycloudflare.com\n",
        ]
        
        with patch("shutil.which", return_value="/usr/bin/cloudflared"):
            with patch("subprocess.Popen", return_value=mock_process):
                info = tunnel.start(9001)
                
                assert info.public_url == "https://test-tunnel-123.trycloudflare.com"
                assert info.backend == "cloudflare"
                assert info.process == mock_process


class TestNgrokTunnel:
    """Test ngrok tunnel."""
    
    def test_name(self):
        tunnel = NgrokTunnel()
        assert tunnel.name == "ngrok"
    
    def test_is_available_with_ngrok(self):
        tunnel = NgrokTunnel()
        with patch("shutil.which", return_value="/usr/bin/ngrok"):
            assert tunnel.is_available() is True
    
    def test_is_available_without_ngrok(self):
        tunnel = NgrokTunnel()
        with patch("shutil.which", return_value=None):
            assert tunnel.is_available() is False
    
    def test_start_raises_if_not_available(self):
        tunnel = NgrokTunnel()
        with patch("shutil.which", return_value=None):
            with pytest.raises(RuntimeError, match="ngrok not found"):
                tunnel.start(9001)
    
    def test_start_gets_url_from_api(self):
        tunnel = NgrokTunnel()
        
        # Mock subprocess.Popen
        mock_process = Mock()
        mock_process.poll.return_value = None
        
        # Mock requests.get to return ngrok API response
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "tunnels": [
                {
                    "proto": "https",
                    "public_url": "https://abc123.ngrok.io"
                }
            ]
        }
        
        with patch("shutil.which", return_value="/usr/bin/ngrok"):
            with patch("subprocess.Popen", return_value=mock_process):
                with patch("requests.get", return_value=mock_response):
                    info = tunnel.start(9001)
                    
                    assert info.public_url == "https://abc123.ngrok.io"
                    assert info.backend == "ngrok"
                    assert info.process == mock_process


class TestFrpTunnel:
    """Test FRP tunnel."""
    
    def test_name(self):
        tunnel = FrpTunnel(
            server_addr="frp.example.com",
            server_port=7000,
            token="secret",
            subdomain="myhub",
            domain="example.com"
        )
        assert tunnel.name == "FRP (self-hosted)"
    
    def test_is_available_with_frpc(self):
        tunnel = FrpTunnel(
            server_addr="frp.example.com",
            server_port=7000,
            token="secret",
            subdomain="myhub",
            domain="example.com"
        )
        with patch("shutil.which", return_value="/usr/bin/frpc"):
            assert tunnel.is_available() is True
    
    def test_is_available_without_frpc(self):
        tunnel = FrpTunnel(
            server_addr="frp.example.com",
            server_port=7000,
            token="secret",
            subdomain="myhub",
            domain="example.com"
        )
        with patch("shutil.which", return_value=None):
            assert tunnel.is_available() is False
    
    def test_write_config(self, tmp_path):
        config_path = tmp_path / "frpc.toml"
        tunnel = FrpTunnel(
            server_addr="frp.example.com",
            server_port=7000,
            token="secret123",
            subdomain="myhub",
            domain="example.com",
            config_path=config_path
        )
        
        result_path = tunnel._write_config(9001)
        
        assert result_path == config_path
        content = config_path.read_text()
        assert 'serverAddr = "frp.example.com"' in content
        assert 'serverPort = 7000' in content
        assert 'token = "secret123"' in content
        assert 'subdomain = "myhub"' in content
        assert 'localPort = 9001' in content
    
    def test_start_constructs_public_url(self, tmp_path):
        config_path = tmp_path / "frpc.toml"
        tunnel = FrpTunnel(
            server_addr="frp.example.com",
            server_port=7000,
            token="secret",
            subdomain="myhub",
            domain="example.com",
            config_path=config_path
        )
        
        # Mock subprocess.Popen
        mock_process = Mock()
        mock_process.poll.return_value = None
        
        with patch("shutil.which", return_value="/usr/bin/frpc"):
            with patch("subprocess.Popen", return_value=mock_process):
                with patch("time.sleep"):  # Skip the 2 second wait
                    info = tunnel.start(9001)
                    
                    assert info.public_url == "https://myhub.example.com"
                    assert info.backend == "frp"
                    assert info.process == mock_process


class TestTunnelInfo:
    """Test TunnelInfo dataclass."""
    
    def test_stop_terminates_process(self):
        mock_process = Mock()
        mock_process.poll.return_value = None  # Still running
        
        info = TunnelInfo(
            public_url="https://test.example.com",
            backend="test",
            process=mock_process
        )
        
        info.stop()
        
        mock_process.terminate.assert_called_once()
        mock_process.wait.assert_called_once_with(timeout=5)
    
    def test_stop_kills_if_terminate_timeout(self):
        mock_process = Mock()
        mock_process.poll.return_value = None
        # First wait() raises TimeoutExpired, second wait() succeeds
        mock_process.wait.side_effect = [subprocess.TimeoutExpired("cmd", 5), None]
        
        info = TunnelInfo(
            public_url="https://test.example.com",
            backend="test",
            process=mock_process
        )
        
        info.stop()
        
        mock_process.terminate.assert_called_once()
        mock_process.kill.assert_called_once()
    
    def test_stop_noop_if_no_process(self):
        info = TunnelInfo(
            public_url="https://test.example.com",
            backend="test",
            process=None
        )
        
        # Should not raise
        info.stop()
    
    def test_stop_noop_if_already_exited(self):
        mock_process = Mock()
        mock_process.poll.return_value = 0  # Already exited
        
        info = TunnelInfo(
            public_url="https://test.example.com",
            backend="test",
            process=mock_process
        )
        
        info.stop()
        
        # Should not call terminate
        mock_process.terminate.assert_not_called()
