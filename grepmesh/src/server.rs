use crate::{
    backend::LocalBackend,
    config::{AppConfig, RuntimeSettings},
    gptadmin::GptAdminTopologyClient,
    mcp::MeshService,
    topology::Topology,
    topology_cache::TopologySnapshot,
};
use anyhow::{anyhow, Context, Result};
use axum::{
    extract::State,
    http::{header, HeaderMap, StatusCode, Uri},
    response::{Html, IntoResponse, Response},
    routing::{get, post},
    Json, Router,
};
use serde_json::{json, Value};
use std::{
    env, fs,
    path::PathBuf,
    sync::{Arc, RwLock},
    time::Duration,
};

const DEFAULT_PROTOCOL_VERSION: &str = "2025-06-18";
const CURRENT_PROTOCOL_VERSION: &str = "2026-07-28";

#[derive(Clone)]
struct AppState {
    service: Arc<RwLock<Arc<MeshService>>>,
    runtime: Arc<RuntimeState>,
    peer_auth_token: Option<String>,
    require_peer_auth: bool,
}

struct RuntimeState {
    host_id: String,
    root: PathBuf,
    index_path: Option<PathBuf>,
    settings_path: PathBuf,
    settings: tokio::sync::RwLock<RuntimeSettings>,
}

pub async fn run_server(config: AppConfig) -> Result<()> {
    let settings_path = config
        .topology_cache_path
        .as_ref()
        .map(|path| path.with_file_name("runtime-settings.json"))
        .unwrap_or_else(|| PathBuf::from("/var/lib/grepmesh-mcp/runtime-settings.json"));
    let mut settings = RuntimeSettings::from_config(&config);
    if settings_path.exists() {
        settings = serde_json::from_slice(&fs::read(&settings_path)?)
            .context("parse persisted GrepMesh runtime settings")?;
    }
    settings.validate()?;
    let cached_snapshot =
        config
            .topology_cache_path
            .as_ref()
            .and_then(|path| match TopologySnapshot::load(path) {
                Ok(snapshot) => Some(snapshot),
                Err(err) => {
                    tracing::warn!(error = %err, "cannot load GrepMesh topology cache");
                    None
                }
            });
    let topology_client = config.gptadmin_topology_url.clone().map(|endpoint| {
        let token_env = config
            .gptadmin_token_env
            .as_deref()
            .or(Some("GPTADMIN_GREPMESH_TOKEN"));
        GptAdminTopologyClient::from_env(
            endpoint,
            config.host_id.clone(),
            token_env,
            config.topology_ttl_ms,
        )
    });
    let topology = if let Some(client) = topology_client.as_ref() {
        let current = cached_snapshot
            .clone()
            .unwrap_or_else(|| TopologySnapshot::empty(config.host_id.clone()));
        match client
            .refresh_cache(&current, config.topology_cache_path.as_deref())
            .await
        {
            Ok(snapshot) => Topology::from_snapshot(snapshot, now_ms()).unwrap_or_else(|err| {
                Topology::new(config.host_id.clone(), config.peers.clone())
                    .with_cache_error(err.to_string())
            }),
            Err(err) => {
                tracing::warn!(error = %err, "GPTAdmin topology refresh failed; using cache/static peers");
                cached_snapshot
                    .map(|snapshot| {
                        Topology::from_snapshot(snapshot, now_ms()).unwrap_or_else(|inner| {
                            Topology::new(config.host_id.clone(), config.peers.clone())
                                .with_cache_error(inner.to_string())
                        })
                    })
                    .unwrap_or_else(|| {
                        Topology::new(config.host_id.clone(), config.peers.clone())
                            .with_cache_error(err.to_string())
                    })
            }
        }
    } else if let Some(snapshot) = cached_snapshot {
        Topology::from_snapshot(snapshot, now_ms()).unwrap_or_else(|err| {
            Topology::new(config.host_id.clone(), config.peers.clone())
                .with_cache_error(err.to_string())
        })
    } else {
        Topology::new(config.host_id.clone(), config.peers.clone())
    };
    let local = LocalBackend::from_config(
        config.host_id.clone(),
        config.root.clone(),
        settings.limits.clone(),
        settings.roots.clone(),
        settings.exclude_globs.clone(),
        config.index_path.clone(),
    )
    .with_unrestricted_roots(settings.unrestricted_roots);
    let peer_auth_token = config
        .peer_auth_token_env
        .as_deref()
        .map(env::var)
        .transpose()?
        .filter(|token| !token.trim().is_empty());
    let remote_bind = config.bind;
    let local_bind = config.local_bind;
    let require_peer_auth = !remote_bind.ip().is_loopback();
    if require_peer_auth && local_bind.is_none() {
        return Err(anyhow!(
            "non-loopback bind requires a separate local_bind for the agent entrypoint"
        ));
    }
    if require_peer_auth && peer_auth_token.is_none() {
        return Err(anyhow!(
            "non-loopback bind requires a non-empty peer_auth_token_env"
        ));
    }
    let service = Arc::new(RwLock::new(Arc::new(
        MeshService::new(local, topology).with_peer_auth_token(peer_auth_token.clone()),
    )));
    let runtime = Arc::new(RuntimeState {
        host_id: config.host_id.clone(),
        root: config.root.clone(),
        index_path: config.index_path.clone(),
        settings_path,
        settings: tokio::sync::RwLock::new(settings),
    });
    if let Some(client) = topology_client {
        let refresh_service = Arc::clone(&service);
        let cache_path = config.topology_cache_path.clone();
        let host_id = config.host_id.clone();
        let refresh_ms = config.topology_ttl_ms.max(1_000);
        tokio::spawn(async move {
            let mut interval = tokio::time::interval(Duration::from_millis(refresh_ms));
            interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
            interval.tick().await;
            loop {
                interval.tick().await;
                let current = cache_path
                    .as_ref()
                    .and_then(|path| TopologySnapshot::load(path).ok())
                    .unwrap_or_else(|| TopologySnapshot::empty(host_id.clone()));
                match client.refresh_cache(&current, cache_path.as_deref()).await {
                    Ok(snapshot) => match Topology::from_snapshot(snapshot, now_ms()) {
                        Ok(next) => {
                            if let Ok(service) = refresh_service.read() {
                                service.replace_topology(next)
                            }
                        }
                        Err(err) => {
                            tracing::warn!(error = %err, "invalid refreshed GrepMesh topology")
                        }
                    },
                    Err(err) => {
                        tracing::warn!(error = %err, "periodic GPTAdmin topology refresh failed");
                        if let Ok(current) = current_topology(&refresh_service) {
                            if let Ok(service) = refresh_service.read() {
                                service.replace_topology(current.with_cache_error(err.to_string()));
                            }
                        }
                    }
                }
            }
        });
    }
    let remote_app = build_app(
        AppState {
            service: Arc::clone(&service),
            runtime: Arc::clone(&runtime),
            peer_auth_token: peer_auth_token.clone(),
            require_peer_auth,
        },
        false,
    );
    let listener = tokio::net::TcpListener::bind(remote_bind).await?;
    if let Some(local_bind) = local_bind.filter(|bind| *bind != remote_bind) {
        let local_listener = tokio::net::TcpListener::bind(local_bind).await?;
        let local_app = build_app(
            AppState {
                service,
                runtime,
                peer_auth_token,
                require_peer_auth: false,
            },
            true,
        );
        tokio::try_join!(
            axum::serve(listener, remote_app),
            axum::serve(local_listener, local_app)
        )?;
    } else {
        axum::serve(listener, remote_app).await?;
    }
    Ok(())
}

