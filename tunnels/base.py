"""
Base class for tunnel backends.
"""
from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import Optional
import subprocess
import logging

log = logging.getLogger(__name__)


@dataclass
class TunnelInfo:
    """Information about a running tunnel."""
    public_url: str
    backend: str
    process: Optional[subprocess.Popen] = None
    
    def stop(self):
        """Stop the tunnel process."""
        if self.process and self.process.poll() is None:
            log.info(f"Stopping {self.backend} tunnel...")
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                log.warning(f"Tunnel process did not terminate gracefully, killing...")
                self.process.kill()
                self.process.wait()


class TunnelBackend(ABC):
    """Abstract base class for tunnel backends."""
    
    @abstractmethod
    def start(self, local_port: int) -> TunnelInfo:
        """
        Start the tunnel and return info with public URL.
        
        Args:
            local_port: Local port to expose (e.g., hub port 9001)
            
        Returns:
            TunnelInfo with public_url and process handle
        """
        pass
    
    @abstractmethod
    def is_available(self) -> bool:
        """Check if this tunnel backend is available (binary installed, etc.)."""
        pass
    
    @property
    @abstractmethod
    def name(self) -> str:
        """Human-readable name of the tunnel backend."""
        pass
