use grepmesh::{
    backend::{IndexState, LocalBackend, SearchMode},
    config::LimitsConfig,
    index::PersistentIndex,
};
use std::time::Duration;
use std::{collections::BTreeMap, fs};

#[tokio::test]
async fn search_modes_and_globs_preserve_match_metadata() {
    let root = tempfile::tempdir().unwrap();
    fs::create_dir_all(root.path().join("ignored")).unwrap();
    fs::write(
        root.path().join("config.rs"),
        "prefix CUDA_BROKER_URL suffix\ncuda_broker_url\n",
    )
    .unwrap();
    fs::write(root.path().join("ignored/config.rs"), "CUDA_BROKER_URL\n").unwrap();

    let backend = LocalBackend::new("A", root.path(), Default::default())
        .with_excludes(vec!["**/ignored/**".into()]);
    let literal = backend
        .search_text(
            "CUDA_BROKER_URL",
            10,
            0,
            SearchMode::Literal,
            vec!["**/*.rs".into()],
            vec![],
        )
        .await
        .unwrap();
    assert_eq!(literal.len(), 1);
    assert_eq!(literal[0].text, "prefix CUDA_BROKER_URL suffix");
    assert_eq!(literal[0].column, 8);

    let insensitive = backend
        .search_text(
            "cuda_broker_url",
            10,
            0,
            SearchMode::CaseInsensitiveLiteral,
            vec!["**/*.rs".into()],
            vec![],
        )
        .await
        .unwrap();
    assert_eq!(insensitive.len(), 2);

    let regex = backend
        .search_text(
            "CUDA_.*URL",
            10,
            0,
            SearchMode::Regex,
            vec!["**/*.rs".into()],
            vec![],
        )
        .await
        .unwrap();
    assert_eq!(regex.len(), 1);
    assert_eq!(regex[0].column, 1);
}

#[tokio::test]
async fn malformed_regex_and_glob_remain_search_errors() {
    let root = tempfile::tempdir().unwrap();
    fs::write(root.path().join("config.rs"), "SEARCH_INPUT_TOKEN\n").unwrap();
    let backend = LocalBackend::from_config(
        "A",
        root.path(),
        Default::default(),
        BTreeMap::new(),
        vec![],
        None,
    );

    let invalid_regex = backend
        .search_text_bounded("(", 10, 0, SearchMode::Regex, vec![], vec![])
        .await;
    assert!(invalid_regex.is_err());

    let invalid_glob = backend
        .search_text_bounded(
            "SEARCH_INPUT_TOKEN",
            10,
            0,
            SearchMode::Literal,
            vec!["[".into()],
            vec![],
        )
        .await;
    assert!(invalid_glob.is_err());
}

#[tokio::test]
async fn named_roots_are_selectable_and_paths_remain_absolute() {
    let home = tempfile::tempdir().unwrap();
    let opt = tempfile::tempdir().unwrap();
    fs::write(home.path().join("home.txt"), "home-only\n").unwrap();
    fs::write(opt.path().join("opt.txt"), "opt-only\n").unwrap();
    let mut roots = BTreeMap::new();
    roots.insert("home".into(), vec![home.path().to_path_buf()]);
    roots.insert("opt".into(), vec![opt.path().to_path_buf()]);
    let backend = LocalBackend::new("A", home.path(), Default::default()).with_named_roots(roots);

    let selected = backend
        .search_text(
            "opt-only",
            10,
            0,
            SearchMode::Literal,
            vec![],
            vec!["opt".into()],
        )
        .await
        .unwrap();
    assert_eq!(selected.len(), 1);
    assert_eq!(
        selected[0].path,
        opt.path().join("opt.txt").display().to_string()
    );

    let unknown = backend
        .search_text(
            "home-only",
            10,
            0,
            SearchMode::Literal,
            vec![],
            vec!["missing".into()],
        )
        .await;
    assert!(unknown.is_err());
}