fn build_app(state: AppState, admin_enabled: bool) -> Router {
    let app = Router::new()
        .route("/", get(health).post(handle_rpc))
        .route("/mcp", post(handle_rpc));
    let app = if admin_enabled {
        app.route("/admin", get(admin_page))
            .route("/admin/settings", get(get_settings).put(put_settings))
    } else {
        app
    };
    app.with_state(state)
}

async fn get_settings(State(state): State<AppState>) -> Json<RuntimeSettings> {
    Json(state.runtime.settings.read().await.clone())
}

async fn put_settings(
    State(state): State<AppState>,
    Json(next): Json<RuntimeSettings>,
) -> Response {
    if let Err(err) = next.validate() {
        return (
            StatusCode::BAD_REQUEST,
            Json(json!({"ok": false, "error": err.to_string()})),
        )
            .into_response();
    }
    let bytes = match serde_json::to_vec_pretty(&next) {
        Ok(bytes) => bytes,
        Err(err) => {
            return (
                StatusCode::BAD_REQUEST,
                Json(json!({"ok": false, "error": err.to_string()})),
            )
                .into_response()
        }
    };
    let temp_path = state.runtime.settings_path.with_extension("json.tmp");
    if let Err(err) = fs::write(&temp_path, bytes)
        .and_then(|_| fs::rename(&temp_path, &state.runtime.settings_path))
    {
        let _ = fs::remove_file(&temp_path);
        return (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(json!({"ok": false, "error": format!("persist runtime settings: {err}")})),
        )
            .into_response();
    }
    let topology = match current_topology(&state.service) {
        Ok(topology) => topology,
        Err(_) => {
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(json!({"ok": false, "error": "GrepMesh service lock poisoned"})),
            )
                .into_response()
        }
    };
    let local = LocalBackend::from_config(
        state.runtime.host_id.clone(),
        state.runtime.root.clone(),
        next.limits.clone(),
        next.roots.clone(),
        next.exclude_globs.clone(),
        state.runtime.index_path.clone(),
    )
    .with_unrestricted_roots(next.unrestricted_roots);
    let replacement = Arc::new(
        MeshService::new(local, topology).with_peer_auth_token(state.peer_auth_token.clone()),
    );
    match state.service.write() {
        Ok(mut service) => *service = replacement,
        Err(_) => {
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(json!({"ok": false, "error": "GrepMesh service lock poisoned"})),
            )
                .into_response()
        }
    }
    *state.runtime.settings.write().await = next;
    (
        StatusCode::OK,
        Json(json!({"ok": true, "restart_required": false})),
    )
        .into_response()
}

