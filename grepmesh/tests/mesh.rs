use grepmesh::{
    backend::{LocalBackend, SearchMode},
    config::AppConfig,
    mcp::{normalize_request, HostsInput, MeshService, SearchArgs},
    server::call_tool,
    topology::{PeerConfig, Topology},
};
use serde_json::{json, Value};
use std::{
    fs,
    io::{Read, Write},
    net::TcpListener,
    path::PathBuf,
    process::{Child, Command, Stdio},
    time::Duration,
};
use tempfile::TempDir;

#[test]
fn normalize_wildcard_hop_and_dedup_rules() {
    let req = normalize_request(
        "A",
        vec!["A".to_string(), "B".to_string(), "C".to_string()],
        Some(HostsInput::One("*".into())),
        Some("req-1".into()),
        None,
        Some(0),
    )
    .unwrap();
    assert_eq!(req.hosts, vec!["A", "B", "C"]);

    let req = normalize_request(
        "A",
        vec!["A".to_string(), "B".to_string()],
        Some(HostsInput::Many(vec!["A".into(), "B".into(), "A".into()])),
        None,
        None,
        Some(0),
    )
    .unwrap();
    assert_eq!(req.hosts, vec!["A", "B"]);

    assert!(normalize_request(
        "A",
        vec!["A".into(), "B".into()],
        Some(HostsInput::One("*".into())),
        None,
        None,
        Some(2)
    )
    .is_err());
    assert!(normalize_request(
        "A",
        vec!["A".into(), "B".into()],
        Some(HostsInput::Many(vec!["B".into()])),
        None,
        None,
        Some(1)
    )
    .is_err());
}

#[test]
fn read_text_routes_by_host_and_path() {
    let temp = TempDir::new().unwrap();
    let root = temp.path();
    let path = root.join("note.txt");
    fs::write(&path, "hello\nworld\n").unwrap();
    let backend = grepmesh::backend::LocalBackend::new("A", root, Default::default());
    let chunks = backend.read_text(&path, Some(1), Some(2)).unwrap();
    assert_eq!(chunks[0].lines.len(), 2);
    assert_eq!(chunks[0].lines[0].text, "hello");
}

#[tokio::test]
async fn malformed_search_inputs_are_failed_partial_mcp_results() {
    let root = tempfile::tempdir().unwrap();
    fs::write(root.path().join("config.rs"), "SEARCH_INPUT_TOKEN\n").unwrap();
    let service = MeshService::new(
        LocalBackend::from_config(
            "A",
            root.path(),
            Default::default(),
            std::collections::BTreeMap::new(),
            vec![],
            None,
        ),
        Topology::new("A", vec![]),
    );

    for (request_id, query, mode, path_globs) in [
        ("invalid-regex", "(", SearchMode::Regex, vec![]),
        (
            "invalid-glob",
            "SEARCH_INPUT_TOKEN",
            SearchMode::Literal,
            vec!["[".into()],
        ),
    ] {
        let result = service
            .call_search(SearchArgs {
                query: query.into(),
                hosts: Some(HostsInput::One("local".into())),
                request_id: Some(request_id.into()),
                origin_host: None,
                hop_count: None,
                limit: Some(10),
                context_lines: Some(0),
                mode,
                path_globs,
                roots: vec![],
            })
            .await
            .unwrap();

        assert!(result.partial, "{request_id}");
        assert!(result.data["results"].as_array().unwrap().is_empty());
        let status = result
            .host_status
            .iter()
            .find(|status| status.host_id == "A")
            .unwrap();
        assert!(!status.ok, "{request_id}");
        assert!(status
            .error
            .as_deref()
            .is_some_and(|error| !error.is_empty()));
    }
}

