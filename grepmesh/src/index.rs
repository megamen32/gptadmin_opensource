use crate::backend::IndexState;
use globset::{Glob, GlobSet, GlobSetBuilder};
use notify::{RecommendedWatcher, RecursiveMode, Watcher};
use rusqlite::{params, Connection};
use std::{
    collections::{BTreeMap, BTreeSet, VecDeque},
    fs,
    io::ErrorKind,
    os::unix::fs::MetadataExt,
    path::{Path, PathBuf},
    sync::{mpsc, Arc, RwLock},
    thread,
    time::{Duration, Instant},
};

type IndexMap = BTreeMap<String, BTreeSet<PathBuf>>;
type DirectoryScan = (usize, IndexMap, Vec<IndexedDocument>, Vec<PathBuf>);

#[derive(Clone, Debug)]
struct IndexedDocument {
    path: PathBuf,
    body: String,
}

struct RebuildState<'a> {
    snapshot: &'a Arc<RwLock<IndexSnapshot>>,
    candidates: &'a Arc<RwLock<BTreeMap<String, BTreeSet<PathBuf>>>>,
    ready_roots: &'a Arc<RwLock<BTreeSet<PathBuf>>>,
    persistent: Option<&'a PersistentIndex>,
}

struct ScanContext<'a> {
    root: &'a Path,
    root_device: u64,
    excludes: &'a GlobSet,
    max_file_bytes: u64,
    build_candidates: bool,
}

#[derive(Clone, Debug)]
pub struct PersistentIndex {
    path: PathBuf,
}

impl PersistentIndex {
    pub fn open(path: PathBuf) -> Result<Self, String> {
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).map_err(|error| format!("{}: {error}", parent.display()))?;
        }
        let store = Self { path };
        store.connection()?;
        Ok(store)
    }

    fn connection(&self) -> Result<Connection, String> {
        let connection = Connection::open(&self.path).map_err(|error| error.to_string())?;
        connection
            .execute_batch(
                "PRAGMA journal_mode=WAL;\
                 CREATE VIRTUAL TABLE IF NOT EXISTS grepmesh_documents \
                 USING fts5(path UNINDEXED, body, tokenize='trigram');",
            )
            .map_err(|error| error.to_string())?;
        Ok(connection)
    }

    pub fn replace_document(&self, path: &Path, body: &str) -> Result<(), String> {
        let connection = self.connection()?;
        let path = path.display().to_string();
        connection
            .execute(
                "DELETE FROM grepmesh_documents WHERE path = ?1",
                params![path],
            )
            .map_err(|error| error.to_string())?;
        connection
            .execute(
                "INSERT INTO grepmesh_documents(path, body) VALUES (?1, ?2)",
                params![path, body],
            )
            .map_err(|error| error.to_string())?;
        Ok(())
    }

    pub fn clear(&self) -> Result<(), String> {
        let connection = self.connection()?;
        connection
            .execute("DELETE FROM grepmesh_documents", [])
            .map_err(|error| error.to_string())?;
        Ok(())
    }

    pub fn candidates(&self, query: &str) -> Result<Vec<PathBuf>, String> {
        let connection = self.connection()?;
        let mut statement = connection
            .prepare("SELECT path FROM grepmesh_documents WHERE body MATCH ?1")
            .map_err(|error| error.to_string())?;
        let rows = statement
            .query_map(params![query], |row| row.get::<_, String>(0))
            .map_err(|error| error.to_string())?;
        rows.map(|row| row.map(PathBuf::from).map_err(|error| error.to_string()))
            .collect()
    }
}

#[derive(Clone, Debug)]
pub struct IndexSnapshot {
    pub state: IndexState,
    pub generation: u64,
    pub indexed_files: usize,
    pub last_error: Option<String>,
}

impl Default for IndexSnapshot {
    fn default() -> Self {
        Self {
            state: IndexState::Building,
            generation: 0,
            indexed_files: 0,
            last_error: None,
        }
    }
}

#[derive(Clone)]
pub struct IndexManager {
    snapshot: Arc<RwLock<IndexSnapshot>>,
    candidates: Arc<RwLock<BTreeMap<String, BTreeSet<PathBuf>>>>,
    ready_roots: Arc<RwLock<BTreeSet<PathBuf>>>,
    persistent: Option<PersistentIndex>,
    enabled: bool,
}

