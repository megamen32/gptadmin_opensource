use crate::config::{default_exclude_globs, LimitsConfig};
use crate::index::IndexManager;
use anyhow::{anyhow, Context, Result};
use globset::Glob;
use serde::{Deserialize, Serialize};
use std::{
    collections::{BTreeMap, BTreeSet},
    fs,
    path::{Path, PathBuf},
    process::Stdio,
    time::Duration,
};
use tokio::{
    io::AsyncReadExt,
    process::Command as TokioCommand,
    time::{timeout, Instant},
};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct MatchLine {
    pub line_number: usize,
    #[serde(default)]
    pub line_id: String,
    pub text: String,
}

pub fn stable_line_id(line_number: usize, text: &str) -> String {
    let mut hash: u32 = 2_166_136_261;
    for byte in text.as_bytes() {
        hash ^= *byte as u32;
        hash = hash.wrapping_mul(16_777_619);
    }
    let folded = ((hash >> 16) ^ (hash & 0xffff)) as u16;
    format!("{line_number}:{folded:04x}")
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SearchHit {
    pub host_id: String,
    pub path: String,
    pub line_number: usize,
    pub context: Vec<MatchLine>,
    #[serde(default)]
    pub text: String,
    #[serde(default)]
    pub column: usize,
}

/// One matching line inside a compact search range.
///
/// Its text is stored once in [`SearchMatchRange::lines`], alongside the
/// context lines. Keeping the column here preserves the match metadata without
/// repeating the matching text for every hit.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct SearchRangeMatch {
    pub line_number: usize,
    pub column: usize,
}

/// Adjacent matches from one file, with their visible match and context lines
/// stored once in line-number order.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct SearchMatchRange {
    pub host_id: String,
    pub path: String,
    pub start_line: usize,
    pub end_line: usize,
    pub matches: Vec<SearchRangeMatch>,
    pub lines: Vec<MatchLine>,
}