#[cfg(target_os = "linux")]
#[tokio::test]
async fn permission_denied_search_is_partial_and_keeps_readable_match() {
    let service = MeshService::new(
        LocalBackend::from_config(
            "A",
            "/proc/1",
            Default::default(),
            std::collections::BTreeMap::new(),
            vec![],
            None,
        ),
        Topology::new("A", vec![]),
    );
    let result = service
        .call_search(SearchArgs {
            query: "Name".into(),
            hosts: Some(HostsInput::One("local".into())),
            request_id: Some("permission-partial".into()),
            origin_host: None,
            hop_count: None,
            limit: Some(10),
            context_lines: Some(0),
            mode: SearchMode::Literal,
            path_globs: vec!["**/status".into()],
            roots: vec![],
        })
        .await
        .unwrap();

    assert!(result.partial);
    assert!(result.data["results"]
        .as_array()
        .unwrap()
        .iter()
        .flat_map(|range| range["lines"].as_array().unwrap())
        .any(|line| line["text"]
            .as_str()
            .is_some_and(|text| text.contains("Name"))));
    let status = result
        .host_status
        .iter()
        .find(|status| status.host_id == "A")
        .unwrap();
    assert!(!status.ok);
    assert!(status
        .error
        .as_deref()
        .is_some_and(|error| error.contains("Permission denied")));
}

