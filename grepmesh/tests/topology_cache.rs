#[path = "../src/topology_cache.rs"]
mod topology_cache;

use std::{fs, path::PathBuf};
use tempfile::TempDir;
use topology_cache::{CacheFreshness, ProviderSnapshot, TopologyNode, TopologySnapshot};

fn node(host_id: &str, generation: u64, fetched_at_ms: u64, expires_at_ms: u64) -> TopologyNode {
    TopologyNode {
        host_id: host_id.to_string(),
        local_url: format!("http://{host_id}.local:9419/mcp"),
        routable_url: format!("http://{host_id}.example.com:9419/mcp"),
        capabilities: vec!["search_text".into(), "read_text".into()],
        roots: vec!["/srv/grepmesh".into()],
        generation,
        fetched_at_ms,
        expires_at_ms,
        last_refresh_error: None,
    }
}

fn provider_snapshot(host_id: &str, generation: u64, peers: Vec<TopologyNode>) -> ProviderSnapshot {
    ProviderSnapshot {
        local_host_id: host_id.to_string(),
        generation,
        fetched_at_ms: 1_700_000_000_000,
        ttl_ms: 60_000,
        peers,
    }
}

fn write_text(path: &PathBuf, text: &str) {
    fs::write(path, text).unwrap();
}

#[test]
fn validation_rejects_incomplete_nodes() {
    let snapshot = TopologySnapshot {
        local_host_id: "A".into(),
        generation: 1,
        fetched_at_ms: Some(10),
        expires_at_ms: Some(20),
        last_refresh_error: None,
        peers: vec![TopologyNode {
            host_id: "B".into(),
            local_url: String::new(),
            routable_url: "http://peer.example.com:9419/mcp".into(),
            capabilities: vec![],
            roots: vec![],
            generation: 1,
            fetched_at_ms: 10,
            expires_at_ms: 20,
            last_refresh_error: None,
        }],
    };

    assert!(snapshot.validate().is_err());
}

#[test]
fn empty_state_is_reported() {
    let snapshot = TopologySnapshot::empty("A");
    assert_eq!(snapshot.freshness_at(1_000), CacheFreshness::Empty);
    assert!(snapshot.validate().is_ok());
}

#[test]
fn generation_and_freshness_are_reported() {
    let snapshot = TopologySnapshot {
        local_host_id: "A".into(),
        generation: 7,
        fetched_at_ms: Some(1_000),
        expires_at_ms: Some(2_000),
        last_refresh_error: None,
        peers: vec![node("B", 7, 1_000, 2_000)],
    };

    assert_eq!(snapshot.freshness_at(1_500), CacheFreshness::Fresh);
    assert_eq!(snapshot.freshness_at(2_100), CacheFreshness::StaleButUsable);
    assert_eq!(
        snapshot.freshness_at(2_000 + 5 * 60 * 1000 + 1),
        CacheFreshness::Expired
    );
    assert_eq!(snapshot.generation, 7);
}

#[test]
fn ttl_and_stale_fallback_preserve_cached_peers() {
    let base = TopologySnapshot {
        local_host_id: "A".into(),
        generation: 1,
        fetched_at_ms: Some(1_000),
        expires_at_ms: Some(2_000),
        last_refresh_error: None,
        peers: vec![node("B", 1, 1_000, 2_000)],
    };

    let failed = base.with_refresh_error("peer B unreachable");
    assert_eq!(
        failed.last_refresh_error.as_deref(),
        Some("peer B unreachable")
    );
    assert_eq!(failed.peer("B").unwrap().host_id, "B");
    assert_eq!(failed.freshness_at(1_500), CacheFreshness::StaleButUsable);
    assert_eq!(
        failed.peer("B").unwrap().freshness_at(1_500),
        CacheFreshness::StaleButUsable
    );
}