#[derive(Debug)]
pub struct SearchOutcome {
    pub hits: Vec<SearchHit>,
    pub truncated: bool,
    pub partial: bool,
    pub partial_error: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum SearchMode {
    #[default]
    Literal,
    Regex,
    CaseInsensitiveLiteral,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub enum IndexState {
    Ready,
    Building,
    Degraded,
    Corrupt,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ReadChunk {
    pub start_line: usize,
    pub end_line: usize,
    pub lines: Vec<MatchLine>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HostStatus {
    pub host_id: String,
    pub root: String,
    pub backend: String,
    pub file_count: usize,
    #[serde(default)]
    pub index_state: Option<IndexState>,
    #[serde(default)]
    pub index_generation: u64,
    #[serde(default)]
    pub indexed_files: usize,
    #[serde(default)]
    pub index_last_error: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PerHostStatus {
    pub host_id: String,
    pub ok: bool,
    pub error: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SearchResponse<T> {
    pub request_id: String,
    pub origin_host: String,
    pub hop_count: u8,
    pub host_id: String,
    pub partial: bool,
    pub truncated: bool,
    pub results: Vec<T>,
    pub host_status: Vec<PerHostStatus>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ReadResponse {
    pub request_id: String,
    pub origin_host: String,
    pub hop_count: u8,
    pub host_id: String,
    pub target_host_id: String,
    pub partial: bool,
    pub truncated: bool,
    pub path: String,
    pub start_line: usize,
    pub end_line: usize,
    pub chunks: Vec<ReadChunk>,
    pub host_status: Vec<PerHostStatus>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StatusResponse {
    pub request_id: String,
    pub origin_host: String,
    pub hop_count: u8,
    pub host_id: String,
    pub partial: bool,
    pub host_status: Vec<PerHostStatus>,
    pub local: HostStatus,
    #[serde(default)]
    pub nodes: Vec<HostStatus>,
    pub topology: crate::topology::TopologyStatus,
}

#[derive(Clone)]
pub struct LocalBackend {
    pub host_id: String,
    pub root: PathBuf,
    pub root_paths: BTreeMap<String, Vec<PathBuf>>,
    pub limits: LimitsConfig,
    pub exclude_globs: Vec<String>,
    pub unrestricted_roots: bool,
    pub index: IndexManager,
}

impl LocalBackend {
    pub fn new(host_id: impl Into<String>, root: impl Into<PathBuf>, limits: LimitsConfig) -> Self {
        let root = root.into();
        let mut root_paths = BTreeMap::new();
        root_paths.insert("local".to_string(), vec![root.clone()]);
        let excludes = default_exclude_globs();
        let index = IndexManager::start(
            root_paths.clone(),
            excludes.clone(),
            limits.max_file_bytes,
            None,
        );
        Self {
            host_id: host_id.into(),
            root,
            root_paths,
            limits,
            exclude_globs: excludes,
            unrestricted_roots: false,
            index,
        }
    }

    pub fn from_config(
        host_id: impl Into<String>,
        root: impl Into<PathBuf>,
        limits: LimitsConfig,
        roots: BTreeMap<String, Vec<PathBuf>>,
        exclude_globs: Vec<String>,
        index_path: Option<PathBuf>,
    ) -> Self {
        let root = root.into();
        let mut root_paths = roots
            .into_iter()
            .filter(|(_, paths)| !paths.is_empty())
            .collect::<BTreeMap<_, _>>();
        root_paths.insert("local".to_string(), vec![root.clone()]);
        // A non-empty config is authoritative so the runtime UI can both add
        // and remove exclusions. Empty legacy configs retain safe defaults.
        let mut excludes = if exclude_globs.is_empty() {
            default_exclude_globs()
        } else {
            exclude_globs
        };
        excludes.sort();
        excludes.dedup();
        let index = match index_path {
            Some(index_path) => IndexManager::start(
                root_paths.clone(),
                excludes.clone(),
                limits.max_file_bytes,
                Some(index_path),
            ),
            None => IndexManager::disabled(),
        };
        Self {
            host_id: host_id.into(),
            root,
            root_paths,
            limits,
            exclude_globs: excludes,
            unrestricted_roots: false,
            index,
        }
    }

    /// Permit an existing absolute directory supplied by the caller. This is
    /// opt-in because it expands the service's file visibility boundary.
    pub fn with_unrestricted_roots(mut self, unrestricted_roots: bool) -> Self {
        self.unrestricted_roots = unrestricted_roots;
        self
    }

    pub fn with_excludes(mut self, excludes: Vec<String>) -> Self {
        self.exclude_globs.extend(excludes);
        self.exclude_globs.sort();
        self.exclude_globs.dedup();
        self.index = IndexManager::start(
            self.root_paths.clone(),
            self.exclude_globs.clone(),
            self.limits.max_file_bytes,
            None,
        );
        self
    }

    pub fn with_named_roots(mut self, roots: BTreeMap<String, Vec<PathBuf>>) -> Self {
        let mut configured = BTreeMap::new();
        for (name, paths) in roots {
            let paths = paths
                .into_iter()
                .filter(|path| path.is_absolute())
                .collect::<Vec<_>>();
            if !paths.is_empty() {
                configured.insert(name, paths);
            }
        }
        configured.insert("local".to_string(), vec![self.root.clone()]);
        self.root_paths = configured;
        self.index = IndexManager::start(
            self.root_paths.clone(),
            self.exclude_globs.clone(),
            self.limits.max_file_bytes,
            None,
        );
        self
    }

    pub fn status(&self) -> Result<HostStatus> {
        let index = self.index.status();
        Ok(HostStatus {
            host_id: self.host_id.clone(),
            root: self.root.display().to_string(),
            backend: if self.index.is_enabled() {
                "indexed+rg-fallback"
            } else {
                "rg"
            }
            .to_string(),
            file_count: index.indexed_files,
            index_state: Some(index.state),
            index_generation: index.generation,
            indexed_files: index.indexed_files,
            index_last_error: index.last_error,
        })
    }

    fn selected_roots(&self, names: &[String]) -> Result<Vec<PathBuf>> {
        let mut roots = Vec::new();
        if names.is_empty() {
            for (name, paths) in &self.root_paths {
                if name != "local" {
                    roots.extend(paths.iter().cloned());
                }
            }
        } else {
            for name in names {
                let paths = if let Some(paths) = self.root_paths.get(name) {
                    paths.clone()
                } else {
                    let requested = Path::new(name);
                    let requested = fs::canonicalize(requested)
                        .with_context(|| format!("resolve requested root {name}"))?;
                    let allowed = self.unrestricted_roots
                        || self
                            .root_paths
                            .values()
                            .flatten()
                            .filter_map(|path| fs::canonicalize(path).ok())
                            .any(|path| requested.starts_with(path));
                    if !allowed {
                        return Err(anyhow!("unknown root {}", name));
                    }
                    vec![requested]
                };
                roots.extend(paths);
            }
        }
        roots.sort();
        roots.dedup();
        if roots.is_empty() {
            roots.push(self.root.clone());
        }
        Ok(roots)
    }

    pub async fn search_text(
        &self,
        query: &str,
        limit: usize,
        context_lines: usize,
        mode: SearchMode,
        path_globs: Vec<String>,
        root_names: Vec<String>,
    ) -> Result<Vec<SearchHit>> {
        Ok(self
            .search_text_bounded(query, limit, context_lines, mode, path_globs, root_names)
            .await?
            .hits)
    }

    pub async fn search_text_bounded(
        &self,
        query: &str,
        limit: usize,
        context_lines: usize,
        mode: SearchMode,
        path_globs: Vec<String>,
        root_names: Vec<String>,
    ) -> Result<SearchOutcome> {
        let host_id = self.host_id.clone();
        let roots = self.selected_roots(&root_names)?;
        let query = query.to_string();
        let exclude_globs = self.exclude_globs.clone();
        let max_file_bytes = self.limits.max_file_bytes;
        let max_response_bytes = self.limits.max_response_bytes;
        let deadline = Instant::now() + Duration::from_millis(self.limits.overall_timeout_ms);
        let mut hits = Vec::new();
        let mut truncated = false;
        let mut partial = false;
        let mut partial_error = None;
        for root in roots {
            let remaining = limit.saturating_sub(hits.len());
            if remaining == 0 {
                truncated = true;
                break;
            }
            let remaining_time = deadline.saturating_duration_since(Instant::now());
            if remaining_time.is_zero() {
                return Err(anyhow!("local rg search timed out"));
            }
            let candidate_paths = if path_globs.is_empty()
                && matches!(
                    mode,
                    SearchMode::Literal | SearchMode::CaseInsensitiveLiteral
                ) {
                self.index.candidate_paths(&query, &root)
            } else {
                None
            };
            let outcome = search_text_impl(
                &host_id,
                &root,
                &query,
                remaining,
                context_lines,
                &mode,
                &path_globs,
                &exclude_globs,
                max_file_bytes,
                max_response_bytes,
                deadline,
                candidate_paths,
            )
            .await?;
            hits.extend(outcome.hits);
            partial |= outcome.partial;
            if partial_error.is_none() {
                partial_error = outcome.partial_error;
            }
            if outcome.truncated {
                truncated = true;
                break;
            }
        }
        hits.sort_by(|a, b| a.path.cmp(&b.path).then(a.line_number.cmp(&b.line_number)));
        hits.truncate(limit);
        Ok(SearchOutcome {
            hits,
            truncated,
            partial,
            partial_error,
        })
    }

    pub async fn find_paths(
        &self,
        query: &str,
        limit: usize,
        root_names: Vec<String>,
    ) -> Result<Vec<SearchHit>> {
        Ok(self
            .find_paths_bounded(query, limit, root_names)
            .await?
            .hits)
    }

    pub async fn find_paths_bounded(
        &self,
        query: &str,
        limit: usize,
        root_names: Vec<String>,
    ) -> Result<SearchOutcome> {
        let host_id = self.host_id.clone();
        let roots = self.selected_roots(&root_names)?;
        let query = query.to_string();
        let exclude_globs = self.exclude_globs.clone();
        let max_response_bytes = self.limits.max_response_bytes;
        let deadline = Instant::now() + Duration::from_millis(self.limits.overall_timeout_ms);
        let mut hits = Vec::new();
        let mut truncated = false;
        for root in roots {
            let remaining = limit.saturating_sub(hits.len());
            if remaining == 0 {
                truncated = true;
                break;
            }
            if deadline.saturating_duration_since(Instant::now()).is_zero() {
                return Err(anyhow!("local rg path search timed out"));
            }
            let outcome = find_paths_impl(
                &host_id,
                &root,
                &query,
                remaining,
                &exclude_globs,
                max_response_bytes,
                deadline,
            )
            .await?;
            hits.extend(outcome.hits);
            if outcome.truncated {
                truncated = true;
                break;
            }
        }
        hits.sort_by(|a, b| a.path.cmp(&b.path));
        hits.truncate(limit);
        Ok(SearchOutcome {
            hits,
            truncated,
            partial: false,
            partial_error: None,
        })
    }

    pub fn read_text(
        &self,
        path: &Path,
        start_line: Option<usize>,
        end_line: Option<usize>,
    ) -> Result<Vec<ReadChunk>> {
        let roots = self.selected_roots(&[])?;
        let abs = normalize_absolute_path_any(&roots, path)?;
        let size = fs::metadata(&abs)
            .with_context(|| format!("metadata {}", abs.display()))?
            .len();
        if size > self.limits.max_file_bytes {
            return Err(anyhow!(
                "file {} exceeds max_file_bytes {}",
                abs.display(),
                self.limits.max_file_bytes
            ));
        }
        let text = fs::read_to_string(&abs).with_context(|| format!("read {}", abs.display()))?;
        let lines: Vec<_> = text.lines().collect();
        let start = start_line.unwrap_or(1).max(1);
        let end = end_line
            .unwrap_or(lines.len().max(1))
            .max(start)
            .min(lines.len().max(start));
        let mut out = Vec::new();
        let mut chunk = Vec::new();
        for idx in start..=end {
            if idx == 0 || idx > lines.len() {
                break;
            }
            chunk.push(MatchLine {
                line_number: idx,
                line_id: stable_line_id(idx, lines[idx - 1]),
                text: lines[idx - 1].to_string(),
            });
        }
        out.push(ReadChunk {
            start_line: start,
            end_line: end,
            lines: chunk,
        });
        Ok(out)
    }
}

#[allow(clippy::too_many_arguments)]
async fn search_text_impl(
    host_id: &str,
    root: &Path,
    query: &str,
    limit: usize,
    context_lines: usize,
    mode: &SearchMode,
    path_globs: &[String],
    exclude_globs: &[String],
    max_file_bytes: u64,
    max_response_bytes: usize,
    deadline: Instant,
    candidate_paths: Option<Vec<PathBuf>>,
) -> Result<SearchOutcome> {
    let mut command = TokioCommand::new("rg");
    command.arg("-n").arg("--hidden");
    match mode {
        SearchMode::Literal => {
            command.arg("-F");
        }
        SearchMode::Regex => {}
        SearchMode::CaseInsensitiveLiteral => {
            command.arg("-F").arg("-i");
        }
    }
    for glob in path_globs {
        command.arg("--glob").arg(glob);
    }
    for glob in exclude_globs {
        command.arg("--glob").arg(format!("!{glob}"));
    }
    command
        .arg("--max-filesize")
        .arg(max_file_bytes.to_string());
    if candidate_paths
        .as_ref()
        .is_some_and(|paths| !paths.is_empty())
    {
        command.arg("--with-filename");
    }
    command.arg("--");
    command.arg(query);
    if let Some(paths) = candidate_paths {
        if paths.is_empty() {
            return Ok(SearchOutcome {
                hits: Vec::new(),
                truncated: false,
                partial: false,
                partial_error: None,
            });
        }
        for path in paths {
            command.arg(path);
        }
    } else {
        command.arg(".");
    }
    let mut child = command
        .current_dir(root)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true)
        .spawn()
        .with_context(|| "run rg search")?;
    let Some(mut stdout) = child.stdout.take() else {
        terminate_child(&mut child).await;
        return Err(anyhow!("rg search did not provide stdout"));
    };
    let Some(mut stderr) = child.stderr.take() else {
        terminate_child(&mut child).await;
        return Err(anyhow!("rg search did not provide stderr"));
    };
    let stderr_task = tokio::spawn(async move {
        let mut output = Vec::new();
        let mut buffer = [0u8; 8192];
        let mut overflowed = false;
        loop {
            let read = stderr.read(&mut buffer).await?;
            if read == 0 {
                break;
            }
            let remaining = (64 * 1024usize).saturating_sub(output.len());
            let kept = read.min(remaining);
            output.extend_from_slice(&buffer[..kept]);
            overflowed |= kept != read;
        }
        Ok::<_, std::io::Error>((output, overflowed))
    });
    let mut hits = Vec::new();
    let mut pending = Vec::new();
    let mut stdout_bytes = 0usize;
    // A zero response limit is the existing configuration convention for an
    // unbounded response. Otherwise, cap the total bytes consumed from this
    // rg invocation and stop as soon as the global match/byte bound is met.
    let byte_limit = (max_response_bytes != 0).then_some(max_response_bytes);
    let mut stopped_early = limit == 0;
    let mut buffer = [0u8; 8192];

    while !stopped_early {
        let read_size = match byte_limit {
            Some(byte_limit) => {
                let remaining_bytes = byte_limit.saturating_sub(stdout_bytes);
                if remaining_bytes == 0 {
                    stopped_early = true;
                    break;
                }
                remaining_bytes.min(buffer.len())
            }
            None => buffer.len(),
        };
        let read = match timeout(
            deadline.saturating_duration_since(Instant::now()),
            stdout.read(&mut buffer[..read_size]),
        )
        .await
        {
            Ok(Ok(read)) => read,
            Ok(Err(err)) => {
                terminate_child(&mut child).await;
                return Err(err).context("read rg search output");
            }
            Err(_) => {
                terminate_child(&mut child).await;
                return Err(anyhow!("rg search timed out"));
            }
        };
        if read == 0 {
            break;
        }
        stdout_bytes = stdout_bytes.saturating_add(read);
        pending.extend_from_slice(&buffer[..read]);

        while let Some(line_end) = pending.iter().position(|byte| *byte == b'\n') {
            let line = pending.drain(..=line_end).collect::<Vec<_>>();
            let hit = match search_hit_from_rg_line(
                host_id,
                root,
                query,
                context_lines,
                mode,
                &line,
                deadline,
            )
            .await
            {
                Ok(hit) => hit,
                Err(err) => {
                    terminate_child(&mut child).await;
                    return Err(err);
                }
            };
            if let Some(hit) = hit {
                hits.push(hit);
            }
            if hits.len() >= limit {
                stopped_early = true;
                break;
            }
        }
    }

    if !stopped_early && !pending.is_empty() {
        let hit = match search_hit_from_rg_line(
            host_id,
            root,
            query,
            context_lines,
            mode,
            &pending,
            deadline,
        )
        .await
        {
            Ok(hit) => hit,
            Err(err) => {
                terminate_child(&mut child).await;
                return Err(err);
            }
        };
        if let Some(hit) = hit {
            hits.push(hit);
        }
    }

    drop(stdout);
    let mut partial = false;
    let mut partial_error = None;
    if stopped_early {
        terminate_child(&mut child).await;
    } else {
        let status = match timeout(
            deadline.saturating_duration_since(Instant::now()),
            child.wait(),
        )
        .await
        {
            Ok(Ok(status)) => status,
            Ok(Err(err)) => return Err(err).context("wait for rg search"),
            Err(_) => {
                terminate_child(&mut child).await;
                return Err(anyhow!("rg search timed out"));
            }
        };
        let (stderr, stderr_overflowed) = stderr_task
            .await
            .context("collect rg search diagnostics")??;
        if stderr_overflowed {
            return Err(anyhow!("rg search diagnostics exceeded 65536 bytes"));
        }
        if status.code() == Some(2) {
            let diagnostic = permission_denied_diagnostic(&stderr).ok_or_else(|| {
                anyhow!(
                    "rg search failed with {}: {}",
                    status,
                    String::from_utf8_lossy(&stderr).trim()
                )
            })?;
            partial = true;
            partial_error = Some(diagnostic);
        } else if !status.success() && status.code() != Some(1) {
            return Err(anyhow!(
                "rg search failed with {}: {}",
                status,
                String::from_utf8_lossy(&stderr).trim()
            ));
        }
    }
    Ok(SearchOutcome {
        hits,
        truncated: stopped_early,
        partial,
        partial_error,
    })
}

fn permission_denied_diagnostic(stderr: &[u8]) -> Option<String> {
    let diagnostic = String::from_utf8_lossy(stderr).trim().to_string();
    let mut lines = diagnostic
        .lines()
        .filter(|line| !line.trim().is_empty())
        .peekable();
    lines.peek()?;
    lines
        .all(|line| {
            let line = line.to_ascii_lowercase();
            line.contains("permission denied") || line.contains("operation not permitted")
        })
        .then_some(diagnostic)
}

async fn terminate_child(child: &mut tokio::process::Child) {
    let _ = child.start_kill();
    let _ = child.wait().await;
}

async fn search_hit_from_rg_line(
    host_id: &str,
    root: &Path,
    query: &str,
    context_lines: usize,
    mode: &SearchMode,
    raw_line: &[u8],
    deadline: Instant,
) -> Result<Option<SearchHit>> {
    let line = String::from_utf8_lossy(raw_line);
    let line = line.strip_suffix('\n').unwrap_or(&line);
    let line = line.strip_suffix('\r').unwrap_or(line);
    let Some((rel_path, line_no, text)) = parse_rg_line(line) else {
        return Ok(None);
    };
    let abs = root.join(strip_current_dir_prefix(rel_path));
    let remaining = deadline.saturating_duration_since(Instant::now());
    let context = timeout(remaining, read_context(&abs, line_no, context_lines))
        .await
        .map_err(|_| anyhow!("rg search timed out"))??;
    Ok(Some(SearchHit {
        host_id: host_id.to_string(),
        path: abs.display().to_string(),
        line_number: line_no,
        context,
        text: text.to_string(),
        column: match mode {
            SearchMode::Regex => 1,
            SearchMode::Literal => text.find(query).map(|index| index + 1).unwrap_or(1),
            SearchMode::CaseInsensitiveLiteral => text
                .to_ascii_lowercase()
                .find(&query.to_ascii_lowercase())
                .map(|index| index + 1)
                .unwrap_or(1),
        },
    }))
}

async fn find_paths_impl(
    host_id: &str,
    root: &Path,
    query: &str,
    limit: usize,
    exclude_globs: &[String],
    max_response_bytes: usize,
    deadline: Instant,
) -> Result<SearchOutcome> {
    let mut command = TokioCommand::new("rg");
    command.arg("--files").arg("--hidden").arg("--no-messages");
    for glob in exclude_globs {
        command.arg("--glob").arg(format!("!{glob}"));
    }
    let mut child = command
        .arg(".")
        .current_dir(root)
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .kill_on_drop(true)
        .spawn()
        .with_context(|| "run rg --files")?;
    let Some(mut stdout) = child.stdout.take() else {
        terminate_child(&mut child).await;
        return Err(anyhow!("rg --files did not provide stdout"));
    };
    let mut hits = Vec::new();
    let mut pending = Vec::new();
    let mut stdout_bytes = 0usize;
    let byte_limit = (max_response_bytes != 0).then_some(max_response_bytes);
    let mut stopped_early = limit == 0;
    let mut buffer = [0u8; 8192];

    while !stopped_early {
        let read_size = match byte_limit {
            Some(byte_limit) => {
                let remaining_bytes = byte_limit.saturating_sub(stdout_bytes);
                if remaining_bytes == 0 {
                    stopped_early = true;
                    break;
                }
                remaining_bytes.min(buffer.len())
            }
            None => buffer.len(),
        };
        let read = match timeout(
            deadline.saturating_duration_since(Instant::now()),
            stdout.read(&mut buffer[..read_size]),
        )
        .await
        {
            Ok(Ok(read)) => read,
            Ok(Err(err)) => {
                terminate_child(&mut child).await;
                return Err(err).context("read rg path output");
            }
            Err(_) => {
                terminate_child(&mut child).await;
                return Err(anyhow!("rg path search timed out"));
            }
        };
        if read == 0 {
            break;
        }
        stdout_bytes = stdout_bytes.saturating_add(read);
        pending.extend_from_slice(&buffer[..read]);

        while let Some(line_end) = pending.iter().position(|byte| *byte == b'\n') {
            let line = pending.drain(..=line_end).collect::<Vec<_>>();
            if let Some(hit) = path_hit_from_rg_line(host_id, root, query, &line) {
                hits.push(hit);
            }
            if hits.len() >= limit {
                stopped_early = true;
                break;
            }
        }
    }

    if !stopped_early && !pending.is_empty() {
        if let Some(hit) = path_hit_from_rg_line(host_id, root, query, &pending) {
            hits.push(hit);
        }
    }

    drop(stdout);
    if stopped_early {
        terminate_child(&mut child).await;
    } else {
        let status = match timeout(
            deadline.saturating_duration_since(Instant::now()),
            child.wait(),
        )
        .await
        {
            Ok(Ok(status)) => status,
            Ok(Err(err)) => return Err(err).context("wait for rg --files"),
            Err(_) => {
                terminate_child(&mut child).await;
                return Err(anyhow!("rg path search timed out"));
            }
        };
        if !status.success() && status.code() != Some(1) && status.code() != Some(2) {
            return Err(anyhow!("rg --files failed with {}", status));
        }
    }
    Ok(SearchOutcome {
        hits,
        truncated: stopped_early,
        partial: false,
        partial_error: None,
    })
}

fn path_hit_from_rg_line(
    host_id: &str,
    root: &Path,
    query: &str,
    raw_line: &[u8],
) -> Option<SearchHit> {
    let line = String::from_utf8_lossy(raw_line);
    let line = line.strip_suffix('\n').unwrap_or(&line);
    let line = line.strip_suffix('\r').unwrap_or(line);
    if !path_query_matches(query, line) {
        return None;
    }
    let reported_path = Path::new(strip_current_dir_prefix(line));
    let abs = if reported_path.is_absolute() {
        reported_path.to_path_buf()
    } else {
        root.join(reported_path)
    };
    Some(SearchHit {
        host_id: host_id.to_string(),
        path: abs.display().to_string(),
        line_number: 0,
        context: Vec::new(),
        text: String::new(),
        column: 0,
    })
}

fn path_query_matches(pattern: &str, path: &str) -> bool {
    if pattern.contains('*') || pattern.contains('?') {
        return Glob::new(pattern)
            .map(|glob| glob.compile_matcher().is_match(path))
            .unwrap_or(false);
    }
    path.contains(pattern)
}

fn parse_rg_line(line: &str) -> Option<(&str, usize, &str)> {
    let (path, rest) = line.split_once(':')?;
    let (line_no, text) = rest.split_once(':')?;
    Some((path, line_no.parse().ok()?, text))
}

fn strip_current_dir_prefix(path: &str) -> &str {
    path.strip_prefix("./").unwrap_or(path)
}

async fn read_context(
    path: &Path,
    line_number: usize,
    context_lines: usize,
) -> Result<Vec<MatchLine>> {
    let text = tokio::fs::read_to_string(path)
        .await
        .with_context(|| format!("read {}", path.display()))?;
    let lines: Vec<_> = text.lines().collect();
    let start = line_number.saturating_sub(context_lines).max(1);
    let end = std::cmp::min(lines.len(), line_number + context_lines);
    let mut out = Vec::new();
    for idx in start..=end {
        out.push(MatchLine {
            line_number: idx,
            line_id: stable_line_id(idx, lines[idx - 1]),
            text: lines[idx - 1].to_string(),
        });
    }
    Ok(out)
}

fn normalize_absolute_path_any(roots: &[PathBuf], path: &Path) -> Result<PathBuf> {
    if !path.is_absolute() {
        return Err(anyhow!("path must be absolute: {}", path.display()));
    }
    if !roots.iter().any(|root| path.starts_with(root)) {
        return Err(anyhow!(
            "path {} is outside configured roots",
            path.display(),
        ));
    }
    Ok(path.to_path_buf())
}

pub fn dedup_hits<T, F>(items: Vec<T>, key: F) -> Vec<T>
where
    F: Fn(&T) -> (String, String, usize),
{
    let mut seen = BTreeSet::new();
    let mut out = Vec::new();
    for item in items {
        let k = key(&item);
        if seen.insert(k) {
            out.push(item);
        }
    }
    out
}

#[cfg(test)]
mod line_id_contract_tests {
    use super::stable_line_id;

    #[test]
    fn line_id_matches_gptadmin_editor_contract() {
        assert_eq!(stable_line_id(2, "beta"), "2:4b46");
    }
}