impl IndexManager {
    pub fn disabled() -> Self {
        Self {
            snapshot: Arc::new(RwLock::new(IndexSnapshot {
                state: IndexState::Ready,
                ..Default::default()
            })),
            candidates: Arc::new(RwLock::new(BTreeMap::new())),
            ready_roots: Arc::new(RwLock::new(BTreeSet::new())),
            persistent: None,
            enabled: false,
        }
    }

    pub fn start(
        roots: BTreeMap<String, Vec<PathBuf>>,
        excludes: Vec<String>,
        max_file_bytes: u64,
        persistent_path: Option<PathBuf>,
    ) -> Self {
        let snapshot = Arc::new(RwLock::new(IndexSnapshot {
            state: IndexState::Building,
            ..Default::default()
        }));
        let state = Arc::clone(&snapshot);
        let candidates = Arc::new(RwLock::new(BTreeMap::new()));
        let candidate_state = Arc::clone(&candidates);
        let ready_roots = Arc::new(RwLock::new(BTreeSet::new()));
        let ready_root_state = Arc::clone(&ready_roots);
        let persistent =
            persistent_path.and_then(|path| match PersistentIndex::open(path.clone()) {
                Ok(index) => Some(index),
                Err(error) => {
                    if let Ok(mut current) = state.write() {
                        current.state = IndexState::Degraded;
                        current.last_error =
                            Some(format!("open persistent index {}: {error}", path.display()));
                    }
                    None
                }
            });
        let persistent_state = persistent.clone();
        thread::spawn(move || {
            let (events, rx) = mpsc::channel();
            let mut watcher: RecommendedWatcher = notify::recommended_watcher(move |_| {
                let _ = events.send(());
            })
            .expect("create GrepMesh watcher");
            for root in ordered_roots(&roots) {
                let _ = watcher.watch(&root, RecursiveMode::Recursive);
            }
            let mut generation = 0;
            let mut rebuild = || {
                rebuild_index(
                    &roots,
                    &excludes,
                    max_file_bytes,
                    RebuildState {
                        snapshot: &state,
                        candidates: &candidate_state,
                        ready_roots: &ready_root_state,
                        persistent: persistent_state.as_ref(),
                    },
                    &mut generation,
                )
            };
            rebuild();
            loop {
                if rx.recv_timeout(Duration::from_secs(30)).is_ok() {
                    // Filesystems commonly emit a burst of events for one
                    // logical update. Wait for a quiet interval before the
                    // expensive reconciliation and never advertise Building
                    // until a rebuild actually starts.
                    let debounce_deadline = Instant::now() + Duration::from_secs(2);
                    while let Some(remaining) =
                        debounce_deadline.checked_duration_since(Instant::now())
                    {
                        if rx.recv_timeout(remaining).is_err() {
                            break;
                        }
                    }
                    rebuild();
                }
            }
        });
        Self {
            snapshot,
            candidates,
            ready_roots,
            persistent,
            enabled: true,
        }
    }

    pub fn status(&self) -> IndexSnapshot {
        self.snapshot
            .read()
            .map(|s| s.clone())
            .unwrap_or(IndexSnapshot {
                state: IndexState::Degraded,
                ..Default::default()
            })
    }

    pub fn is_enabled(&self) -> bool {
        self.enabled
    }

    pub fn candidate_paths(&self, query: &str, root: &Path) -> Option<Vec<PathBuf>> {
        if !self
            .ready_roots
            .read()
            .ok()
            .is_some_and(|roots| roots.iter().any(|ready| root.starts_with(ready)))
        {
            return None;
        }
        let grams = trigrams(&query.to_ascii_lowercase());
        if grams.is_empty() {
            return None;
        }
        if let Some(persistent) = &self.persistent {
            match persistent.candidates(query) {
                Ok(paths) => {
                    return Some(
                        paths
                            .into_iter()
                            .filter(|path| path.starts_with(root))
                            .collect(),
                    );
                }
                Err(_) => return None,
            }
        }
        let map = self.candidates.read().ok()?;
        let mut sets = grams.iter().map(|gram| map.get(gram));
        let first = sets.next()?.cloned()?;
        let result: Vec<_> = sets
            .try_fold(first, |acc, set| {
                set.map(|set| acc.intersection(set).cloned().collect())
            })?
            .into_iter()
            .filter(|path| path.starts_with(root))
            .collect();
        Some(result)
    }
}