fn current_topology(service: &Arc<RwLock<Arc<MeshService>>>) -> Result<Topology> {
    let service = service
        .read()
        .map_err(|_| anyhow!("GrepMesh service lock poisoned"))?;
    service
        .topology
        .read()
        .map(|topology| topology.clone())
        .map_err(|_| anyhow!("GrepMesh topology lock poisoned"))
}

async fn admin_page() -> Html<&'static str> {
    Html(
        r#"<!doctype html><meta charset=utf-8><title>GrepMesh settings</title>
<style>body{font:16px system-ui;max-width:820px;margin:3rem auto;padding:0 1rem}textarea{width:100%;height:28rem;font:13px ui-monospace}button{padding:.6rem 1rem}#status{margin-left:1rem}.warn{padding:1rem;background:#fff3cd}</style>
<h1>GrepMesh runtime settings</h1><p class=warn><b>Warning:</b> “unrestricted roots” lets the service search any readable absolute path. It can expose secrets if you remove exclude globs or grant the service access to secret-bearing directories.</p>
<p>Changes apply immediately, persist in the GrepMesh state directory, and survive restart. This page is intentionally available only on the loopback listener.</p>
<textarea id=settings spellcheck=false></textarea><p><button id=save>Apply runtime settings</button><span id=status></span></p>
<script>const box=document.querySelector('#settings'),status=document.querySelector('#status');fetch('/admin/settings').then(r=>r.json()).then(v=>box.value=JSON.stringify(v,null,2)).catch(e=>status.textContent=e);document.querySelector('#save').onclick=async()=>{let body;try{body=JSON.parse(box.value)}catch(e){status.textContent='Invalid JSON: '+e;return}const r=await fetch('/admin/settings',{method:'PUT',headers:{'content-type':'application/json'},body:JSON.stringify(body)}),v=await r.json();status.textContent=v.ok?'Applied immediately.':(v.error||'Failed')};</script>"#,
    )
}

async fn health(State(state): State<AppState>, headers: HeaderMap) -> Response {
    if state.require_peer_auth
        && validate_peer_auth(&headers, state.peer_auth_token.as_deref()).is_err()
    {
        return (
            StatusCode::UNAUTHORIZED,
            Json(json!({"ok": false, "error": "peer authentication failed"})),
        )
            .into_response();
    }
    (StatusCode::OK, Json(json!({"ok": true}))).into_response()
}

fn now_ms() -> u64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as u64
}

