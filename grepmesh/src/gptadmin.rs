use crate::topology_cache::{ProviderSnapshot, TopologyNode, TopologySnapshot};
use anyhow::{anyhow, Context, Result};
use serde::Deserialize;
use std::{
    env,
    path::Path,
    time::{SystemTime, UNIX_EPOCH},
};

#[derive(Debug, Clone)]
pub struct GptAdminTopologyClient {
    endpoint: String,
    token: Option<String>,
    local_host_id: String,
    ttl_ms: u64,
    client: reqwest::Client,
}

#[derive(Debug, Deserialize)]
struct TopologyResponse {
    nodes: Vec<TopologyNodeWire>,
}

#[derive(Debug, Deserialize)]
struct TopologyNodeWire {
    host_id: String,
    #[serde(default)]
    local_url: Option<String>,
    #[serde(alias = "advertise_url", alias = "routable_url", alias = "endpoint")]
    #[serde(default)]
    peer_url: Option<String>,
    #[serde(default)]
    capabilities: Vec<String>,
    #[serde(default)]
    roots: Vec<String>,
    #[serde(default)]
    generation: u64,
}

impl GptAdminTopologyClient {
    pub fn from_env(
        endpoint: impl Into<String>,
        local_host_id: impl Into<String>,
        token_env: Option<&str>,
        ttl_ms: u64,
    ) -> Self {
        let token = token_env
            .and_then(|name| env::var(name).ok())
            .filter(|value| !value.trim().is_empty());
        Self {
            endpoint: endpoint.into(),
            token,
            local_host_id: local_host_id.into(),
            ttl_ms,
            client: reqwest::Client::new(),
        }
    }

    pub async fn fetch(&self) -> Result<ProviderSnapshot> {
        let mut request = self.client.get(&self.endpoint);
        if let Some(token) = &self.token {
            request = request.bearer_auth(token);
        }
        let body = request
            .send()
            .await
            .with_context(|| format!("fetch GPTAdmin topology {}", self.endpoint))?
            .error_for_status()
            .context("GPTAdmin topology returned an error")?
            .json::<TopologyResponse>()
            .await
            .context("decode GPTAdmin topology")?;
        parse_topology_response(body, &self.local_host_id, now_ms(), self.ttl_ms)
    }

    pub async fn refresh_cache(
        &self,
        current: &TopologySnapshot,
        cache_path: Option<&Path>,
    ) -> Result<TopologySnapshot> {
        let provider = self.fetch().await?;
        let merged = current.merge_fresh_provider_result(provider)?;
        if let Some(path) = cache_path {
            merged.save_atomic(path)?;
        }
        Ok(merged)
    }
}

fn parse_topology_response(
    response: TopologyResponse,
    local_host_id: &str,
    fetched_at_ms: u64,
    ttl_ms: u64,
) -> Result<ProviderSnapshot> {
    let generation = response
        .nodes
        .iter()
        .map(|node| node.generation)
        .max()
        .unwrap_or(1)
        .max(1);
    let peers = response
        .nodes
        .into_iter()
        .filter(|node| node.host_id != local_host_id)
        .map(|node| -> Result<TopologyNode> {
            Ok(TopologyNode {
                host_id: node.host_id,
                local_url: node
                    .local_url
                    .clone()
                    .or_else(|| node.peer_url.clone())
                    .ok_or_else(|| anyhow!("topology node has no local_url or peer_url"))?,
                routable_url: node
                    .peer_url
                    .or_else(|| node.local_url.clone())
                    .ok_or_else(|| anyhow!("topology node has no peer_url or local_url"))?,
                capabilities: node.capabilities,
                roots: node.roots,
                generation: node.generation.max(generation),
                fetched_at_ms,
                expires_at_ms: fetched_at_ms.saturating_add(ttl_ms),
                last_refresh_error: None,
            })
        })
        .collect::<Result<Vec<_>>>()?;
    let result = ProviderSnapshot {
        local_host_id: local_host_id.to_string(),
        generation,
        fetched_at_ms,
        ttl_ms,
        peers,
    };
    result.validate()?;
    Ok(result)
}

fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as u64
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn topology_response_excludes_local_and_preserves_peer_url() {
        let response: TopologyResponse = serde_json::from_value(json!({
            "nodes": [
                {"host_id":"A","local_url":"http://127.0.0.1:9419/mcp","peer_url":"https://203.0.113.10:9419/mcp","generation":4},
                {"host_id":"B","local_url":"http://127.0.0.1:9419/mcp","advertise_url":"https://203.0.113.10:9419/mcp","generation":5,"roots":["home"]}
            ]
        })).unwrap();
        let result = parse_topology_response(response, "A", 1_700_000_000_000, 30_000).unwrap();
        assert_eq!(result.peers.len(), 1);
        assert_eq!(result.peers[0].host_id, "B");
        assert_eq!(
            result.peers[0].routable_url,
            "https://203.0.113.10:9419/mcp"
        );
    }

    #[test]
    fn endpoint_only_discovery_record_is_usable_for_both_urls() {
        let response: TopologyResponse = serde_json::from_value(json!({
            "nodes": [{
                "host_id": "B",
                "endpoint": "https://203.0.113.10:9419/mcp",
                "generation": 5,
                "capabilities": ["search_text"],
                "roots": ["home"]
            }]
        }))
        .unwrap();
        let result = parse_topology_response(response, "A", 1_700_000_000_000, 30_000).unwrap();
        assert_eq!(result.peers[0].local_url, result.peers[0].routable_url);
        assert_eq!(
            result.peers[0].routable_url,
            "https://203.0.113.10:9419/mcp"
        );
    }

    #[tokio::test]
    async fn fetch_reads_only_the_control_plane_topology_projection() {
        let app = axum::Router::new().route(
            "/mcp-relay/grepmesh",
            axum::routing::get(|| async {
                axum::Json(serde_json::json!({
                    "nodes": [{
                        "host_id": "B",
                        "local_url": "http://127.0.0.1:9419/mcp",
                        "peer_url": "http://127.0.0.1:29419/mcp",
                        "generation": 9
                    }]
                }))
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });

        let client = GptAdminTopologyClient::from_env(
            format!("http://{address}/mcp-relay/grepmesh"),
            "A",
            None,
            30_000,
        );
        let result = client.fetch().await.unwrap();
        assert_eq!(result.peers[0].host_id, "B");
        assert_eq!(result.peers[0].routable_url, "http://127.0.0.1:29419/mcp");
    }
}