fn rebuild_index(
    roots: &BTreeMap<String, Vec<PathBuf>>,
    excludes: &[String],
    max_file_bytes: u64,
    rebuild: RebuildState<'_>,
    generation: &mut u64,
) {
    let matcher = match compile_excludes(excludes) {
        Ok(matcher) => matcher,
        Err(error) => {
            if let Ok(mut current) = rebuild.snapshot.write() {
                current.state = IndexState::Degraded;
                current.last_error = Some(error);
                current.generation = generation.saturating_add(1);
            }
            return;
        }
    };
    let mut count = 0;
    if let Ok(mut current) = rebuild.snapshot.write() {
        current.state = IndexState::Building;
        current.indexed_files = 0;
        current.last_error = None;
    }
    if let Ok(mut map) = rebuild.candidates.write() {
        map.clear();
    }
    if let Ok(mut ready) = rebuild.ready_roots.write() {
        ready.clear();
    }
    if let Some(persistent) = rebuild.persistent {
        if let Err(error) = persistent.clear() {
            if let Ok(mut current) = rebuild.snapshot.write() {
                current.state = IndexState::Degraded;
                current.last_error = Some(error);
                current.generation = generation.saturating_add(1);
            }
            return;
        }
    }
    for root in ordered_roots(roots) {
        let mut units: VecDeque<_> = match root_units(&root) {
            Ok(units) => units.into(),
            Err(error) => {
                if let Ok(mut current) = rebuild.snapshot.write() {
                    current.state = IndexState::Degraded;
                    current.last_error = Some(error);
                    current.generation = generation.saturating_add(1);
                }
                return;
            }
        };
        while let Some(unit) = units.pop_front() {
            let result = scan_directory_unit(
                &unit,
                &root,
                &matcher,
                max_file_bytes,
                rebuild.persistent.is_none(),
            );
            let (unit_count, next, documents, children) = match result {
                Ok(result) => result,
                Err(error) => {
                    if let Ok(mut current) = rebuild.snapshot.write() {
                        current.state = IndexState::Degraded;
                        current.last_error = Some(error);
                        current.generation = generation.saturating_add(1);
                    }
                    return;
                }
            };
            if rebuild.persistent.is_none() {
                if let Ok(mut map) = rebuild.candidates.write() {
                    for (gram, paths) in next {
                        map.entry(gram).or_default().extend(paths);
                    }
                }
            }
            if let Some(persistent) = rebuild.persistent {
                for document in documents {
                    if let Err(error) = persistent.replace_document(&document.path, &document.body)
                    {
                        if let Ok(mut current) = rebuild.snapshot.write() {
                            current.state = IndexState::Degraded;
                            current.last_error =
                                Some(format!("{}: {error}", document.path.display()));
                            current.generation = generation.saturating_add(1);
                        }
                        return;
                    }
                }
            }
            units.extend(children);
            count += unit_count;
            *generation += 1;
            if let Ok(mut current) = rebuild.snapshot.write() {
                current.indexed_files = count;
                current.generation = *generation;
            }
        }
        if let Ok(mut ready) = rebuild.ready_roots.write() {
            ready.insert(root);
        }
    }
    if let Ok(mut current) = rebuild.snapshot.write() {
        current.state = IndexState::Ready;
        current.last_error = None;
    }
}

#[cfg(test)]
fn build_root_index(
    root: &Path,
    excludes: &[String],
    max_file_bytes: u64,
) -> Result<(usize, BTreeMap<String, BTreeSet<PathBuf>>), String> {
    let matcher = compile_excludes(excludes)?;
    let root_device = fs::symlink_metadata(root)
        .map_err(|error| format!("{}: {error}", root.display()))?
        .dev();
    let mut map = BTreeMap::new();
    let mut documents = Vec::new();
    let context = ScanContext {
        root,
        root_device,
        excludes: &matcher,
        max_file_bytes,
        build_candidates: true,
    };
    let count = walk(&root.to_path_buf(), &context, &mut map, &mut documents)?;
    Ok((count, map))
}