async fn handle_rpc(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(payload): Json<Value>,
) -> Response {
    let id = payload.get("id").cloned().unwrap_or(Value::Null);
    if state.require_peer_auth
        && validate_peer_auth(&headers, state.peer_auth_token.as_deref()).is_err()
    {
        return (
            StatusCode::UNAUTHORIZED,
            Json(json!({
                "jsonrpc": "2.0",
                "error": {"code": -32003, "message": "peer authentication failed"},
                "id": id,
            })),
        )
            .into_response();
    }
    if let Err(err) = validate_origin(&headers) {
        return (
            StatusCode::FORBIDDEN,
            Json(json!({
                "jsonrpc": "2.0",
                "error": {"code": -32001, "message": err.to_string()},
                "id": id,
            })),
        )
            .into_response();
    }
    if let Err(err) = validate_transport_headers(&headers, &payload) {
        return (
            StatusCode::BAD_REQUEST,
            Json(json!({
                "jsonrpc": "2.0",
                "error": {"code": -32020, "message": err.to_string()},
                "id": id,
            })),
        )
            .into_response();
    }
    let is_notification = payload.get("id").is_none();
    let response = match handle_rpc_inner(state, payload).await {
        Ok(v) => v,
        Err(err) => {
            json!({"jsonrpc":"2.0","error":{"code":-32000,"message":err.to_string()},"id":id})
        }
    };
    if is_notification {
        StatusCode::ACCEPTED.into_response()
    } else {
        (StatusCode::OK, Json(response)).into_response()
    }
}

fn validate_peer_auth(headers: &HeaderMap, expected: Option<&str>) -> anyhow::Result<()> {
    let expected = expected
        .filter(|token| !token.trim().is_empty())
        .ok_or_else(|| anyhow!("peer authentication is not configured"))?;
    let provided = headers
        .get(header::AUTHORIZATION)
        .ok_or_else(|| anyhow!("missing Authorization header"))?
        .to_str()
        .map_err(|_| anyhow!("invalid Authorization header"))?;
    let expected_header = format!("Bearer {expected}");
    if provided != expected_header {
        return Err(anyhow!("invalid peer bearer token"));
    }
    Ok(())
}

async fn handle_rpc_inner(state: AppState, payload: Value) -> Result<Value> {
    let id = payload.get("id").cloned().unwrap_or(Value::Null);
    let method = payload
        .get("method")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let params = payload.get("params").cloned().unwrap_or(Value::Null);
    let result = match method {
        "initialize" => {
            let protocol_version = negotiate_protocol_version(&params);
            json!({
                "protocolVersion": protocol_version,
                "serverInfo": {"name": "grepmesh", "version": "0.1.0"},
                "capabilities": {"tools": {"listChanged": false}},
                "instructions": "Use GrepMesh when the location is unknown, cross-host search is needed, or configured mesh scopes matter. When a concrete local checkout/path is already known, prefer rg: its per-line output is usually materially more context-efficient. Start with search_text or find_paths, then read_text for the exact file. read_text returns stable N:hhhh line_id values compatible with GPTAdmin file_editor batch edits; reuse those ids instead of re-reading unchanged ranges.",
            })
        }
        "tools/list" => json!({
            "tools": [
                tool_meta("search_text", "Search text across one or more hosts."),
                tool_meta("find_paths", "Find file paths across one or more hosts."),
                tool_meta("read_text", "Read a text file from a specific host. Each returned line includes an N:hhhh line_id bound to its content; pass these ids to GPTAdmin file_editor batch_edit and reuse fresh ids from edit results instead of re-reading unchanged ranges."),
                tool_meta("search_status", "Report search/status metadata for one or more hosts."),
            ]
        }),
        "tools/call" => {
            let service = state
                .service
                .read()
                .map_err(|_| anyhow!("GrepMesh service lock poisoned"))?
                .clone();
            call_tool(service.as_ref(), params).await?
        }
        _ => {
            return Ok(
                json!({"jsonrpc":"2.0","error":{"code":-32601,"message":"method not found"},"id":id}),
            )
        }
    };
    Ok(json!({"jsonrpc":"2.0","result": result, "id": id}))
}

fn negotiate_protocol_version(params: &Value) -> String {
    match params.get("protocolVersion").and_then(Value::as_str) {
        Some(CURRENT_PROTOCOL_VERSION) => CURRENT_PROTOCOL_VERSION.to_string(),
        Some("2025-11-25") => "2025-11-25".to_string(),
        Some("2025-06-18") => "2025-06-18".to_string(),
        Some("2025-03-26") => "2025-03-26".to_string(),
        Some("2024-11-05") => "2024-11-05".to_string(),
        _ => DEFAULT_PROTOCOL_VERSION.to_string(),
    }
}

