use serde::{Deserialize, Serialize};

use crate::topology_cache::{CacheFreshness, TopologySnapshot};

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct PeerConfig {
    pub host_id: String,
    pub local_url: String,
    pub routable_url: String,
}

#[derive(Debug, Clone)]
pub struct Topology {
    pub local_host_id: String,
    pub peers: Vec<PeerConfig>,
    pub cache_freshness: Option<CacheFreshness>,
    pub generation: u64,
    pub last_refresh_error: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TopologyStatus {
    pub freshness: Option<CacheFreshness>,
    pub generation: u64,
    pub last_refresh_error: Option<String>,
}

impl Topology {
    pub fn new(local_host_id: impl Into<String>, peers: Vec<PeerConfig>) -> Self {
        Self {
            local_host_id: local_host_id.into(),
            peers,
            cache_freshness: None,
            generation: 0,
            last_refresh_error: None,
        }
    }

    pub fn from_snapshot(snapshot: TopologySnapshot, now_ms: u64) -> anyhow::Result<Self> {
        snapshot.validate()?;
        let freshness = snapshot.freshness_at(now_ms);
        let peers = snapshot
            .peers
            .into_iter()
            .map(|node| PeerConfig {
                host_id: node.host_id,
                local_url: node.local_url,
                routable_url: node.routable_url,
            })
            .collect();
        Ok(Self {
            local_host_id: snapshot.local_host_id,
            peers,
            cache_freshness: Some(freshness),
            generation: snapshot.generation,
            last_refresh_error: snapshot.last_refresh_error,
        })
    }

    pub fn with_cache_error(mut self, error: impl Into<String>) -> Self {
        self.last_refresh_error = Some(error.into());
        self
    }

    pub fn status(&self) -> TopologyStatus {
        TopologyStatus {
            freshness: self.cache_freshness,
            generation: self.generation,
            last_refresh_error: self.last_refresh_error.clone(),
        }
    }

    pub fn peer(&self, host_id: &str) -> Option<&PeerConfig> {
        self.peers.iter().find(|peer| peer.host_id == host_id)
    }

    pub fn known_host_ids(&self) -> impl Iterator<Item = String> + '_ {
        std::iter::once(self.local_host_id.clone())
            .chain(self.peers.iter().map(|peer| peer.host_id.clone()))
    }
}
