use anyhow::{anyhow, Context, Result};
use http::Uri;
use serde::{Deserialize, Serialize};
use std::{
    collections::BTreeMap,
    fs,
    io::Write,
    path::{Path, PathBuf},
    time::{SystemTime, UNIX_EPOCH},
};

const DEFAULT_STALE_GRACE_MS: u64 = 5 * 60 * 1000;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CacheFreshness {
    Fresh,
    StaleButUsable,
    Expired,
    Empty,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TopologyNode {
    pub host_id: String,
    pub local_url: String,
    pub routable_url: String,
    #[serde(default)]
    pub capabilities: Vec<String>,
    #[serde(default)]
    pub roots: Vec<String>,
    pub generation: u64,
    pub fetched_at_ms: u64,
    pub expires_at_ms: u64,
    #[serde(default)]
    pub last_refresh_error: Option<String>,
}

impl TopologyNode {
    pub fn freshness_at(&self, now_ms: u64) -> CacheFreshness {
        freshness_from_bounds(
            Some(self.fetched_at_ms),
            Some(self.expires_at_ms),
            self.last_refresh_error.as_deref(),
            now_ms,
        )
    }

    pub fn validate(&self) -> Result<()> {
        validate_host_id(&self.host_id)?;
        validate_url(&self.local_url, "local_url")?;
        validate_url(&self.routable_url, "routable_url")?;
        validate_nonempty_items(&self.capabilities, "capabilities")?;
        validate_nonempty_items(&self.roots, "roots")?;
        if self.generation == 0 {
            return Err(anyhow!("generation must be greater than zero"));
        }
        if self.fetched_at_ms == 0 {
            return Err(anyhow!("fetched_at_ms must be greater than zero"));
        }
        if self.expires_at_ms == 0 {
            return Err(anyhow!("expires_at_ms must be greater than zero"));
        }
        if self.expires_at_ms < self.fetched_at_ms {
            return Err(anyhow!(
                "expires_at_ms must be greater than or equal to fetched_at_ms"
            ));
        }
        if let Some(error) = &self.last_refresh_error {
            if error.trim().is_empty() {
                return Err(anyhow!("last_refresh_error cannot be blank"));
            }
        }
        Ok(())
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TopologySnapshot {
    pub local_host_id: String,
    #[serde(default)]
    pub generation: u64,
    #[serde(default)]
    pub fetched_at_ms: Option<u64>,
    #[serde(default)]
    pub expires_at_ms: Option<u64>,
    #[serde(default)]
    pub last_refresh_error: Option<String>,
    #[serde(default)]
    pub peers: Vec<TopologyNode>,
}

impl TopologySnapshot {
    pub fn empty(local_host_id: impl Into<String>) -> Self {
        Self {
            local_host_id: local_host_id.into(),
            generation: 0,
            fetched_at_ms: None,
            expires_at_ms: None,
            last_refresh_error: None,
            peers: Vec::new(),
        }
    }

    pub fn validate(&self) -> Result<()> {
        validate_host_id(&self.local_host_id)?;
        if let Some(error) = &self.last_refresh_error {
            if error.trim().is_empty() {
                return Err(anyhow!("last_refresh_error cannot be blank"));
            }
        }
        if self.peers.is_empty() {
            if self.generation == 0 && self.fetched_at_ms.is_none() && self.expires_at_ms.is_none()
            {
                return Ok(());
            }
            if self.generation == 0 {
                return Err(anyhow!("empty snapshots with timestamps need a generation"));
            }
            let fetched_at_ms = self
                .fetched_at_ms
                .ok_or_else(|| anyhow!("fetched_at_ms is required when peers are present"))?;
            let expires_at_ms = self
                .expires_at_ms
                .ok_or_else(|| anyhow!("expires_at_ms is required when peers are present"))?;
            if fetched_at_ms == 0 || expires_at_ms == 0 {
                return Err(anyhow!(
                    "empty snapshot timestamps must be greater than zero"
                ));
            }
            if expires_at_ms < fetched_at_ms {
                return Err(anyhow!(
                    "expires_at_ms must be greater than or equal to fetched_at_ms"
                ));
            }
            return Ok(());
        }

        if self.generation == 0 {
            return Err(anyhow!("generation must be greater than zero"));
        }
        let fetched_at_ms = self
            .fetched_at_ms
            .ok_or_else(|| anyhow!("fetched_at_ms is required when peers are present"))?;
        let expires_at_ms = self
            .expires_at_ms
            .ok_or_else(|| anyhow!("expires_at_ms is required when peers are present"))?;
        if fetched_at_ms == 0 {
            return Err(anyhow!("fetched_at_ms must be greater than zero"));
        }
        if expires_at_ms == 0 {
            return Err(anyhow!("expires_at_ms must be greater than zero"));
        }
        if expires_at_ms < fetched_at_ms {
            return Err(anyhow!(
                "expires_at_ms must be greater than or equal to fetched_at_ms"
            ));
        }
        for peer in &self.peers {
            peer.validate()?;
        }
        let mut seen = BTreeMap::new();
        for peer in &self.peers {
            if seen.insert(peer.host_id.clone(), ()).is_some() {
                return Err(anyhow!("duplicate host_id {} in snapshot", peer.host_id));
            }
        }
        Ok(())
    }

    pub fn freshness_at(&self, now_ms: u64) -> CacheFreshness {
        freshness_from_bounds(
            self.fetched_at_ms,
            self.expires_at_ms,
            self.last_refresh_error.as_deref(),
            now_ms,
        )
    }

    pub fn load(path: impl AsRef<Path>) -> Result<Self> {
        let path = path.as_ref();
        let bytes = fs::read(path).with_context(|| format!("read {}", path.display()))?;
        let snapshot: Self =
            serde_json::from_slice(&bytes).with_context(|| format!("parse {}", path.display()))?;
        snapshot.validate()?;
        Ok(snapshot)
    }

    pub fn save_atomic(&self, path: impl AsRef<Path>) -> Result<()> {
        self.validate()?;
        let path = path.as_ref();
        let parent = path
            .parent()
            .ok_or_else(|| anyhow!("{} has no parent directory", path.display()))?;
        fs::create_dir_all(parent)
            .with_context(|| format!("create directory {}", parent.display()))?;
        let temp_path = atomic_temp_path(path)?;
        let bytes = serde_json::to_vec_pretty(self)?;
        {
            let mut file = fs::OpenOptions::new()
                .create_new(true)
                .write(true)
                .open(&temp_path)
                .with_context(|| format!("open {}", temp_path.display()))?;
            file.write_all(&bytes)
                .with_context(|| format!("write {}", temp_path.display()))?;
            file.sync_all()
                .with_context(|| format!("sync {}", temp_path.display()))?;
        }
        fs::rename(&temp_path, path)
            .with_context(|| format!("replace {} with {}", path.display(), temp_path.display()))?;
        Ok(())
    }

    pub fn peer(&self, host_id: &str) -> Option<&TopologyNode> {
        self.peers.iter().find(|peer| peer.host_id == host_id)
    }

    pub fn merge_fresh_provider_result(&self, result: ProviderSnapshot) -> Result<Self> {
        result.validate()?;

        let mut merged = BTreeMap::new();
        for peer in result.peers {
            merged.insert(peer.host_id.clone(), peer);
        }

        let mut peers = merged.into_values().collect::<Vec<_>>();
        peers.sort_by(|a, b| {
            a.host_id
                .cmp(&b.host_id)
                .then(a.local_url.cmp(&b.local_url))
                .then(a.routable_url.cmp(&b.routable_url))
        });

        Ok(Self {
            local_host_id: result.local_host_id,
            generation: self.generation.max(result.generation),
            fetched_at_ms: Some(result.fetched_at_ms),
            expires_at_ms: Some(result.fetched_at_ms.saturating_add(result.ttl_ms)),
            last_refresh_error: None,
            peers,
        })
    }

    pub fn with_refresh_error(&self, error: impl Into<String>) -> Self {
        let error = error.into();
        let mut next = self.clone();
        next.last_refresh_error = Some(error.clone());
        for peer in &mut next.peers {
            if peer.last_refresh_error.is_none() {
                peer.last_refresh_error = Some(error.clone());
            }
        }
        next
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProviderSnapshot {
    pub local_host_id: String,
    pub generation: u64,
    pub fetched_at_ms: u64,
    pub ttl_ms: u64,
    pub peers: Vec<TopologyNode>,
}

impl ProviderSnapshot {
    pub fn validate(&self) -> Result<()> {
        validate_host_id(&self.local_host_id)?;
        if self.generation == 0 {
            return Err(anyhow!("generation must be greater than zero"));
        }
        if self.fetched_at_ms == 0 {
            return Err(anyhow!("fetched_at_ms must be greater than zero"));
        }
        if self.ttl_ms == 0 {
            return Err(anyhow!("ttl_ms must be greater than zero"));
        }
        for peer in &self.peers {
            peer.validate()?;
        }
        let mut seen = BTreeMap::new();
        for peer in &self.peers {
            if seen.insert(peer.host_id.clone(), ()).is_some() {
                return Err(anyhow!(
                    "duplicate host_id {} in provider result",
                    peer.host_id
                ));
            }
        }
        Ok(())
    }
}

fn validate_host_id(host_id: &str) -> Result<()> {
    if host_id.trim().is_empty() {
        return Err(anyhow!("host_id cannot be blank"));
    }
    Ok(())
}

fn validate_url(value: &str, field: &str) -> Result<()> {
    let uri: Uri = value
        .parse()
        .with_context(|| format!("parse {field} as absolute URI"))?;
    if uri.scheme_str().is_none() || uri.authority().is_none() {
        return Err(anyhow!("{field} must be an absolute URI"));
    }
    Ok(())
}

fn validate_nonempty_items(values: &[String], field: &str) -> Result<()> {
    for value in values {
        if value.trim().is_empty() {
            return Err(anyhow!("{field} cannot contain blank values"));
        }
    }
    Ok(())
}

fn freshness_from_bounds(
    fetched_at_ms: Option<u64>,
    expires_at_ms: Option<u64>,
    last_refresh_error: Option<&str>,
    now_ms: u64,
) -> CacheFreshness {
    match (fetched_at_ms, expires_at_ms) {
        (None, None) => CacheFreshness::Empty,
        (Some(_), Some(expires_at_ms)) => {
            if now_ms <= expires_at_ms {
                if last_refresh_error.is_some() {
                    CacheFreshness::StaleButUsable
                } else {
                    CacheFreshness::Fresh
                }
            } else if now_ms <= expires_at_ms.saturating_add(DEFAULT_STALE_GRACE_MS) {
                CacheFreshness::StaleButUsable
            } else {
                CacheFreshness::Expired
            }
        }
        _ => CacheFreshness::Empty,
    }
}

fn atomic_temp_path(path: &Path) -> Result<PathBuf> {
    let parent = path
        .parent()
        .ok_or_else(|| anyhow!("{} has no parent directory", path.display()))?;
    let file_name = path
        .file_name()
        .ok_or_else(|| anyhow!("{} has no file name", path.display()))?
        .to_string_lossy();
    let unique = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .context("system clock before unix epoch")?
        .as_nanos();
    Ok(parent.join(format!(".{file_name}.{unique}.tmp")))
}