fn tool_meta(name: &str, description: &str) -> Value {
    let hosts = json!({
        "anyOf": [
            {"type": "string", "enum": ["local", "*"]},
            {"type": "array", "items": {"type": "string"}}
        ]
    });
    let schema = match name {
        "search_text" => json!({
            "type": "object",
            "required": ["query"],
            "properties": {
                "query": {"type": "string"}, "hosts": hosts,
                "roots": {"type": "array", "items": {"type": "string"}},
                "mode": {"type": "string", "enum": ["literal", "regex", "case_insensitive_literal"]},
                "path_globs": {"type": "array", "items": {"type": "string"}},
                "context_lines": {"type": "integer", "minimum": 0},
                "max_matches": {"type": "integer", "minimum": 1}
            }
        }),
        "find_paths" => json!({
            "type": "object",
            "required": ["pattern"],
            "properties": {
                "pattern": {"type": "string"}, "hosts": hosts,
                "roots": {"type": "array", "items": {"type": "string"}},
                "max_matches": {"type": "integer", "minimum": 1}
            }
        }),
        "read_text" => json!({
            "type": "object",
            "required": ["host", "path"],
            "properties": {
                "host": {"type": "string"}, "path": {"type": "string"},
                "start_line": {"type": "integer", "minimum": 1},
                "end_line": {"type": "integer", "minimum": 1}
            }
        }),
        _ => json!({"type": "object", "properties": {"hosts": hosts}}),
    };
    json!({
        "name": name,
        "description": description,
        "inputSchema": schema
    })
}

pub async fn call_tool(service: &MeshService, params: Value) -> Result<Value> {
    let name = params
        .get("name")
        .and_then(Value::as_str)
        .ok_or_else(|| anyhow::anyhow!("missing tool name"))?;
    let arguments = params.get("arguments").cloned().unwrap_or(Value::Null);
    let tool_result = match name {
        "search_text" => {
            service
                .call_search(serde_json::from_value(arguments)?)
                .await?
        }
        "find_paths" => {
            service
                .call_find_paths(serde_json::from_value(arguments)?)
                .await?
        }
        "read_text" => {
            service
                .call_read_text(serde_json::from_value(arguments)?)
                .await?
        }
        "search_status" => {
            service
                .call_status(serde_json::from_value(arguments)?)
                .await?
        }
        other => return Err(anyhow::anyhow!("unknown tool {}", other)),
    };
    let bounded = bound_tool_data(tool_result.data, service.local.limits.max_response_bytes)?;
    Ok(json!({
        "content": [{"type": "text", "text": serde_json::to_string(&bounded)?}],
        "isError": false
    }))
}

fn validate_transport_headers(headers: &HeaderMap, payload: &Value) -> anyhow::Result<()> {
    let Some(version) = headers.get("mcp-protocol-version") else {
        return Ok(());
    };
    let version = version
        .to_str()
        .map_err(|_| anyhow::anyhow!("invalid MCP-Protocol-Version header"))?;
    match version {
        "2024-11-05" | "2025-03-26" | "2025-06-18" | "2025-11-25" | "2026-07-28" => {}
        _ => {
            return Err(anyhow::anyhow!(
                "unsupported MCP protocol version {version}"
            ))
        }
    }
    if let Some(accept) = headers.get("accept") {
        let accept = accept
            .to_str()
            .map_err(|_| anyhow::anyhow!("invalid Accept header"))?;
        if !accept.contains("application/json") && !accept.contains("text/event-stream") {
            return Err(anyhow::anyhow!(
                "Accept must include application/json or text/event-stream"
            ));
        }
    }
    if version == "2026-07-28" {
        let method = payload
            .get("method")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let mirrored_method = headers
            .get("mcp-method")
            .and_then(|value| value.to_str().ok())
            .ok_or_else(|| anyhow::anyhow!("Mcp-Method header is required"))?;
        if mirrored_method != method {
            return Err(anyhow::anyhow!(
                "Mcp-Method header does not match request method"
            ));
        }
        if method == "tools/call" {
            let name = payload
                .get("params")
                .and_then(|params| params.get("name"))
                .and_then(Value::as_str)
                .unwrap_or_default();
            let mirrored_name = headers
                .get("mcp-name")
                .and_then(|value| value.to_str().ok())
                .ok_or_else(|| anyhow::anyhow!("Mcp-Name header is required for tools/call"))?;
            if mirrored_name != name {
                return Err(anyhow::anyhow!("Mcp-Name header does not match tool name"));
            }
        }
    }
    Ok(())
}