#[tokio::test]
async fn unrestricted_roots_allow_an_explicit_absolute_directory() {
    let configured = tempfile::tempdir().unwrap();
    let outside = tempfile::tempdir().unwrap();
    fs::write(outside.path().join("outside.txt"), "runtime-root\n").unwrap();
    let backend =
        LocalBackend::new("A", configured.path(), Default::default()).with_unrestricted_roots(true);

    let hits = backend
        .search_text(
            "runtime-root",
            10,
            0,
            SearchMode::Literal,
            vec![],
            vec![outside.path().display().to_string()],
        )
        .await
        .unwrap();
    assert_eq!(hits.len(), 1);
}

#[tokio::test]
async fn rg_search_sees_files_created_after_backend_construction() {
    let home = tempfile::tempdir().unwrap();
    let opt = tempfile::tempdir().unwrap();
    let mut roots = BTreeMap::new();
    roots.insert("home".into(), vec![home.path().to_path_buf()]);
    roots.insert("opt".into(), vec![opt.path().to_path_buf()]);

    let backend = LocalBackend::new("A", home.path(), Default::default()).with_named_roots(roots);
    fs::write(
        opt.path().join("created-after-start.txt"),
        "fresh-rg-content\n",
    )
    .unwrap();
    let status = backend.status().unwrap();
    assert_eq!(status.backend, "indexed+rg-fallback");
    let hits = backend
        .search_text(
            "fresh-rg-content",
            10,
            0,
            SearchMode::Literal,
            vec![],
            vec!["opt".into()],
        )
        .await
        .unwrap();
    assert_eq!(hits.len(), 1);
    assert_eq!(hits[0].host_id, "A");
}

#[test]
fn index_candidates_reconcile_create_and_delete() {
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("candidate.txt");
    fs::write(&path, "INDEX_WATCH_TOKEN\n").unwrap();
    let backend = LocalBackend::new("A", root.path(), Default::default());
    let deadline = std::time::Instant::now() + Duration::from_secs(6);
    while std::time::Instant::now() < deadline
        && !backend
            .index
            .candidate_paths("INDEX_WATCH_TOKEN", root.path())
            .unwrap_or_default()
            .contains(&path)
    {
        std::thread::sleep(Duration::from_millis(50));
    }
    assert!(backend
        .index
        .candidate_paths("INDEX_WATCH_TOKEN", root.path())
        .unwrap_or_default()
        .contains(&path));
    fs::remove_file(&path).unwrap();
    let deadline = std::time::Instant::now() + Duration::from_secs(6);
    while std::time::Instant::now() < deadline
        && backend
            .index
            .candidate_paths("INDEX_WATCH_TOKEN", root.path())
            .unwrap_or_default()
            .contains(&path)
    {
        std::thread::sleep(Duration::from_millis(50));
    }
    assert!(!backend
        .index
        .candidate_paths("INDEX_WATCH_TOKEN", root.path())
        .unwrap_or_default()
        .contains(&path));
}

#[test]
fn configured_index_path_receives_scanned_documents_and_reconciles_deletes() {
    let root = tempfile::tempdir().unwrap();
    let db = root.path().join("grepmesh-index.sqlite");
    let document = root.path().join("persistent.txt");
    fs::write(&document, "PERSISTENT_INDEX_TOKEN\n").unwrap();

    let backend = LocalBackend::from_config(
        "A",
        root.path(),
        Default::default(),
        BTreeMap::new(),
        vec![],
        Some(db.clone()),
    );
    wait_until_ready(&backend);

    assert_eq!(
        PersistentIndex::open(db.clone())
            .unwrap()
            .candidates("PERSISTENT_INDEX_TOKEN")
            .unwrap(),
        vec![document.clone()]
    );

    fs::remove_file(&document).unwrap();
    wait_until_persistent_missing(&db, "PERSISTENT_INDEX_TOKEN");
    wait_until_ready(&backend);
    assert!(PersistentIndex::open(db)
        .unwrap()
        .candidates("PERSISTENT_INDEX_TOKEN")
        .unwrap()
        .is_empty());
}

