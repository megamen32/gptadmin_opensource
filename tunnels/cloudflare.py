"""
Cloudflare Quick Tunnel backend.
Uses `cloudflared tunnel --url http://localhost:PORT` which doesn't require account.
"""
from .base import TunnelBackend, TunnelInfo, log
import subprocess
import shutil
import re
import time
import sys


class CloudflareTunnel(TunnelBackend):
    """Cloudflare Quick Tunnel (no account required)."""
    
    @property
    def name(self) -> str:
        return "Cloudflare Quick Tunnel"
    
    def is_available(self) -> bool:
        """Check if cloudflared binary is available."""
        return shutil.which("cloudflared") is not None
    
    def start(self, local_port: int) -> TunnelInfo:
        """Start Cloudflare Quick Tunnel."""
        if not self.is_available():
            raise RuntimeError(
                "cloudflared not found. Install it:\n"
                "  - Linux: curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 -o /usr/local/bin/cloudflared && chmod +x /usr/local/bin/cloudflared\n"
                "  - macOS: brew install cloudflared\n"
                "  - Windows: winget install cloudflare.cloudflared"
            )
        
        url = f"http://localhost:{local_port}"
        log.info(f"Starting Cloudflare Quick Tunnel for {url}")
        
        # Start cloudflared in background
        process = subprocess.Popen(
            ["cloudflared", "tunnel", "--url", url],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            bufsize=1,
        )
        
        # Parse output to find the public URL
        # cloudflared outputs something like:
        # "Your quick Tunnel has been created! Try it out: https://random-words-123.trycloudflare.com"
        public_url = None
        start_time = time.time()
        timeout = 30  # seconds
        
        while time.time() - start_time < timeout:
            if process.poll() is not None:
                # Process exited unexpectedly
                output = process.stdout.read() if process.stdout else ""
                raise RuntimeError(f"cloudflared exited unexpectedly: {output}")
            
            line = process.stdout.readline() if process.stdout else ""
            if line:
                log.debug(f"cloudflared: {line.strip()}")
                # Look for the URL pattern
                match = re.search(r"https://[\w-]+\.trycloudflare\.com", line)
                if match:
                    public_url = match.group(0)
                    log.info(f"Cloudflare tunnel created: {public_url}")
                    break
        
        if not public_url:
            process.terminate()
            raise RuntimeError(f"Failed to get Cloudflare tunnel URL within {timeout}s")
        
        return TunnelInfo(
            public_url=public_url,
            backend="cloudflare",
            process=process
        )