fn validate_origin(headers: &HeaderMap) -> anyhow::Result<()> {
    let Some(origin) = headers.get("origin") else {
        return Ok(());
    };
    let origin = origin
        .to_str()
        .map_err(|_| anyhow::anyhow!("invalid Origin header"))?;
    let uri: Uri = origin
        .parse()
        .map_err(|_| anyhow::anyhow!("invalid Origin header"))?;
    match uri.host() {
        Some("localhost") | Some("127.0.0.1") | Some("[::1]") | Some("::1") => Ok(()),
        _ => Err(anyhow::anyhow!("Origin is not an allowed local origin")),
    }
}

fn bound_tool_data(mut data: Value, max_bytes: usize) -> anyhow::Result<Value> {
    if max_bytes == 0 || serde_json::to_vec(&data)?.len() <= max_bytes {
        return Ok(data);
    }
    if let Some(object) = data.as_object_mut() {
        object.insert("truncated".into(), Value::Bool(true));
    }
    loop {
        if serde_json::to_vec(&data)?.len() <= max_bytes {
            return Ok(data);
        }
        let mut removed = false;
        if let Some(object) = data.as_object_mut() {
            for key in ["matches", "results", "paths"] {
                if let Some(items) = object.get_mut(key).and_then(Value::as_array_mut) {
                    removed |= items.pop().is_some();
                }
            }
            if !removed {
                if let Some(chunks) = object.get_mut("chunks").and_then(Value::as_array_mut) {
                    if let Some(last) = chunks.last_mut() {
                        if let Some(lines) = last.get_mut("lines").and_then(Value::as_array_mut) {
                            removed |= lines.pop().is_some();
                        }
                        if !removed {
                            removed |= chunks.pop().is_some();
                        }
                    }
                }
            }
        }
        if !removed {
            return Err(anyhow::anyhow!(
                "tool response exceeds max_response_bytes ({max_bytes})"
            ));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn protocol_negotiation_prefers_client_supported_version() {
        assert_eq!(
            negotiate_protocol_version(&json!({"protocolVersion": "2025-06-18"})),
            "2025-06-18"
        );
        assert_eq!(
            negotiate_protocol_version(&json!({"protocolVersion": "2025-03-26"})),
            "2025-03-26"
        );
    }

    #[test]
    fn protocol_negotiation_keeps_current_version_when_explicit() {
        assert_eq!(
            negotiate_protocol_version(&json!({"protocolVersion": "2026-07-28"})),
            "2026-07-28"
        );
        assert_eq!(
            negotiate_protocol_version(&json!({})),
            DEFAULT_PROTOCOL_VERSION
        );
    }

    #[tokio::test]
    async fn initialize_routes_known_local_searches_to_rg() {
        let temp = tempfile::tempdir().unwrap();
        let local = LocalBackend::new("local", temp.path(), Default::default());
        let service = Arc::new(RwLock::new(Arc::new(MeshService::new(
            local,
            Topology::new("local", vec![]),
        ))));
        let state = AppState {
            service,
            runtime: Arc::new(RuntimeState {
                host_id: "local".to_string(),
                root: temp.path().to_path_buf(),
                index_path: None,
                settings_path: temp.path().join("runtime-settings.json"),
                settings: tokio::sync::RwLock::new(RuntimeSettings {
                    roots: Default::default(),
                    exclude_globs: vec![],
                    limits: Default::default(),
                    unrestricted_roots: false,
                }),
            }),
            peer_auth_token: None,
            require_peer_auth: false,
        };
        let response = handle_rpc_inner(
            state,
            json!({"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}),
        )
        .await
        .unwrap();
        let instructions = response["result"]["instructions"].as_str().unwrap();
        assert!(instructions.contains("location is unknown"));
        assert!(instructions.contains("prefer rg"));
        assert!(instructions.contains("context-efficient"));
        assert!(instructions.contains("search_text"));
        assert!(instructions.contains("find_paths"));
        assert!(instructions.contains("read_text"));
    }

    #[test]
    fn peer_auth_requires_exact_bearer_token() {
        let mut headers = HeaderMap::new();
        assert!(validate_peer_auth(&headers, Some("secret")).is_err());
        headers.insert(header::AUTHORIZATION, "Bearer wrong".parse().unwrap());
        assert!(validate_peer_auth(&headers, Some("secret")).is_err());
        headers.insert(header::AUTHORIZATION, "Bearer secret".parse().unwrap());
        assert!(validate_peer_auth(&headers, Some("secret")).is_ok());
    }
}