fn free_port() -> u16 {
    TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

fn write_config(path: &PathBuf, cfg: &AppConfig) {
    fs::write(path, serde_json::to_vec_pretty(cfg).unwrap()).unwrap();
}

fn spawn_server(config: &PathBuf) -> Child {
    Command::new(env!("CARGO_BIN_EXE_grepmesh-mcp"))
        .arg("--config")
        .arg(config)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .unwrap()
}

async fn wait_for_server(port: u16) {
    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    loop {
        if tokio::net::TcpStream::connect(("127.0.0.1", port))
            .await
            .is_ok()
        {
            return;
        }
        if tokio::time::Instant::now() >= deadline {
            panic!("grepmesh test server on port {port} did not become ready within 5s");
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
}

async fn rpc(url: &str, method: &str, params: serde_json::Value) -> serde_json::Value {
    let client = reqwest::Client::new();
    let mut request = client
        .post(url)
        .header("Accept", "application/json, text/event-stream")
        .header("MCP-Protocol-Version", "2026-07-28")
        .header("Mcp-Method", method);
    if let Some(name) = params.get("name").and_then(|value| value.as_str()) {
        request = request.header("Mcp-Name", name);
    }
    let resp = request
        .json(&serde_json::json!({"jsonrpc":"2.0","id":1,"method":method,"params":params}))
        .send()
        .await
        .unwrap();
    resp.json::<serde_json::Value>().await.unwrap()
}

/// A deterministic, dependency-free estimate used only for this output-size
/// regression test: ceil(serialized UTF-8 bytes / 4).
fn estimated_tokens(bytes: usize) -> usize {
    bytes.div_ceil(4)
}

fn ranges_include_host(results: &[Value], host_id: &str) -> bool {
    results.iter().any(|range| range["host_id"] == host_id)
}

fn rg_fixture_output(
    root: &std::path::Path,
    query: &str,
) -> (Vec<(usize, String)>, Vec<(usize, String, usize)>, Vec<u8>) {
    let json_output = Command::new("rg")
        .args(["--json", "--context=1", "--color=never", query])
        .arg(root.join("realistic.txt"))
        .output()
        .expect("run rg JSON fixture");
    assert!(
        json_output.status.success(),
        "rg JSON fixture failed: {}",
        String::from_utf8_lossy(&json_output.stderr)
    );

    let mut visible_lines = Vec::new();
    let mut matches = Vec::new();
    for raw in String::from_utf8(json_output.stdout)
        .expect("rg JSON is UTF-8 for the fixture")
        .lines()
    {
        let record: Value = serde_json::from_str(raw).expect("rg JSON record");
        let kind = record["type"].as_str().unwrap_or_default();
        if kind != "match" && kind != "context" {
            continue;
        }
        let data = &record["data"];
        let line_number = data["line_number"].as_u64().unwrap() as usize;
        let text = data["lines"]["text"]
            .as_str()
            .unwrap()
            .trim_end_matches(['\n', '\r'])
            .to_string();
        visible_lines.push((line_number, text.clone()));
        if kind == "match" {
            let column = data["submatches"][0]["start"].as_u64().unwrap() as usize + 1;
            matches.push((line_number, text, column));
        }
    }
    visible_lines.sort_by_key(|(line_number, _)| *line_number);
    visible_lines.dedup_by_key(|(line_number, _)| *line_number);

    let text_output = Command::new("rg")
        .args([
            "--with-filename",
            "--line-number",
            "--column",
            "--context=1",
            "--no-heading",
            "--color=never",
            query,
        ])
        .arg(root.join("realistic.txt"))
        .output()
        .expect("run rg text fixture");
    assert!(
        text_output.status.success(),
        "rg text fixture failed: {}",
        String::from_utf8_lossy(&text_output.stderr)
    );
    (visible_lines, matches, text_output.stdout)
}

fn legacy_per_line_results(compact: &Value) -> Vec<Value> {
    compact["results"]
        .as_array()
        .unwrap()
        .iter()
        .flat_map(|range| {
            range["matches"]
                .as_array()
                .unwrap()
                .iter()
                .map(move |matched| {
                    let line_number = matched["line_number"].as_u64().unwrap() as usize;
                    let text = range["lines"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .find(|line| line["line_number"].as_u64() == Some(line_number as u64))
                        .unwrap()["text"]
                        .clone();
                    let context = range["lines"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .filter(|line| {
                            let number = line["line_number"].as_u64().unwrap() as usize;
                            number >= line_number.saturating_sub(1).max(1)
                                && number <= line_number.saturating_add(1)
                        })
                        .cloned()
                        .collect::<Vec<_>>();
                    json!({
                        "host_id": range["host_id"],
                        "path": range["path"],
                        "line_number": line_number,
                        "context": context,
                        "text": text,
                        "column": matched["column"],
                    })
                })
        })
        .collect()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn compact_search_tool_response_matches_rg_per_line_and_reports_efficiency_gap() {
    let temp = TempDir::new().unwrap();
    fs::write(
        temp.path().join("realistic.txt"),
        concat!(
            "2026-08-26T01:00:00Z boot complete\n",
            "2026-08-26T01:00:01Z adjacent SEARCH_CANARY first hit\n",
            "2026-08-26T01:00:02Z adjacent SEARCH_CANARY second hit\n",
            "2026-08-26T01:00:03Z adjacent range tail context\n",
            "2026-08-26T01:00:04Z unrelated heartbeat\n",
            "2026-08-26T01:00:05Z isolated range head context\n",
            "2026-08-26T01:00:06Z isolated SEARCH_CANARY third hit\n",
            "2026-08-26T01:00:07Z shutdown complete\n",
        ),
    )
    .unwrap();
    let service = MeshService::new(
        LocalBackend::from_config(
            "A",
            temp.path(),
            Default::default(),
            std::collections::BTreeMap::new(),
            vec![],
            None,
        ),
        Topology::new("A", vec![]),
    );
    let tool_result = call_tool(
        &service,
        json!({
            "name": "search_text",
            "arguments": {
                "query": "SEARCH_CANARY",
                "hosts": "local",
                "request_id": "compactness-fixture",
                "context_lines": 1,
                "limit": 10
            }
        }),
    )
    .await
    .unwrap();
    let tool_text = tool_result["content"][0]["text"]
        .as_str()
        .unwrap()
        .to_string();
    let compact: Value = serde_json::from_str(&tool_text).unwrap();
    let ranges = compact["results"].as_array().unwrap();
    assert_eq!(
        ranges.len(),
        2,
        "adjacent matches must share one range; response={compact}"
    );
    assert_eq!(ranges[0]["start_line"], 2);
    assert_eq!(ranges[0]["end_line"], 3);
    assert_eq!(ranges[0]["matches"].as_array().unwrap().len(), 2);

    let actual_lines = ranges
        .iter()
        .flat_map(|range| range["lines"].as_array().unwrap())
        .map(|line| {
            (
                line["line_number"].as_u64().unwrap() as usize,
                line["text"].as_str().unwrap().to_string(),
            )
        })
        .collect::<Vec<_>>();
    let actual_matches = ranges
        .iter()
        .flat_map(|range| {
            range["matches"]
                .as_array()
                .unwrap()
                .iter()
                .map(move |matched| {
                    let line_number = matched["line_number"].as_u64().unwrap() as usize;
                    let text = range["lines"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .find(|line| line["line_number"].as_u64() == Some(line_number as u64))
                        .unwrap()["text"]
                        .as_str()
                        .unwrap()
                        .to_string();
                    (
                        line_number,
                        text,
                        matched["column"].as_u64().unwrap() as usize,
                    )
                })
        })
        .collect::<Vec<_>>();
    let (rg_lines, rg_matches, rg_text) = rg_fixture_output(temp.path(), "SEARCH_CANARY");
    assert_eq!(
        actual_lines, rg_lines,
        "per-line output regression: compact GrepMesh range lines diverged from rg"
    );
    assert_eq!(
        actual_matches, rg_matches,
        "per-line output regression: compact GrepMesh match metadata diverged from rg"
    );

    let legacy_results = legacy_per_line_results(&compact);
    let mut legacy = compact.clone();
    legacy["results"] = Value::Array(legacy_results.clone());
    legacy["matches"] = Value::Array(legacy_results);
    let legacy_text = serde_json::to_string(&legacy).unwrap();
    assert!(
        tool_text.len() < legacy_text.len(),
        "adjacent-range compaction regressed: compact={} bytes, legacy={} bytes",
        tool_text.len(),
        legacy_text.len()
    );

    let grepmesh_bytes = tool_text.len();
    let rg_bytes = rg_text.len();
    let grepmesh_tokens = estimated_tokens(grepmesh_bytes);
    let rg_tokens = estimated_tokens(rg_bytes);
    eprintln!(
        "compactness fixture (content[0].text; token estimate=ceil(UTF-8 bytes/4)): \
         grepmesh={grepmesh_bytes}B/{grepmesh_tokens}tok, \
         legacy={}/{}tok, rg={rg_bytes}B/{rg_tokens}tok, \
         grepmesh-vs-rg={:+}B/{:+}tok",
        legacy_text.len(),
        estimated_tokens(legacy_text.len()),
        grepmesh_bytes as isize - rg_bytes as isize,
        grepmesh_tokens as isize - rg_tokens as isize,
    );
    assert!(
        grepmesh_bytes > rg_bytes && grepmesh_tokens > rg_tokens,
        "fixture must retain an honest less-efficient-than-rg case; \
         grepmesh={grepmesh_bytes}B/{grepmesh_tokens}tok, rg={rg_bytes}B/{rg_tokens}tok"
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn black_box_two_process_peer_fanout_and_partial_results() {
    let temp_a = TempDir::new().unwrap();
    let temp_b = TempDir::new().unwrap();
    let root_a = temp_a.path().to_path_buf();
    let root_b = temp_b.path().to_path_buf();
    fs::write(root_a.join("a-canary.txt"), "alpha canary from A\n").unwrap();
    fs::write(root_b.join("b-canary.txt"), "bravo canary from B\n").unwrap();
    let port_a = free_port();
    let port_b = free_port();
    let url_a = format!("http://127.0.0.1:{port_a}/mcp");
    let url_b = format!("http://127.0.0.1:{port_b}/mcp");
    let cfg_b = AppConfig {
        host_id: "B".into(),
        bind: format!("127.0.0.1:{port_b}").parse().unwrap(),
        local_bind: None,
        root: root_b.clone(),
        roots: std::collections::BTreeMap::new(),
        peers: vec![],
        limits: Default::default(),
        exclude_globs: vec![],
        // Keep the runtime-settings sidecar isolated from the host-level
        // default so this black-box process cannot inherit live search roots.
        topology_cache_path: Some(temp_b.path().join("topology-cache.json")),
        index_path: Some(root_b.join("index.sqlite")),
        gptadmin_topology_url: None,
        gptadmin_token_env: None,
        peer_auth_token_env: None,
        topology_ttl_ms: 30_000,
    };
    let cfg_a = AppConfig {
        host_id: "A".into(),
        bind: format!("127.0.0.1:{port_a}").parse().unwrap(),
        local_bind: None,
        root: root_a.clone(),
        roots: std::collections::BTreeMap::new(),
        peers: vec![PeerConfig {
            host_id: "B".into(),
            local_url: "http://127.0.0.1:1/mcp".into(),
            routable_url: url_b.clone(),
        }],
        limits: Default::default(),
        exclude_globs: vec![],
        // Keep the runtime-settings sidecar isolated from the host-level
        // default so this black-box process cannot inherit live search roots.
        topology_cache_path: Some(temp_a.path().join("topology-cache.json")),
        index_path: Some(root_a.join("index.sqlite")),
        gptadmin_topology_url: None,
        gptadmin_token_env: None,
        peer_auth_token_env: None,
        topology_ttl_ms: 30_000,
    };
    let path_a = temp_a.path().join("a.json");
    let path_b = temp_b.path().join("b.json");
    write_config(&path_a, &cfg_a);
    write_config(&path_b, &cfg_b);
    for (path, legacy_index_path) in [
        (&path_a, temp_a.path().join("legacy-index-a.json")),
        (&path_b, temp_b.path().join("legacy-index-b.json")),
    ] {
        let mut value: serde_json::Value =
            serde_json::from_slice(&fs::read(path).unwrap()).unwrap();
        value["index_path"] = serde_json::json!(legacy_index_path);
        fs::write(path, serde_json::to_vec_pretty(&value).unwrap()).unwrap();
    }
    let mut child_b = spawn_server(&path_b);
    let mut child_a = spawn_server(&path_a);
    wait_for_server(port_b).await;
    wait_for_server(port_a).await;

    let init = rpc(&url_a, "initialize", serde_json::json!({})).await;
    assert_eq!(init["jsonrpc"], "2.0");
    let tools = rpc(&url_a, "tools/list", serde_json::json!({})).await;
    assert_eq!(tools["result"]["tools"].as_array().unwrap().len(), 4);

    let status = rpc(
        &url_a,
        "tools/call",
        serde_json::json!({
            "name": "search_status",
            "arguments": {"hosts": "local"}
        }),
    )
    .await;
    let status_text = status["result"]["content"][0]["text"].as_str().unwrap();
    let status_value: serde_json::Value = serde_json::from_str(status_text).unwrap();
    assert_eq!(status_value["local"]["backend"], "indexed+rg-fallback");

    let search = rpc(
        &url_a,
        "tools/call",
        serde_json::json!({
            "name": "search_text",
            "arguments": {"query": "canary", "limit": 10}
        }),
    )
    .await;
    let result_text = search["result"]["content"][0]["text"].as_str().unwrap();
    let value: serde_json::Value = serde_json::from_str(result_text).unwrap();
    assert_eq!(value["host_id"], "A");
    let results = value["results"].as_array().unwrap();
    assert!(
        ranges_include_host(results, "A"),
        "local compact range missing from response: {value}"
    );
    assert!(
        ranges_include_host(results, "B"),
        "remote compact range missing from response: {value}"
    );

    let paths = rpc(
        &url_a,
        "tools/call",
        serde_json::json!({
            "name": "find_paths",
            "arguments": {"pattern": "*canary*", "hosts": "*", "max_matches": 10}
        }),
    )
    .await;
    let paths_text = paths["result"]["content"][0]["text"].as_str().unwrap();
    let paths_value: serde_json::Value = serde_json::from_str(paths_text).unwrap();
    assert!(paths_value["paths"].as_array().unwrap().iter().any(|path| {
        path["host"] == "A" && path["path"].as_str().unwrap().ends_with("a-canary.txt")
    }));
    assert!(paths_value["paths"].as_array().unwrap().iter().any(|path| {
        path["host"] == "B" && path["path"].as_str().unwrap().ends_with("b-canary.txt")
    }));

    let read_remote = rpc(&url_a, "tools/call", serde_json::json!({
        "name": "read_text",
        "arguments": {"host": "B", "path": root_b.join("b-canary.txt"), "start_line": 1, "end_line": 1}
    })).await;
    let read_value: serde_json::Value = serde_json::from_str(
        read_remote["result"]["content"][0]["text"]
            .as_str()
            .unwrap(),
    )
    .unwrap();
    assert_eq!(read_value["host_id"], "A");
    assert_eq!(read_value["target_host_id"], "B");
    assert_eq!(
        read_value["chunks"][0]["lines"][0]["text"],
        "bravo canary from B"
    );

    fs::remove_file(root_b.join("b-canary.txt")).unwrap();
    let _ = child_b.kill();
    let _ = child_b.wait();

    let search_partial = rpc(
        &url_a,
        "tools/call",
        serde_json::json!({
            "name": "search_text",
            "arguments": {"query": "canary", "hosts": "*", "limit": 10}
        }),
    )
    .await;
    let partial_value: serde_json::Value = serde_json::from_str(
        search_partial["result"]["content"][0]["text"]
            .as_str()
            .unwrap(),
    )
    .unwrap();
    assert_eq!(partial_value["partial"], true);
    let partial_results = partial_value["results"].as_array().unwrap();
    assert!(ranges_include_host(partial_results, "A"));
    assert!(!ranges_include_host(partial_results, "B"));

    let _ = child_a.kill();
    let _ = child_a.wait();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn remote_partial_status_and_local_results_survive_fanout() {
    let temp = TempDir::new().unwrap();
    let root = temp.path().to_path_buf();
    fs::write(root.join("local-canary.txt"), "local partial canary\n").unwrap();
    let fake_listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let fake_url = format!(
        "http://127.0.0.1:{}/mcp",
        fake_listener.local_addr().unwrap().port()
    );
    let fake_thread = std::thread::spawn(move || {
        let response_data = serde_json::json!({
            "request_id": "fake-peer-request",
            "origin_host": "A",
            "hop_count": 1,
            "host_id": "B",
            "partial": true,
            "truncated": false,
            "results": [{
                "host_id": "B",
                "path": "/fake/partial-canary.txt",
                "line_number": 1,
                "context": [],
                "text": "fake peer partial",
                "column": 1
            }],
            "host_status": [{
                "host_id": "B",
                "ok": false,
                "error": "peer-local-timeout"
            }]
        });
        let envelope = serde_json::json!({
            "jsonrpc": "2.0",
            "id": 1,
            "result": {
                "content": [{
                    "type": "text",
                    "text": serde_json::to_string(&response_data).unwrap()
                }],
                "isError": false
            }
        });
        let body = serde_json::to_vec(&envelope).unwrap();
        for _ in 0..2 {
            let (mut stream, _) = fake_listener.accept().unwrap();
            let mut request = [0u8; 4096];
            let _ = stream.read(&mut request);
            write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                body.len()
            )
            .unwrap();
            stream.write_all(&body).unwrap();
        }
    });

    let port = free_port();
    let url = format!("http://127.0.0.1:{port}/mcp");
    let cfg = AppConfig {
        host_id: "A".into(),
        bind: format!("127.0.0.1:{port}").parse().unwrap(),
        local_bind: None,
        root: root.clone(),
        roots: std::collections::BTreeMap::new(),
        peers: vec![PeerConfig {
            host_id: "B".into(),
            local_url: "http://127.0.0.1:1/mcp".into(),
            routable_url: fake_url,
        }],
        limits: grepmesh::config::LimitsConfig {
            peer_timeout_ms: 500,
            overall_timeout_ms: 1000,
            ..Default::default()
        },
        exclude_globs: vec![],
        // Keep the runtime-settings sidecar isolated from the host-level
        // default so the local search cannot consume the peer test deadline.
        topology_cache_path: Some(temp.path().join("topology-cache.json")),
        index_path: Some(root.join("index.sqlite")),
        gptadmin_topology_url: None,
        gptadmin_token_env: None,
        peer_auth_token_env: None,
        topology_ttl_ms: 30_000,
    };
    let path = temp.path().join("config.json");
    write_config(&path, &cfg);
    let mut child = spawn_server(&path);
    wait_for_server(port).await;

    let search = rpc(
        &url,
        "tools/call",
        serde_json::json!({
            "name": "search_text",
            "arguments": {"query": "partial", "hosts": "*", "limit": 10}
        }),
    )
    .await;
    let search_value: serde_json::Value =
        serde_json::from_str(search["result"]["content"][0]["text"].as_str().unwrap()).unwrap();
    assert_eq!(search_value["partial"], true);
    assert!(ranges_include_host(
        search_value["results"].as_array().unwrap(),
        "B"
    ));
    assert!(search_value["host_status"]
        .as_array()
        .unwrap()
        .iter()
        .any(|status| status["host_id"] == "B" && status["ok"] == false));

    let paths = rpc(
        &url,
        "tools/call",
        serde_json::json!({
            "name": "find_paths",
            "arguments": {"pattern": "partial", "hosts": "*", "max_matches": 10}
        }),
    )
    .await;
    let paths_value: serde_json::Value =
        serde_json::from_str(paths["result"]["content"][0]["text"].as_str().unwrap()).unwrap();
    assert_eq!(paths_value["partial"], true);
    assert!(paths_value["paths"]
        .as_array()
        .unwrap()
        .iter()
        .any(|path| path["host"] == "B"));
    assert!(paths_value["host_status"]
        .as_array()
        .unwrap()
        .iter()
        .any(|status| status["host_id"] == "B" && status["ok"] == false));

    let _ = child.kill();
    let _ = child.wait();
    fake_thread.join().unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn stalled_peer_body_keeps_completed_local_results() {
    let temp = TempDir::new().unwrap();
    let root = temp.path().to_path_buf();
    fs::write(root.join("local-stalled.txt"), "local stalled canary\n").unwrap();
    let fake_listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let fake_url = format!(
        "http://127.0.0.1:{}/mcp",
        fake_listener.local_addr().unwrap().port()
    );
    let fake_thread = std::thread::spawn(move || {
        let (mut stream, _) = fake_listener.accept().unwrap();
        let mut request = [0u8; 4096];
        let _ = stream.read(&mut request);
        write!(
            stream,
            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100000\r\nConnection: close\r\n\r\n"
        )
        .unwrap();
        std::thread::sleep(Duration::from_millis(700));
    });

    let port = free_port();
    let url = format!("http://127.0.0.1:{port}/mcp");
    let cfg = AppConfig {
        host_id: "A".into(),
        bind: format!("127.0.0.1:{port}").parse().unwrap(),
        local_bind: None,
        root: root.clone(),
        roots: std::collections::BTreeMap::new(),
        peers: vec![PeerConfig {
            host_id: "B".into(),
            local_url: "http://127.0.0.1:1/mcp".into(),
            routable_url: fake_url,
        }],
        limits: grepmesh::config::LimitsConfig {
            peer_timeout_ms: 150,
            overall_timeout_ms: 500,
            ..Default::default()
        },
        exclude_globs: vec![],
        topology_cache_path: None,
        index_path: Some(root.join("index.sqlite")),
        gptadmin_topology_url: None,
        gptadmin_token_env: None,
        peer_auth_token_env: None,
        topology_ttl_ms: 30_000,
    };
    let path = temp.path().join("config.json");
    write_config(&path, &cfg);
    let mut child = spawn_server(&path);
    wait_for_server(port).await;

    let search = rpc(
        &url,
        "tools/call",
        serde_json::json!({
            "name": "search_text",
            "arguments": {"query": "stalled", "hosts": "*", "limit": 10}
        }),
    )
    .await;
    let value: serde_json::Value =
        serde_json::from_str(search["result"]["content"][0]["text"].as_str().unwrap()).unwrap();
    assert_eq!(value["partial"], true);
    assert!(ranges_include_host(
        value["results"].as_array().unwrap(),
        "A"
    ));
    assert!(value["host_status"]
        .as_array()
        .unwrap()
        .iter()
        .any(|status| status["host_id"] == "B" && status["ok"] == false));

    let _ = child.kill();
    let _ = child.wait();
    fake_thread.join().unwrap();
}