#[test]
fn corrupt_cache_is_rejected() {
    let temp = TempDir::new().unwrap();
    let path = temp.path().join("topology.json");
    write_text(&path, "{not-json");

    assert!(TopologySnapshot::load(&path).is_err());
}

#[test]
fn atomic_persistence_round_trips() {
    let temp = TempDir::new().unwrap();
    let path = temp.path().join("topology.json");
    let snapshot = TopologySnapshot {
        local_host_id: "A".into(),
        generation: 3,
        fetched_at_ms: Some(10_000),
        expires_at_ms: Some(20_000),
        last_refresh_error: None,
        peers: vec![node("B", 3, 10_000, 20_000)],
    };

    snapshot.save_atomic(&path).unwrap();
    let loaded = TopologySnapshot::load(&path).unwrap();
    assert_eq!(loaded, snapshot);
    assert!(path.exists());
}

#[test]
fn duplicate_host_ids_are_rejected() {
    let snapshot = TopologySnapshot {
        local_host_id: "A".into(),
        generation: 1,
        fetched_at_ms: Some(1_000),
        expires_at_ms: Some(2_000),
        last_refresh_error: None,
        peers: vec![node("B", 1, 1_000, 2_000), node("B", 1, 1_000, 2_000)],
    };

    assert!(snapshot.validate().is_err());

    let provider = provider_snapshot(
        "A",
        1,
        vec![node("B", 1, 1_000, 2_000), node("B", 1, 1_000, 2_000)],
    );
    assert!(provider.validate().is_err());
}

#[test]
fn unreachable_peers_keep_cached_entries_and_error_status() {
    let cached = TopologySnapshot {
        local_host_id: "A".into(),
        generation: 2,
        fetched_at_ms: Some(1_000),
        expires_at_ms: Some(2_000),
        last_refresh_error: None,
        peers: vec![node("B", 2, 1_000, 2_000)],
    };

    let refreshed = cached.with_refresh_error("timeout talking to B");
    assert_eq!(refreshed.peer("B").unwrap().host_id, "B");
    assert_eq!(
        refreshed.last_refresh_error.as_deref(),
        Some("timeout talking to B")
    );
    assert_eq!(
        refreshed.freshness_at(1_500),
        CacheFreshness::StaleButUsable
    );
}

#[test]
fn merge_fresh_provider_result_is_deterministic() {
    let cached = TopologySnapshot {
        local_host_id: "A".into(),
        generation: 2,
        fetched_at_ms: Some(1_000),
        expires_at_ms: Some(2_000),
        last_refresh_error: Some("previous failure".into()),
        peers: vec![node("C", 2, 1_000, 2_000), node("B", 2, 1_000, 2_000)],
    };

    let provider = provider_snapshot(
        "A",
        4,
        vec![node("D", 4, 1_700_000_000_000, 1_700_000_060_000)],
    );
    let merged = cached.merge_fresh_provider_result(provider).unwrap();

    assert_eq!(merged.generation, 4);
    assert_eq!(merged.last_refresh_error, None);
    let host_ids: Vec<_> = merged
        .peers
        .iter()
        .map(|peer| peer.host_id.as_str())
        .collect();
    assert_eq!(host_ids, vec!["D"]);
    assert_eq!(
        merged.freshness_at(1_700_000_000_100),
        CacheFreshness::Fresh
    );
}

#[test]
fn fresh_empty_provider_result_is_valid_and_removes_cached_peers() {
    let cached = TopologySnapshot {
        local_host_id: "A".into(),
        generation: 2,
        fetched_at_ms: Some(1_000),
        expires_at_ms: Some(2_000),
        last_refresh_error: None,
        peers: vec![node("B", 2, 1_000, 2_000)],
    };
    let provider = provider_snapshot("A", 3, vec![]);
    let refreshed = cached.merge_fresh_provider_result(provider).unwrap();
    assert!(refreshed.peers.is_empty());
    assert_eq!(refreshed.freshness_at(1_500), CacheFreshness::Fresh);
    refreshed.validate().unwrap();
}
