"""
Ngrok tunnel backend.
Uses `ngrok http PORT` which can work without auth token (with limitations).
"""
from .base import TunnelBackend, TunnelInfo, log
import subprocess
import shutil
import time
import json
import requests


class NgrokTunnel(TunnelBackend):
    """Ngrok tunnel (can work without auth token)."""
    
    @property
    def name(self) -> str:
        return "ngrok"
    
    def is_available(self) -> bool:
        """Check if ngrok binary is available."""
        return shutil.which("ngrok") is not None
    
    def start(self, local_port: int) -> TunnelInfo:
        """Start ngrok tunnel."""
        if not self.is_available():
            raise RuntimeError(
                "ngrok not found. Install it:\n"
                "  - Linux: curl -s https://ngrok-agent.s3.amazonaws.com/ngrok.asc | sudo tee /etc/apt/trusted.gpg.d/ngrok.asc && echo 'deb https://ngrok-agent.s3.amazonaws.com buster main' | sudo tee /etc/apt/sources.list.d/ngrok.list && sudo apt update && sudo apt install ngrok\n"
                "  - macOS: brew install ngrok\n"
                "  - Windows: winget install ngrok\n\n"
                "Optional: Sign up at https://ngrok.com for a free auth token to remove limitations."
            )
        
        log.info(f"Starting ngrok tunnel for port {local_port}")
        
        # Start ngrok in background
        process = subprocess.Popen(
            ["ngrok", "http", str(local_port), "--log=stdout"],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            bufsize=1,
        )
        
        # Wait for ngrok to start and get public URL via API
        public_url = None
        start_time = time.time()
        timeout = 30  # seconds
        
        while time.time() - start_time < timeout:
            if process.poll() is not None:
                # Process exited unexpectedly
                output = process.stdout.read() if process.stdout else ""
                raise RuntimeError(f"ngrok exited unexpectedly: {output}")
            
            try:
                # ngrok exposes API at localhost:4040
                resp = requests.get("http://localhost:4040/api/tunnels", timeout=2)
                if resp.status_code == 200:
                    data = resp.json()
                    tunnels = data.get("tunnels", [])
                    if tunnels:
                        # Get the HTTPS tunnel URL
                        for tunnel in tunnels:
                            if tunnel.get("proto") == "https":
                                public_url = tunnel.get("public_url")
                                log.info(f"ngrok tunnel created: {public_url}")
                                break
                        if public_url:
                            break
            except requests.RequestException:
                # API not ready yet
                pass
            
            time.sleep(0.5)
        
        if not public_url:
            process.terminate()
            raise RuntimeError(f"Failed to get ngrok tunnel URL within {timeout}s")
        
        return TunnelInfo(
            public_url=public_url,
            backend="ngrok",
            process=process
        )
