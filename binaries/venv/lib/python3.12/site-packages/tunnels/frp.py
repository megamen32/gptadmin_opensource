"""
FRP (frpc) tunnel backend.
Requires a frps server running somewhere (self-hosted).
"""
from .base import TunnelBackend, TunnelInfo, log
import subprocess
import shutil
import os
from pathlib import Path


class FrpTunnel(TunnelBackend):
    """FRP tunnel (requires self-hosted frps server)."""
    
    def __init__(self, server_addr: str, server_port: int, token: str, 
                 subdomain: str, domain: str, config_path: Path = None):
        """
        Initialize FRP tunnel.
        
        Args:
            server_addr: FRP server address (e.g., "frp.example.com")
            server_port: FRP server port (e.g., 7000)
            token: FRP auth token
            subdomain: Subdomain for this tunnel (e.g., "myhub")
            domain: Base domain (e.g., "example.com")
            config_path: Optional custom config path
        """
        self.server_addr = server_addr
        self.server_port = server_port
        self.token = token
        self.subdomain = subdomain
        self.domain = domain
        self.config_path = config_path
        
    @property
    def name(self) -> str:
        return "FRP (self-hosted)"
    
    def is_available(self) -> bool:
        """Check if frpc binary is available."""
        return shutil.which("frpc") is not None
    
    def _write_config(self, local_port: int) -> Path:
        """Write frpc config file."""
        if self.config_path:
            config_path = self.config_path
        else:
            # Use XDG config dir
            config_dir = Path(os.getenv("XDG_CONFIG_HOME", Path.home() / ".config"))
            config_dir = config_dir / "gptadmin" / "tunnels"
            config_dir.mkdir(parents=True, exist_ok=True)
            config_path = config_dir / "frpc.toml"
        
        content = f"""serverAddr = "{self.server_addr}"
serverPort = {self.server_port}

[auth]
token = "{self.token}"

[transport.tls]
enable = true
serverName = "{self.domain}"

[[proxies]]
name = "gptadmin-hub"
type = "http"
localPort = {local_port}
subdomain = "{self.subdomain}"
"""
        config_path.write_text(content)
        os.chmod(config_path, 0o640)
        return config_path
    
    def start(self, local_port: int) -> TunnelInfo:
        """Start FRP tunnel."""
        if not self.is_available():
            raise RuntimeError(
                "frpc not found. Install it:\n"
                "  - Download from: https://github.com/fatedier/frp/releases\n"
                "  - Or use: gptadmin setup (which installs frpc automatically)"
            )
        
        log.info(f"Starting FRP tunnel for port {local_port}")
        
        # Write config
        config_path = self._write_config(local_port)
        
        # Start frpc in background
        process = subprocess.Popen(
            ["frpc", "-c", str(config_path)],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            bufsize=1,
        )
        
        # Wait a bit for tunnel to establish
        import time
        time.sleep(2)
        
        if process.poll() is not None:
            output = process.stdout.read() if process.stdout else ""
            raise RuntimeError(f"frpc exited unexpectedly: {output}")
        
        # Construct public URL
        public_url = f"https://{self.subdomain}.{self.domain}"
        log.info(f"FRP tunnel created: {public_url}")
        
        return TunnelInfo(
            public_url=public_url,
            backend="frp",
            process=process
        )