fn scan_directory_unit(
    unit: &Path,
    root: &Path,
    excludes: &GlobSet,
    max_file_bytes: u64,
    build_candidates: bool,
) -> Result<DirectoryScan, String> {
    let root_device = fs::symlink_metadata(root)
        .map_err(|error| format!("{}: {error}", root.display()))?
        .dev();
    let mut map = BTreeMap::new();
    let metadata = match fs::symlink_metadata(unit) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == ErrorKind::PermissionDenied => {
            return Ok((0, map, Vec::new(), Vec::new()))
        }
        Err(error) => return Err(format!("{}: {error}", unit.display())),
    };
    if metadata.dev() != root_device
        || metadata.file_type().is_symlink()
        || excluded(unit, root, excludes)
    {
        return Ok((0, map, Vec::new(), Vec::new()));
    }
    if metadata.is_file() {
        let mut documents = Vec::new();
        let context = ScanContext {
            root,
            root_device,
            excludes,
            max_file_bytes,
            build_candidates,
        };
        let count = walk(&unit.to_path_buf(), &context, &mut map, &mut documents)?;
        return Ok((count, map, documents, Vec::new()));
    }
    if !metadata.is_dir() {
        return Ok((0, map, Vec::new(), Vec::new()));
    }
    let children = match fs::read_dir(unit) {
        Ok(entries) => entries
            .filter_map(|entry| entry.ok().map(|entry| entry.path()))
            .collect(),
        Err(error) if error.kind() == ErrorKind::PermissionDenied => Vec::new(),
        Err(error) => return Err(format!("{}: {error}", unit.display())),
    };
    Ok((0, map, Vec::new(), children))
}

fn root_units(root: &Path) -> Result<Vec<PathBuf>, String> {
    let mut units: Vec<_> = fs::read_dir(root)
        .map_err(|error| format!("{}: {error}", root.display()))?
        .filter_map(|entry| entry.ok().map(|entry| entry.path()))
        .collect();
    units.sort_by_key(|path| {
        let name = path
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or_default();
        (name != ".grepmesh-canary", path.clone())
    });
    Ok(units)
}

fn ordered_roots(roots: &BTreeMap<String, Vec<PathBuf>>) -> Vec<PathBuf> {
    let mut ordered = Vec::new();
    let mut seen = BTreeSet::new();
    for name in ["home", "opt", "etc", "local"] {
        if let Some(paths) = roots.get(name) {
            for path in paths {
                if seen.insert(path.clone()) {
                    ordered.push(path.clone());
                }
            }
        }
    }
    for paths in roots.values() {
        for path in paths {
            if seen.insert(path.clone()) {
                ordered.push(path.clone());
            }
        }
    }
    ordered
}

fn walk(
    path: &PathBuf,
    context: &ScanContext<'_>,
    map: &mut BTreeMap<String, BTreeSet<PathBuf>>,
    documents: &mut Vec<IndexedDocument>,
) -> Result<usize, String> {
    let metadata = match fs::symlink_metadata(path) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == ErrorKind::PermissionDenied => return Ok(0),
        Err(error) => return Err(format!("{}: {error}", path.display())),
    };
    if metadata.dev() != context.root_device {
        return Ok(0);
    }
    if metadata.file_type().is_symlink() {
        return Ok(0);
    }
    if excluded(path, context.root, context.excludes) {
        return Ok(0);
    }
    if metadata.is_file() {
        if context.max_file_bytes != 0 && metadata.len() > context.max_file_bytes {
            return Ok(0);
        }
        let bytes = match fs::read(path) {
            Ok(bytes) => bytes,
            Err(error) if error.kind() == ErrorKind::PermissionDenied => return Ok(0),
            Err(error) => return Err(format!("{}: {error}", path.display())),
        };
        if bytes.contains(&0) {
            return Ok(0);
        }
        let text = match String::from_utf8(bytes) {
            Ok(text) => text,
            Err(_) => return Ok(0),
        };
        if context.build_candidates {
            for gram in trigrams(&text.to_ascii_lowercase()) {
                map.entry(gram).or_default().insert(path.clone());
            }
        }
        documents.push(IndexedDocument {
            path: path.clone(),
            body: text,
        });
        return Ok(1);
    }
    if !metadata.is_dir() {
        return Ok(0);
    }
    let entries = match fs::read_dir(path) {
        Ok(entries) => entries,
        Err(error) if error.kind() == ErrorKind::PermissionDenied => return Ok(0),
        Err(error) => return Err(format!("{}: {error}", path.display())),
    };
    let mut count = 0;
    for entry in entries {
        let entry = match entry {
            Ok(entry) => entry,
            Err(error) if error.kind() == ErrorKind::PermissionDenied => continue,
            Err(error) => return Err(error.to_string()),
        };
        count += walk(&entry.path(), context, map, documents)?;
    }
    Ok(count)
}

