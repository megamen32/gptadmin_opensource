use crate::{
    backend::{
        dedup_hits, stable_line_id, LocalBackend, MatchLine, PerHostStatus, ReadResponse,
        SearchHit, SearchMatchRange, SearchMode, SearchRangeMatch, SearchResponse, StatusResponse,
    },
    topology::Topology,
};
use anyhow::{anyhow, Context, Result};
use futures::{stream::FuturesUnordered, StreamExt};
use reqwest::Client;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::{
    collections::{BTreeMap, BTreeSet},
    path::PathBuf,
    sync::{Arc, RwLock},
    time::{Duration, Instant},
};
use tokio::time::timeout;

#[derive(Debug, Clone, Deserialize)]
#[serde(untagged)]
pub enum HostsInput {
    One(String),
    Many(Vec<String>),
}

impl HostsInput {
    fn into_vec(self) -> Vec<String> {
        match self {
            HostsInput::One(v) => vec![v],
            HostsInput::Many(v) => v,
        }
    }
}

#[derive(Debug, Clone, Deserialize)]
pub struct SearchArgs {
    pub query: String,
    #[serde(default)]
    pub hosts: Option<HostsInput>,
    #[serde(default)]
    pub request_id: Option<String>,
    #[serde(default)]
    pub origin_host: Option<String>,
    #[serde(default)]
    pub hop_count: Option<u8>,
    #[serde(default, alias = "max_matches")]
    pub limit: Option<usize>,
    #[serde(default)]
    pub context_lines: Option<usize>,
    #[serde(default)]
    pub mode: SearchMode,
    #[serde(default)]
    pub path_globs: Vec<String>,
    #[serde(default)]
    pub roots: Vec<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct FindPathsArgs {
    #[serde(alias = "pattern")]
    pub query: String,
    #[serde(default)]
    pub hosts: Option<HostsInput>,
    #[serde(default)]
    pub request_id: Option<String>,
    #[serde(default)]
    pub origin_host: Option<String>,
    #[serde(default)]
    pub hop_count: Option<u8>,
    #[serde(default)]
    #[serde(alias = "max_matches")]
    pub limit: Option<usize>,
    #[serde(default)]
    pub roots: Vec<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct ReadTextArgs {
    pub host: String,
    pub path: PathBuf,
    #[serde(default)]
    pub start_line: Option<usize>,
    #[serde(default)]
    pub end_line: Option<usize>,
    #[serde(default)]
    pub request_id: Option<String>,
    #[serde(default)]
    pub origin_host: Option<String>,
    #[serde(default)]
    pub hop_count: Option<u8>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct StatusArgs {
    #[serde(default)]
    pub hosts: Option<HostsInput>,
    #[serde(default)]
    pub request_id: Option<String>,
    #[serde(default)]
    pub origin_host: Option<String>,
    #[serde(default)]
    pub hop_count: Option<u8>,
}

#[derive(Debug, Clone)]
pub struct NormalizedRequest {
    pub request_id: String,
    pub origin_host: String,
    pub hop_count: u8,
    pub hosts: Vec<String>,
}

pub fn normalize_request(
    local_host_id: &str,
    known_hosts: impl IntoIterator<Item = String>,
    hosts: Option<HostsInput>,
    request_id: Option<String>,
    origin_host: Option<String>,
    hop_count: Option<u8>,
) -> Result<NormalizedRequest> {
    let hop_count = hop_count.unwrap_or(0);
    if hop_count > 1 {
        return Err(anyhow!("hop_count above one is rejected"));
    }

    let mut known: BTreeSet<String> = known_hosts.into_iter().collect();
    known.insert(local_host_id.to_string());
    let requested = hosts.unwrap_or(HostsInput::One("*".into())).into_vec();
    let request_id = request_id.unwrap_or_else(|| format!("{}-{}", local_host_id, fresh_id()));
    let origin_host = origin_host.unwrap_or_else(|| local_host_id.to_string());

    let hosts = if hop_count == 1 {
        if requested != vec!["local".to_string()] {
            return Err(anyhow!("peer calls are local-only"));
        }
        vec![local_host_id.to_string()]
    } else if requested.len() == 1 && requested[0] == "local" {
        vec![local_host_id.to_string()]
    } else if requested.len() == 1 && requested[0] == "*" {
        known.into_iter().collect()
    } else {
        let mut dedup = BTreeSet::new();
        let mut out = Vec::new();
        for host in requested {
            let target = if host == "local" {
                local_host_id.to_string()
            } else {
                host
            };
            if !known.contains(&target) {
                return Err(anyhow!("unknown host {}", target));
            }
            if dedup.insert(target.clone()) {
                out.push(target);
            }
        }
        out
    };

    Ok(NormalizedRequest {
        request_id,
        origin_host,
        hop_count,
        hosts,
    })
}

fn fresh_id() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let d = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    format!("{:x}{:x}", d.as_secs(), d.subsec_nanos())
}

#[derive(Debug, Clone, Serialize)]
pub struct ToolResult {
    pub request_id: String,
    pub origin_host: String,
    pub hop_count: u8,
    pub host_id: String,
    pub partial: bool,
    pub truncated: bool,
    pub data: Value,
    pub host_status: Vec<PerHostStatus>,
}

/// The search result wire format used during a rolling mesh upgrade.
///
/// New peers send compact ranges. Accepting legacy hits here prevents an
/// upgraded fan-out node from dropping a result returned by an older peer.
#[derive(Debug, Deserialize)]
#[serde(untagged)]
enum RemoteSearchResult {
    Range(SearchMatchRange),
    LegacyHit(SearchHit),
}

fn compact_search_hits(hits: Vec<SearchHit>) -> Vec<SearchMatchRange> {
    let mut ranges = Vec::<SearchMatchRange>::new();
    for hit in hits {
        let continues_previous = ranges.last().is_some_and(|range| {
            range.host_id == hit.host_id
                && range.path == hit.path
                && hit.line_number == range.end_line.saturating_add(1)
        });
        if !continues_previous {
            ranges.push(SearchMatchRange {
                host_id: hit.host_id.clone(),
                path: hit.path.clone(),
                start_line: hit.line_number,
                end_line: hit.line_number,
                matches: Vec::new(),
                lines: Vec::new(),
            });
        }

        let range = ranges.last_mut().expect("a range was just inserted");
        range.end_line = hit.line_number;
        range.matches.push(SearchRangeMatch {
            line_number: hit.line_number,
            column: hit.column,
        });
        for line in hit.context {
            if !range
                .lines
                .iter()
                .any(|known| known.line_number == line.line_number)
            {
                range.lines.push(line);
            }
        }
        if !range
            .lines
            .iter()
            .any(|line| line.line_number == hit.line_number)
        {
            range.lines.push(MatchLine {
                line_number: hit.line_number,
                line_id: stable_line_id(hit.line_number, &hit.text),
                text: hit.text,
            });
        }
        range.lines.sort_by_key(|line| line.line_number);
    }
    ranges
}

fn expand_search_ranges(
    ranges: Vec<SearchMatchRange>,
    context_lines: usize,
) -> Result<Vec<SearchHit>> {
    let mut hits = Vec::new();
    for range in ranges {
        for matched in &range.matches {
            if matched.line_number < range.start_line || matched.line_number > range.end_line {
                return Err(anyhow!(
                    "compact match line {} is outside range {}-{}",
                    matched.line_number,
                    range.start_line,
                    range.end_line
                ));
            }
            let text = range
                .lines
                .iter()
                .find(|line| line.line_number == matched.line_number)
                .map(|line| line.text.clone())
                .ok_or_else(|| anyhow!("compact range omits match line {}", matched.line_number))?;
            let first_context_line = matched.line_number.saturating_sub(context_lines).max(1);
            let last_context_line = matched.line_number.saturating_add(context_lines);
            let context = range
                .lines
                .iter()
                .filter(|line| {
                    line.line_number >= first_context_line && line.line_number <= last_context_line
                })
                .cloned()
                .collect();
            hits.push(SearchHit {
                host_id: range.host_id.clone(),
                path: range.path.clone(),
                line_number: matched.line_number,
                context,
                text,
                column: matched.column,
            });
        }
    }
    Ok(hits)
}

fn with_search_paths_alias<T: Serialize>(
    response: SearchResponse<T>,
    paths: Vec<Value>,
) -> Result<Value> {
    let mut value = serde_json::to_value(response)?;
    value["paths"] = Value::Array(paths);
    Ok(value)
}

#[derive(Clone)]
pub struct MeshService {
    pub local: LocalBackend,
    pub topology: Arc<RwLock<Topology>>,
    pub client: Client,
    peer_auth_token: Option<String>,
    seen_requests: Arc<std::sync::Mutex<BTreeMap<String, Instant>>>,
}

impl MeshService {
    pub fn new(local: LocalBackend, topology: Topology) -> Self {
        Self {
            local,
            topology: Arc::new(RwLock::new(topology)),
            client: Client::new(),
            peer_auth_token: None,
            seen_requests: Arc::new(std::sync::Mutex::new(BTreeMap::new())),
        }
    }

    pub fn with_peer_auth_token(mut self, token: Option<String>) -> Self {
        self.peer_auth_token = token.filter(|value| !value.trim().is_empty());
        self
    }

    pub fn replace_topology(&self, topology: Topology) {
        if let Ok(mut current) = self.topology.write() {
            *current = topology;
        }
    }

    fn known_host_ids(&self) -> Vec<String> {
        self.topology
            .read()
            .map(|topology| topology.known_host_ids().collect())
            .unwrap_or_else(|_| vec![self.local.host_id.clone()])
    }

    fn peer_config(&self, host_id: &str) -> Option<crate::topology::PeerConfig> {
        self.topology
            .read()
            .ok()
            .and_then(|topology| topology.peer(host_id).cloned())
    }

    fn claim_request(&self, request_id: &str) -> Result<()> {
        let mut seen = self
            .seen_requests
            .lock()
            .map_err(|_| anyhow!("request deduplication lock poisoned"))?;
        let now = Instant::now();
        seen.retain(|_, first_seen| now.duration_since(*first_seen) < Duration::from_secs(60));
        if seen.contains_key(request_id) {
            return Err(anyhow!("duplicate request_id {}", request_id));
        }
        if seen.len() >= 4096 {
            if let Some(oldest) = seen
                .iter()
                .min_by_key(|(_, first_seen)| *first_seen)
                .map(|(id, _)| id.clone())
            {
                seen.remove(&oldest);
            }
        }
        seen.insert(request_id.to_string(), now);
        Ok(())
    }

    pub async fn call_search(&self, args: SearchArgs) -> Result<ToolResult> {
        let SearchArgs {
            query,
            hosts,
            request_id,
            origin_host,
            hop_count,
            limit,
            context_lines,
            mode,
            path_globs,
            roots,
        } = args;
        let normalized = normalize_request(
            &self.local.host_id,
            self.known_host_ids(),
            hosts,
            request_id,
            origin_host,
            hop_count,
        )?;
        self.claim_request(&normalized.request_id)?;
        let limit = limit
            .unwrap_or(self.local.limits.max_results)
            .min(self.local.limits.max_results);
        let context_lines = context_lines.unwrap_or(self.local.limits.context_lines);
        let (mut results, host_status, partial, mut truncated) = self
            .search_across(
                &normalized,
                &query,
                limit,
                context_lines,
                mode,
                path_globs,
                roots,
            )
            .await?;
        results.sort_by(|a, b| {
            a.host_id
                .cmp(&b.host_id)
                .then(a.path.cmp(&b.path))
                .then(a.line_number.cmp(&b.line_number))
        });
        results = dedup_hits(results, |item| {
            (item.host_id.clone(), item.path.clone(), item.line_number)
        });
        truncated |= results.len() > limit;
        results.truncate(limit);
        let request_id = normalized.request_id.clone();
        let origin_host = normalized.origin_host.clone();
        let hop_count = normalized.hop_count;
        let data = serde_json::to_value(SearchResponse {
            request_id: request_id.clone(),
            origin_host: origin_host.clone(),
            hop_count,
            host_id: self.local.host_id.clone(),
            partial,
            truncated,
            results: compact_search_hits(results),
            host_status: host_status.clone(),
        })?;
        Ok(self.wrap_result(
            request_id.clone(),
            origin_host.clone(),
            hop_count,
            partial,
            truncated,
            host_status.clone(),
            data,
        ))
    }

    pub async fn call_find_paths(&self, args: FindPathsArgs) -> Result<ToolResult> {
        let FindPathsArgs {
            query,
            hosts,
            request_id,
            origin_host,
            hop_count,
            limit,
            roots,
        } = args;
        let normalized = normalize_request(
            &self.local.host_id,
            self.known_host_ids(),
            hosts,
            request_id,
            origin_host,
            hop_count,
        )?;
        self.claim_request(&normalized.request_id)?;
        let limit = limit
            .unwrap_or(self.local.limits.max_results)
            .min(self.local.limits.max_results);
        let (mut results, host_status, partial, mut truncated) = self
            .find_paths_across(&normalized, &query, limit, roots)
            .await?;
        results.sort_by(|a, b| {
            a.host_id
                .cmp(&b.host_id)
                .then(a.path.cmp(&b.path))
                .then(a.line_number.cmp(&b.line_number))
        });
        results = dedup_hits(results, |item| {
            (item.host_id.clone(), item.path.clone(), item.line_number)
        });
        truncated |= results.len() > limit;
        results.truncate(limit);
        let request_id = normalized.request_id.clone();
        let origin_host = normalized.origin_host.clone();
        let hop_count = normalized.hop_count;
        let paths = results
            .iter()
            .map(|item| {
                json!({
                    "host": item.host_id,
                    "path": item.path,
                    "kind": "file",
                })
            })
            .collect::<Vec<_>>();
        let data = with_search_paths_alias(
            SearchResponse {
                request_id: request_id.clone(),
                origin_host: origin_host.clone(),
                hop_count,
                host_id: self.local.host_id.clone(),
                partial,
                truncated,
                results,
                host_status: host_status.clone(),
            },
            paths,
        )?;
        Ok(self.wrap_result(
            request_id.clone(),
            origin_host.clone(),
            hop_count,
            partial,
            truncated,
            host_status.clone(),
            data,
        ))
    }

    pub async fn call_read_text(&self, args: ReadTextArgs) -> Result<ToolResult> {
        let ReadTextArgs {
            host,
            path,
            start_line,
            end_line,
            request_id,
            origin_host,
            hop_count,
        } = args;
        let normalized = normalize_request(
            &self.local.host_id,
            self.known_host_ids(),
            Some(HostsInput::One(host.clone())),
            request_id,
            origin_host,
            hop_count,
        )?;
        self.claim_request(&normalized.request_id)?;
        let target = normalized
            .hosts
            .first()
            .cloned()
            .unwrap_or_else(|| self.local.host_id.clone());
        let response = if target == self.local.host_id {
            let chunks = self.local.read_text(&path, start_line, end_line)?;
            ReadResponse {
                request_id: normalized.request_id.clone(),
                origin_host: normalized.origin_host.clone(),
                hop_count: normalized.hop_count,
                host_id: self.local.host_id.clone(),
                target_host_id: self.local.host_id.clone(),
                partial: false,
                truncated: false,
                path: path.display().to_string(),
                start_line: start_line.unwrap_or(1),
                end_line: end_line.unwrap_or(usize::MAX),
                chunks,
                host_status: vec![PerHostStatus {
                    host_id: self.local.host_id.clone(),
                    ok: true,
                    error: None,
                }],
            }
        } else {
            let peer = self
                .peer_config(&target)
                .ok_or_else(|| anyhow!("unknown peer {}", target))?;
            let value = self
                .call_remote(
                    &peer.routable_url,
                    "read_text",
                    json!({
                        "host": "local",
                        "path": path,
                        "start_line": start_line,
                        "end_line": end_line,
                        "request_id": normalized.request_id.clone(),
                        "origin_host": normalized.origin_host.clone(),
                        "hop_count": 1u8,
                    }),
                )
                .await?;
            let mut response = decode_tool_result::<ReadResponse>(value)?;
            response.host_id = self.local.host_id.clone();
            response.target_host_id = target.clone();
            response
        };
        let host_status = response.host_status.clone();
        let request_id = normalized.request_id.clone();
        let origin_host = normalized.origin_host.clone();
        let hop_count = normalized.hop_count;
        Ok(self.wrap_result(
            request_id,
            origin_host,
            hop_count,
            response.partial,
            response.truncated,
            host_status,
            json!(response),
        ))
    }

    pub async fn call_status(&self, args: StatusArgs) -> Result<ToolResult> {
        let StatusArgs {
            hosts,
            request_id,
            origin_host,
            hop_count,
        } = args;
        let normalized = normalize_request(
            &self.local.host_id,
            self.known_host_ids(),
            hosts,
            request_id,
            origin_host,
            hop_count,
        )?;
        self.claim_request(&normalized.request_id)?;
        let local = self.local.status()?;
        let mut nodes = vec![local.clone()];
        let mut host_status = vec![PerHostStatus {
            host_id: self.local.host_id.clone(),
            ok: true,
            error: None,
        }];
        let mut partial = false;

        if normalized.hop_count == 0 {
            for target in normalized
                .hosts
                .iter()
                .filter(|host| *host != &self.local.host_id)
            {
                match self.remote_status(target, &normalized).await {
                    Ok(response) => {
                        host_status.extend(response.host_status);
                        if response.nodes.is_empty() {
                            nodes.push(response.local);
                        } else {
                            nodes.extend(response.nodes);
                        }
                    }
                    Err(err) => {
                        partial = true;
                        host_status.push(PerHostStatus {
                            host_id: target.clone(),
                            ok: false,
                            error: Some(err.to_string()),
                        });
                    }
                }
            }
        }
        nodes.sort_by(|a, b| a.host_id.cmp(&b.host_id));
        nodes.dedup_by(|a, b| a.host_id == b.host_id);

        let request_id = normalized.request_id.clone();
        let origin_host = normalized.origin_host.clone();
        let hop_count = normalized.hop_count;
        Ok(self.wrap_result(
            request_id.clone(),
            origin_host.clone(),
            hop_count,
            partial,
            false,
            host_status.clone(),
            json!(StatusResponse {
                request_id,
                origin_host,
                hop_count,
                host_id: self.local.host_id.clone(),
                partial,
                host_status,
                local,
                nodes,
                topology: self
                    .topology
                    .read()
                    .map(|topology| topology.status())
                    .unwrap_or_default(),
            }),
        ))
    }

    #[allow(clippy::too_many_arguments)]
    async fn search_across(
        &self,
        normalized: &NormalizedRequest,
        query: &str,
        limit: usize,
        context_lines: usize,
        mode: SearchMode,
        path_globs: Vec<String>,
        roots: Vec<String>,
    ) -> Result<(Vec<SearchHit>, Vec<PerHostStatus>, bool, bool)> {
        let mut futures = Vec::new();
        for target in unique_hosts(&normalized.hosts) {
            let service = self.clone();
            let target_for_error = target.clone();
            let request_id = normalized.request_id.clone();
            let origin_host = normalized.origin_host.clone();
            let query = query.to_string();
            let mode = mode.clone();
            let path_globs = path_globs.clone();
            let roots = roots.clone();
            let hop_count = hop_count_for(&service.local.host_id, &target, normalized.hop_count)?;
            futures.push((target.clone(), async move {
                let result: Result<(Vec<SearchHit>, Vec<PerHostStatus>, bool, bool)> = if target
                    == service.local.host_id
                {
                    let outcome = service
                        .local
                        .search_text_bounded(
                            &query,
                            limit,
                            context_lines,
                            mode.clone(),
                            path_globs.clone(),
                            roots.clone(),
                        )
                        .await?;
                    let truncated = outcome.truncated;
                    let partial = outcome.partial;
                    Ok((
                        outcome.hits,
                        vec![PerHostStatus {
                            host_id: target.clone(),
                            ok: !partial,
                            error: outcome.partial_error,
                        }],
                        partial,
                        truncated,
                    ))
                } else {
                    let peer = service
                        .peer_config(&target)
                        .ok_or_else(|| anyhow!("unknown peer {}", target))?;
                    let value = service
                        .call_remote(
                            &peer.routable_url,
                            "search_text",
                            json!({
                                "query": query,
                                "hosts": ["local"],
                                "request_id": request_id,
                                "origin_host": origin_host,
                                "hop_count": hop_count,
                                "limit": limit,
                                "context_lines": context_lines,
                                "mode": mode,
                                "path_globs": path_globs,
                                "roots": roots,
                            }),
                        )
                        .await?;
                    let response = decode_tool_result::<SearchResponse<RemoteSearchResult>>(value)?;
                    let remote_status = if response.host_status.is_empty() {
                        vec![PerHostStatus {
                            host_id: target.clone(),
                            ok: !response.partial,
                            error: response
                                .partial
                                .then(|| "remote response was partial".to_string()),
                        }]
                    } else {
                        response.host_status.clone()
                    };
                    Ok((
                        response
                            .results
                            .into_iter()
                            .map(|result| match result {
                                RemoteSearchResult::Range(range) => {
                                    expand_search_ranges(vec![range], context_lines)
                                }
                                RemoteSearchResult::LegacyHit(hit) => Ok(vec![hit]),
                            })
                            .collect::<Result<Vec<_>>>()?
                            .into_iter()
                            .flatten()
                            .collect(),
                        remote_status,
                        response.partial,
                        response.truncated,
                    ))
                };
                match result {
                    Ok(value) => Ok(value),
                    Err(err) => Ok((
                        Vec::new(),
                        vec![PerHostStatus {
                            host_id: target_for_error,
                            ok: false,
                            error: Some(err.to_string()),
                        }],
                        true,
                        false,
                    )),
                }
            }));
        }
        self.join_hosts(futures).await
    }

    async fn find_paths_across(
        &self,
        normalized: &NormalizedRequest,
        query: &str,
        limit: usize,
        roots: Vec<String>,
    ) -> Result<(Vec<SearchHit>, Vec<PerHostStatus>, bool, bool)> {
        let mut futures = Vec::new();
        for target in unique_hosts(&normalized.hosts) {
            let service = self.clone();
            let target_for_error = target.clone();
            let request_id = normalized.request_id.clone();
            let origin_host = normalized.origin_host.clone();
            let query = query.to_string();
            let roots = roots.clone();
            let hop_count = hop_count_for(&service.local.host_id, &target, normalized.hop_count)?;
            futures.push((target.clone(), async move {
                let result: Result<(Vec<SearchHit>, Vec<PerHostStatus>, bool, bool)> =
                    if target == service.local.host_id {
                        let outcome = service
                            .local
                            .find_paths_bounded(&query, limit, roots.clone())
                            .await?;
                        Ok((
                            outcome.hits,
                            vec![PerHostStatus {
                                host_id: target.clone(),
                                ok: true,
                                error: None,
                            }],
                            false,
                            outcome.truncated,
                        ))
                    } else {
                        let peer = service
                            .peer_config(&target)
                            .ok_or_else(|| anyhow!("unknown peer {}", target))?;
                        let value = service
                            .call_remote(
                                &peer.routable_url,
                                "find_paths",
                                json!({
                                    "query": query,
                                    "hosts": ["local"],
                                    "request_id": request_id,
                                    "origin_host": origin_host,
                                    "hop_count": hop_count,
                                    "limit": limit,
                                    "roots": roots,
                                }),
                            )
                            .await?;
                        let response = decode_tool_result::<SearchResponse<SearchHit>>(value)?;
                        let remote_status = if response.host_status.is_empty() {
                            vec![PerHostStatus {
                                host_id: target.clone(),
                                ok: !response.partial,
                                error: response
                                    .partial
                                    .then(|| "remote response was partial".to_string()),
                            }]
                        } else {
                            response.host_status.clone()
                        };
                        Ok((
                            response.results,
                            remote_status,
                            response.partial,
                            response.truncated,
                        ))
                    };
                match result {
                    Ok(value) => Ok(value),
                    Err(err) => Ok((
                        Vec::new(),
                        vec![PerHostStatus {
                            host_id: target_for_error,
                            ok: false,
                            error: Some(err.to_string()),
                        }],
                        true,
                        false,
                    )),
                }
            }));
        }
        self.join_hosts(futures).await
    }

    async fn remote_status(
        &self,
        target: &str,
        normalized: &NormalizedRequest,
    ) -> Result<StatusResponse> {
        let peer = self
            .peer_config(target)
            .ok_or_else(|| anyhow!("unknown peer {}", target))?;
        let value = self
            .call_remote(
                &peer.routable_url,
                "search_status",
                json!({
                    "hosts": ["local"],
                    "request_id": normalized.request_id,
                    "origin_host": normalized.origin_host,
                    "hop_count": 1u8,
                }),
            )
            .await?;
        decode_tool_result::<StatusResponse>(value)
    }

    async fn join_hosts<F>(
        &self,
        futures: Vec<(String, F)>,
    ) -> Result<(Vec<SearchHit>, Vec<PerHostStatus>, bool, bool)>
    where
        F: std::future::Future<Output = Result<(Vec<SearchHit>, Vec<PerHostStatus>, bool, bool)>>
            + Send,
    {
        let mut pending = FuturesUnordered::new();
        let mut pending_hosts = BTreeSet::new();
        for (host_id, future) in futures {
            pending_hosts.insert(host_id.clone());
            pending.push(async move { (host_id, future.await) });
        }
        let mut merged = Vec::new();
        let mut statuses = Vec::new();
        let mut partial = false;
        let mut truncated = false;
        let deadline = Instant::now() + Duration::from_millis(self.local.limits.overall_timeout_ms);
        while !pending_hosts.is_empty() {
            let remaining = deadline.saturating_duration_since(Instant::now());
            if remaining.is_zero() {
                partial = true;
                for host_id in pending_hosts {
                    statuses.push(PerHostStatus {
                        host_id,
                        ok: false,
                        error: Some("overall timeout".to_string()),
                    });
                }
                break;
            }
            match timeout(remaining, pending.next()).await {
                Ok(Some((host_id, result))) => {
                    pending_hosts.remove(&host_id);
                    match result {
                        Ok((mut items, mut host_status, host_partial, host_truncated)) => {
                            partial |= host_partial || host_status.iter().any(|status| !status.ok);
                            truncated |= host_truncated;
                            merged.append(&mut items);
                            if host_status.is_empty() {
                                host_status.push(PerHostStatus {
                                    host_id,
                                    ok: !host_partial,
                                    error: host_partial
                                        .then(|| "host returned a partial result".to_string()),
                                });
                            }
                            statuses.append(&mut host_status);
                        }
                        Err(err) => {
                            partial = true;
                            statuses.push(PerHostStatus {
                                host_id,
                                ok: false,
                                error: Some(err.to_string()),
                            });
                        }
                    }
                }
                Ok(None) => break,
                Err(_) => {
                    partial = true;
                    for host_id in pending_hosts {
                        statuses.push(PerHostStatus {
                            host_id,
                            ok: false,
                            error: Some("overall timeout".to_string()),
                        });
                    }
                    break;
                }
            }
        }
        Ok((merged, statuses, partial, truncated))
    }

    async fn call_remote(&self, url: &str, tool: &str, arguments: Value) -> Result<Value> {
        let payload = json!({
            "jsonrpc": "2.0",
            "id": 1,
            "method": "tools/call",
            "params": {"name": tool, "arguments": arguments},
        });
        let max_response_bytes = self.local.limits.max_response_bytes;
        let value = timeout(
            Duration::from_millis(self.local.limits.peer_timeout_ms),
            async {
                let mut request = self
                    .client
                    .post(url)
                    .header("Accept", "application/json, text/event-stream")
                    .header("MCP-Protocol-Version", "2026-07-28")
                    .header("Mcp-Method", "tools/call")
                    .header("Mcp-Name", tool);
                if let Some(token) = self.peer_auth_token.as_deref() {
                    request = request.bearer_auth(token);
                }
                let resp = request
                    .json(&payload)
                    .send()
                    .await
                    .context("send remote request")?;
                if !resp.status().is_success() {
                    return Err(anyhow!("remote HTTP status {}", resp.status()));
                }
                let mut stream = resp.bytes_stream();
                let mut body = Vec::new();
                while let Some(chunk) = stream.next().await {
                    let chunk = chunk.context("read remote response")?;
                    if max_response_bytes != 0
                        && body.len().saturating_add(chunk.len()) > max_response_bytes
                    {
                        return Err(anyhow!(
                            "remote response exceeds max_response_bytes ({max_response_bytes})"
                        ));
                    }
                    body.extend_from_slice(&chunk);
                }
                serde_json::from_slice::<Value>(&body).context("parse remote response")
            },
        )
        .await??;
        if let Some(err) = value.get("error") {
            return Err(anyhow!("remote error: {}", err));
        }
        Ok(value.get("result").cloned().unwrap_or(value))
    }

    #[allow(clippy::too_many_arguments)]
    fn wrap_result(
        &self,
        request_id: String,
        origin_host: String,
        hop_count: u8,
        partial: bool,
        truncated: bool,
        host_status: Vec<PerHostStatus>,
        data: Value,
    ) -> ToolResult {
        ToolResult {
            request_id,
            origin_host,
            hop_count,
            host_id: self.local.host_id.clone(),
            partial,
            truncated,
            data,
            host_status,
        }
    }
}

fn unique_hosts(hosts: &[String]) -> Vec<String> {
    let mut seen = BTreeSet::new();
    let mut out = Vec::new();
    for host in hosts {
        if seen.insert(host.clone()) {
            out.push(host.clone());
        }
    }
    out
}

fn hop_count_for(local: &str, target: &str, base: u8) -> Result<u8> {
    let hop = if target == local { base } else { base + 1 };
    if hop > 1 {
        Err(anyhow!("hop_count above one is rejected"))
    } else {
        Ok(hop)
    }
}

pub fn decode_tool_result<T: for<'de> Deserialize<'de>>(value: Value) -> Result<T> {
    if let Some(content) = value.get("content").and_then(Value::as_array) {
        if let Some(first) = content.first() {
            if let Some(text) = first.get("text").and_then(Value::as_str) {
                return Ok(serde_json::from_str(text)?);
            }
        }
    }
    Ok(serde_json::from_value(value)?)
}
