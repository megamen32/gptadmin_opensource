use crate::topology::PeerConfig;
use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use std::{
    collections::BTreeMap,
    fs,
    net::SocketAddr,
    path::{Path, PathBuf},
    time::Duration,
};

pub fn default_exclude_globs() -> Vec<String> {
    [
        "**/.git/**",
        "**/.svn/**",
        "**/.hg/**",
        "**/node_modules/**",
        "**/.pnpm-store/**",
        "**/bower_components/**",
        "**/venv/**",
        "**/.venv/**",
        "**/__pycache__/**",
        "**/.tox/**",
        "**/.nox/**",
        "**/.pytest_cache/**",
        "**/.mypy_cache/**",
        "**/.ruff_cache/**",
        "**/target/**",
        "**/dist/**",
        "**/build/**",
        "**/out/**",
        "**/.next/**",
        "**/.nuxt/**",
        "**/coverage/**",
        "**/.cache/**",
        "**/.cargo/registry/**",
        "**/.cargo/git/**",
        "**/.rustup/**",
        "**/go/pkg/mod/**",
        "**/.local/share/Trash/**",
        "**/diag-live/**",
        "**/rollback-*/**",
        "**/rollback-*",
        "**/.ssh/**",
        "**/.gnupg/**",
        "**/.aws/credentials",
        "**/.netrc",
        "**/id_rsa",
        "**/id_ed25519",
        "**/*.pem",
        "**/*.key",
        "**/shadow",
        "**/gshadow",
    ]
    .into_iter()
    .map(str::to_string)
    .collect()
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct LimitsConfig {
    #[serde(default = "default_max_results")]
    pub max_results: usize,
    #[serde(default = "default_context_lines")]
    pub context_lines: usize,
    #[serde(default = "default_max_response_bytes")]
    pub max_response_bytes: usize,
    #[serde(default = "default_peer_timeout_ms")]
    pub peer_timeout_ms: u64,
    #[serde(default = "default_overall_timeout_ms")]
    pub overall_timeout_ms: u64,
    #[serde(default = "default_max_file_bytes")]
    pub max_file_bytes: u64,
}

fn default_max_results() -> usize {
    64
}
fn default_context_lines() -> usize {
    2
}
fn default_max_response_bytes() -> usize {
    128 * 1024
}
fn default_peer_timeout_ms() -> u64 {
    2_000
}
fn default_overall_timeout_ms() -> u64 {
    5_000
}
fn default_max_file_bytes() -> u64 {
    16 * 1024 * 1024
}

impl Default for LimitsConfig {
    fn default() -> Self {
        Self {
            max_results: default_max_results(),
            context_lines: default_context_lines(),
            max_response_bytes: default_max_response_bytes(),
            peer_timeout_ms: default_peer_timeout_ms(),
            overall_timeout_ms: default_overall_timeout_ms(),
            max_file_bytes: default_max_file_bytes(),
        }
    }
}

/// Search policy editable at runtime by the loopback-only GrepMesh admin UI.
/// It deliberately excludes topology and all authentication settings.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct RuntimeSettings {
    pub roots: BTreeMap<String, Vec<PathBuf>>,
    pub exclude_globs: Vec<String>,
    pub limits: LimitsConfig,
    #[serde(default)]
    pub unrestricted_roots: bool,
}

impl RuntimeSettings {
    pub fn from_config(config: &AppConfig) -> Self {
        Self {
            roots: config.roots.clone(),
            exclude_globs: config.exclude_globs.clone(),
            limits: config.limits.clone(),
            unrestricted_roots: false,
        }
    }

    pub fn validate(&self) -> Result<()> {
        if self.limits.max_results == 0 || self.limits.max_file_bytes == 0 {
            anyhow::bail!("limits.max_results and limits.max_file_bytes must be positive");
        }
        for (name, paths) in &self.roots {
            if name.trim().is_empty()
                || paths.is_empty()
                || paths.iter().any(|path| !path.is_absolute())
            {
                anyhow::bail!("each root requires a name and absolute path(s)");
            }
        }
        Ok(())
    }
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct AppConfig {
    pub host_id: String,
    pub bind: SocketAddr,
    #[serde(default)]
    pub local_bind: Option<SocketAddr>,
    pub root: PathBuf,
    #[serde(default)]
    pub roots: BTreeMap<String, Vec<PathBuf>>,
    #[serde(default)]
    pub peers: Vec<PeerConfig>,
    #[serde(default)]
    pub limits: LimitsConfig,
    #[serde(default)]
    pub exclude_globs: Vec<String>,
    #[serde(default)]
    pub topology_cache_path: Option<PathBuf>,
    /// Persistent full-text indexing is opt-in. `rg` is the production default:
    /// it avoids a background full-tree scan for repositories that already
    /// search quickly with ripgrep.
    #[serde(default)]
    pub index_path: Option<PathBuf>,
    #[serde(default)]
    pub gptadmin_topology_url: Option<String>,
    #[serde(default)]
    pub gptadmin_token_env: Option<String>,
    #[serde(default)]
    pub peer_auth_token_env: Option<String>,
    #[serde(default = "default_topology_ttl_ms")]
    pub topology_ttl_ms: u64,
}

fn default_topology_ttl_ms() -> u64 {
    30_000
}

impl AppConfig {
    pub fn from_path(path: impl AsRef<Path>) -> Result<Self> {
        let bytes = fs::read(path.as_ref())
            .with_context(|| format!("read config {}", path.as_ref().display()))?;
        let mut cfg: AppConfig = serde_json::from_slice(&bytes).context("parse config JSON")?;
        if cfg.limits.max_results == 0 {
            cfg.limits.max_results = default_max_results();
        }
        if cfg.limits.max_file_bytes == 0 {
            cfg.limits.max_file_bytes = default_max_file_bytes();
        }
        Ok(cfg)
    }

    pub fn peer_timeout(&self) -> Duration {
        Duration::from_millis(self.limits.peer_timeout_ms)
    }

    pub fn overall_timeout(&self) -> Duration {
        Duration::from_millis(self.limits.overall_timeout_ms)
    }
}