fn compile_excludes(excludes: &[String]) -> Result<GlobSet, String> {
    let mut builder = GlobSetBuilder::new();
    for pattern in excludes {
        builder.add(
            Glob::new(pattern).map_err(|error| format!("invalid exclude {pattern}: {error}"))?,
        );
    }
    builder
        .build()
        .map_err(|error| format!("compile excludes: {error}"))
}

fn trigrams(value: &str) -> BTreeSet<String> {
    let bytes = value.as_bytes();
    (0..bytes.len().saturating_sub(2))
        .map(|i| String::from_utf8_lossy(&bytes[i..i + 3]).into_owned())
        .collect()
}

fn excluded(path: &Path, root: &Path, excludes: &GlobSet) -> bool {
    let matches = |candidate: &Path| {
        excludes.is_match(candidate)
            || candidate
                .strip_prefix(root)
                .map(|relative| excludes.is_match(relative))
                .unwrap_or(false)
    };
    matches(path) || matches(&path.join(".grepmesh-directory-probe"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;

    #[test]
    fn permission_denied_subtree_does_not_degrade_the_entire_index() {
        let root = tempfile::tempdir().unwrap();
        let readable = root.path().join("readable.txt");
        let denied = root.path().join("denied");
        fs::write(&readable, "INDEX_ACCESS_TOKEN\n").unwrap();
        fs::create_dir(&denied).unwrap();
        fs::write(denied.join("secret.txt"), "should-not-break-index\n").unwrap();
        let mut permissions = fs::metadata(&denied).unwrap().permissions();
        permissions.set_mode(0o000);
        fs::set_permissions(&denied, permissions).unwrap();

        let mut roots = BTreeMap::new();
        roots.insert("home".to_string(), vec![root.path().to_path_buf()]);
        let result = build_root_index(root.path(), &[], 0);

        let mut restore = fs::metadata(&denied).unwrap().permissions();
        restore.set_mode(0o755);
        fs::set_permissions(&denied, restore).unwrap();
        let (count, candidates) = result.unwrap();
        assert!(count >= 1);
        assert!(candidates
            .get("ind")
            .is_some_and(|paths| paths.contains(&readable)));
    }

    #[test]
    fn special_files_do_not_break_the_entire_index() {
        let root = tempfile::tempdir().unwrap();
        let readable = root.path().join("readable.txt");
        let fifo = root.path().join("console");
        fs::write(&readable, "INDEX_SPECIAL_FILE_TOKEN\n").unwrap();
        let status = std::process::Command::new("mkfifo")
            .arg(&fifo)
            .status()
            .unwrap();
        assert!(status.success());

        let mut roots = BTreeMap::new();
        roots.insert("home".to_string(), vec![root.path().to_path_buf()]);
        let (count, candidates) = build_root_index(root.path(), &[], 0).unwrap();

        assert_eq!(count, 1);
        assert!(candidates
            .get("ind")
            .is_some_and(|paths| paths.contains(&readable)));
    }

    #[test]
    fn roots_prioritize_home_before_other_named_roots() {
        let mut roots = BTreeMap::new();
        roots.insert("etc".to_string(), vec![PathBuf::from("/etc")]);
        roots.insert("home".to_string(), vec![PathBuf::from("/home/admin")]);
        roots.insert("opt".to_string(), vec![PathBuf::from("/opt")]);
        assert_eq!(
            ordered_roots(&roots),
            vec![
                PathBuf::from("/home/admin"),
                PathBuf::from("/opt"),
                PathBuf::from("/etc"),
            ]
        );
    }

    #[test]
    fn directory_exclusion_prunes_the_directory_itself() {
        let root = PathBuf::from("/workspace");
        let matcher = compile_excludes(&["**/.cache/**".to_string()]).unwrap();
        assert!(excluded(&root.join(".cache"), &root, &matcher));
    }

    #[test]
    fn persistent_index_replaces_documents_and_survives_reopen() {
        let dir = tempfile::tempdir().unwrap();
        let db = dir.path().join("index.sqlite");
        let document = dir.path().join("producer.rs");
        let index = PersistentIndex::open(db.clone()).unwrap();
        index
            .replace_document(&document, "pub struct HealthProducer")
            .unwrap();
        assert_eq!(
            index.candidates("HealthProducer").unwrap(),
            vec![document.clone()]
        );
        index
            .replace_document(&document, "replacement body")
            .unwrap();
        assert!(index.candidates("HealthProducer").unwrap().is_empty());
        assert_eq!(
            PersistentIndex::open(db)
                .unwrap()
                .candidates("replacement")
                .unwrap(),
            vec![document]
        );
    }
}