#[test]
fn config_without_index_path_is_immediately_ready_and_skips_scanning() {
    let root = tempfile::tempdir().unwrap();
    fs::write(
        root.path().join("large-enough-to-index.txt"),
        "DIRECT_RG_DEFAULT_TOKEN\n",
    )
    .unwrap();

    let backend = LocalBackend::from_config(
        "A",
        root.path(),
        Default::default(),
        BTreeMap::new(),
        vec![],
        None,
    );

    let status = backend.status().unwrap();
    assert_eq!(status.index_state, Some(IndexState::Ready));
    assert_eq!(status.indexed_files, 0);
    assert_eq!(status.backend, "rg");
    assert!(backend
        .index
        .candidate_paths("DIRECT_RG_DEFAULT_TOKEN", root.path())
        .is_none());
}

#[tokio::test]
async fn rg_search_stops_after_the_requested_match_limit() {
    let root = tempfile::tempdir().unwrap();
    let content = (0..128)
        .map(|line| format!("MATCH_LIMIT_TOKEN_{line}\n"))
        .collect::<String>();
    fs::write(root.path().join("many-matches.txt"), content).unwrap();

    let backend = LocalBackend::new("A", root.path(), Default::default());
    let hits = backend
        .search_text(
            "MATCH_LIMIT_TOKEN",
            3,
            0,
            SearchMode::Literal,
            vec![],
            vec![],
        )
        .await
        .unwrap();
    assert_eq!(hits.len(), 3);
}

#[tokio::test]
async fn rg_byte_bound_is_reported_as_truncated() {
    let root = tempfile::tempdir().unwrap();
    let content = (0..128)
        .map(|line| format!("BYTE_BOUND_TOKEN_{line}\n"))
        .collect::<String>();
    fs::write(root.path().join("many-matches.txt"), content).unwrap();

    let limits = LimitsConfig {
        max_response_bytes: 64,
        ..Default::default()
    };
    let backend = LocalBackend::new("A", root.path(), limits);
    let outcome = backend
        .search_text_bounded(
            "BYTE_BOUND_TOKEN",
            100,
            0,
            SearchMode::Literal,
            vec![],
            vec![],
        )
        .await
        .unwrap();
    assert!(outcome.truncated);
    assert!(outcome.hits.len() < 100);
}

#[tokio::test]
async fn rg_path_search_is_bounded_and_reports_truncation() {
    let root = tempfile::tempdir().unwrap();
    for file in 0..128 {
        fs::write(root.path().join(format!("candidate-{file}.txt")), "path\n").unwrap();
    }

    let limits = LimitsConfig {
        max_response_bytes: 64,
        ..Default::default()
    };
    let backend = LocalBackend::new("A", root.path(), limits);
    let outcome = backend
        .find_paths_bounded("candidate-", 100, vec![])
        .await
        .unwrap();
    assert!(outcome.truncated);
    assert!(outcome.hits.len() < 100);
}

fn wait_until_ready(backend: &LocalBackend) {
    let deadline = std::time::Instant::now() + Duration::from_secs(6);
    while std::time::Instant::now() < deadline {
        let status = backend.status().unwrap();
        if status.index_state == Some(IndexState::Ready) {
            return;
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    panic!("index did not reach Ready: {:?}", backend.status().unwrap());
}

fn wait_until_persistent_missing(db: &std::path::Path, query: &str) {
    let deadline = std::time::Instant::now() + Duration::from_secs(6);
    while std::time::Instant::now() < deadline {
        if PersistentIndex::open(db.to_path_buf())
            .unwrap()
            .candidates(query)
            .unwrap()
            .is_empty()
        {
            return;
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    panic!("persistent index did not remove {query}");
}
