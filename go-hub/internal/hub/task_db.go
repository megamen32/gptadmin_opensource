package hub

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
	_ "modernc.org/sqlite"
)

const taskDatabaseFilename = "tasks_state.sqlite"

const pruneDoneRecordsSQL = `SELECT kind,key FROM task_records INDEXED BY task_records_done WHERE done>0 AND done<? ORDER BY done LIMIT 128`

// Each operation owns a bounded connection. There is no background writer,
// debounce window, process-lifetime file lock, or unclosed per-Server DB pool.
// SQLite serializes primary/standby writers; the transaction is durable before
// the caller acknowledges the operation. FULL must not be changed to NORMAL.
var errTaskConflict = errors.New("task changed in another Hub; reload and retry")

var errTaskSchemaNeedsInit = errors.New("task database needs initialization")

type taskTransaction struct {
	tx                     *sql.Tx
	revisions              map[string]int64
	quorumChanges          map[string]quorum.Mutation
	quorumEnabled          bool
	quorumIndex            uint64
	quorumBatch            []quorum.Mutation
	quorumAuthorityChanges int
}

type quorumTaskRecordRow struct {
	Kind     string  `json:"kind"`
	Key      string  `json:"key"`
	Updated  float64 `json:"updated"`
	Done     float64 `json:"done"`
	Payload  []byte  `json:"payload"`
	Revision int64   `json:"revision"`
}

type quorumTaskControlRow struct {
	TaskID      string  `json:"task_id"`
	Server      string  `json:"server"`
	NextAttempt float64 `json:"next_attempt"`
}

type quorumTaskRequestRow struct {
	Key         string  `json:"key"`
	Fingerprint string  `json:"fingerprint"`
	Owner       string  `json:"owner"`
	Created     float64 `json:"created"`
	JobID       string  `json:"job_id"`
	Response    []byte  `json:"response,omitempty"`
	Status      int     `json:"status"`
}

func (t *taskTransaction) captureQuorumRow(namespace, key string, value any, deleted bool) error {
	if !t.quorumEnabled {
		return nil
	}
	if t.quorumChanges == nil {
		t.quorumChanges = map[string]quorum.Mutation{}
	}
	var previous int64
	err := t.tx.QueryRow(`SELECT revision FROM task_quorum_revisions WHERE namespace=? AND row_key=?`, namespace, key).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	encoded := []byte(nil)
	if !deleted {
		encoded, err = json.Marshal(value)
		if err != nil {
			return err
		}
	}
	next := previous + 1
	if _, err := t.tx.Exec(`INSERT INTO task_quorum_revisions(namespace,row_key,revision) VALUES(?,?,?) ON CONFLICT(namespace,row_key) DO UPDATE SET revision=excluded.revision`, namespace, key, next); err != nil {
		return err
	}
	mapKey := namespace + "\x00" + key
	t.quorumChanges[mapKey] = quorum.Mutation{
		Namespace: namespace, Key: key, External: true,
		Record: quorum.Record{Revision: uint64(next), Value: encoded, Deleted: deleted},
	}
	return nil
}

func (t *taskTransaction) captureTaskRequest(key string) error {
	var row quorumTaskRequestRow
	err := t.tx.QueryRow(`SELECT key,fingerprint,owner,created,job_id,response,status FROM task_requests WHERE key=?`, key).Scan(
		&row.Key, &row.Fingerprint, &row.Owner, &row.Created, &row.JobID, &row.Response, &row.Status,
	)
	if err != nil {
		return err
	}
	return t.captureQuorumRow("task_requests", key, row, false)
}

func (t *taskTransaction) captureTaskControl(taskID string) error {
	var row quorumTaskControlRow
	err := t.tx.QueryRow(`SELECT task_id,server,next_attempt FROM task_controls WHERE task_id=?`, taskID).Scan(
		&row.TaskID, &row.Server, &row.NextAttempt,
	)
	if err != nil {
		return err
	}
	return t.captureQuorumRow("task_controls", taskID, row, false)
}

func (s *Server) taskDatabasePath() string {
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, taskDatabaseFilename)
}

// Serialize file creation only. Opening/closing an existing SQLite file with
// os.File can release POSIX record locks held by other connections in this
// process. Existing databases must be opened exclusively by the SQLite driver.
var taskDatabaseCreateMu sync.Mutex

func ensureTaskDatabaseFile(path string) error {
	taskDatabaseCreateMu.Lock()
	defer taskDatabaseCreateMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return f.Close()
}

func openTaskDatabase(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := ensureTaskDatabaseFile(path); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// Normal task polling owns a deferred, read-only WAL snapshot. It must not
// compete with durable mutations for SQLite's single writer or rerun DDL.
func openTaskReadDatabase(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "query_only(1)")
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

func (s *Server) withTaskReadDatabase(fn func(*sql.Tx) error) error {
	path := s.taskDatabasePath()
	if path == "" {
		return nil
	}
	s.taskQuorumDBMu.RLock()
	defer s.taskQuorumDBMu.RUnlock()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return errTaskSchemaNeedsInit
	} else if err != nil {
		return err
	}
	db, err := openTaskReadDatabase(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='task_store_meta'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return errTaskSchemaNeedsInit
	}
	var version int
	err = tx.QueryRow(`SELECT version FROM task_store_meta WHERE id=1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && version == 1) {
		return errTaskSchemaNeedsInit
	}
	if err != nil {
		return err
	}
	if version != 2 {
		return fmt.Errorf("unsupported task database version %d", version)
	}
	// Upgrade derived inventory indexes atomically through the existing writer
	// before publishing a ready reader; old binaries remain rollback-compatible.
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name IN ('task_records_inventory','task_records_status','task_records_server_page')`).Scan(&exists); err != nil {
		return err
	}
	if exists != 3 {
		return errTaskSchemaNeedsInit
	}
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE (type='table' AND name='task_status_counts') OR (type='trigger' AND name IN ('task_counts_insert','task_counts_delete','task_counts_update_remove','task_counts_update_add'))`).Scan(&exists); err != nil {
		return err
	}
	if exists != 5 {
		return errTaskSchemaNeedsInit
	}
	if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('task_status_counts') WHERE name='server'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return errTaskSchemaNeedsInit
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) withTaskDatabase(fn func(*taskTransaction) error) error {
	path := s.taskDatabasePath()
	if path == "" {
		return nil
	}
	s.taskQuorumDBMu.RLock()
	locked := true
	defer func() {
		if locked {
			s.taskQuorumDBMu.RUnlock()
		}
	}()
	db, err := openTaskDatabase(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS task_records (
    kind TEXT NOT NULL, key TEXT NOT NULL, updated REAL NOT NULL,
    done REAL NOT NULL DEFAULT 0, payload BLOB NOT NULL, revision INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(kind,key)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS task_records_done ON task_records(done) WHERE done > 0`,
		`CREATE INDEX IF NOT EXISTS task_records_updated ON task_records(kind,updated)`,
		`CREATE INDEX IF NOT EXISTS task_records_inventory ON task_records(json_extract(payload,'$.created_at') DESC,key DESC) WHERE kind IN ('shell','relay')`,
		`CREATE INDEX IF NOT EXISTS task_records_status ON task_records(kind,json_extract(payload,'$.status')) WHERE kind IN ('shell','relay')`,
		`CREATE INDEX IF NOT EXISTS task_records_server_page ON task_records(json_extract(payload,'$.server'),json_extract(payload,'$.created_at') DESC,key DESC) WHERE kind='shell'`,
		`CREATE TABLE IF NOT EXISTS task_controls (task_id TEXT PRIMARY KEY, server TEXT NOT NULL, next_attempt REAL NOT NULL DEFAULT 0)`,
		`CREATE INDEX IF NOT EXISTS task_controls_server ON task_controls(server,next_attempt)`,
		`CREATE TABLE IF NOT EXISTS task_requests (key TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, owner TEXT NOT NULL, created REAL NOT NULL, job_id TEXT NOT NULL DEFAULT '', response BLOB, status INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS task_store_meta (id INTEGER PRIMARY KEY CHECK(id=1), version INTEGER NOT NULL)`,
	} {
		if _, err = tx.Exec(ddl); err != nil {
			return err
		}
	}
	if s.cfg.QuorumConfigFile != "" {
		for _, ddl := range []string{
			`CREATE TABLE IF NOT EXISTS task_quorum_revisions (namespace TEXT NOT NULL, row_key TEXT NOT NULL, revision INTEGER NOT NULL, PRIMARY KEY(namespace,row_key)) WITHOUT ROWID`,
			`CREATE TABLE IF NOT EXISTS task_quorum_meta (id INTEGER PRIMARY KEY CHECK(id=1), cluster_id TEXT NOT NULL DEFAULT '', applied_index INTEGER NOT NULL DEFAULT 0, baseline_ready INTEGER NOT NULL DEFAULT 0, authority_ready INTEGER NOT NULL DEFAULT 0)`,
			`CREATE TABLE IF NOT EXISTS task_quorum_authority (namespace TEXT NOT NULL, row_key TEXT NOT NULL, payload BLOB NOT NULL, revision INTEGER NOT NULL, PRIMARY KEY(namespace,row_key)) WITHOUT ROWID`,
		} {
			if _, err = tx.Exec(ddl); err != nil {
				return err
			}
		}
		var authorityRevisionColumn int
		if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('task_quorum_meta') WHERE name='authority_revision'`).Scan(&authorityRevisionColumn); err != nil {
			return err
		}
		if authorityRevisionColumn == 0 {
			if _, err := tx.Exec(`ALTER TABLE task_quorum_meta ADD COLUMN authority_revision INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
	}
	if err = ensureTaskCountSchema(tx); err != nil {
		return err
	}
	var version int
	err = tx.QueryRow(`SELECT version FROM task_store_meta WHERE id=1`).Scan(&version)
	store := &taskTransaction{tx: tx, quorumChanges: map[string]quorum.Mutation{}, quorumEnabled: s.cfg.QuorumConfigFile != ""}
	if errors.Is(err, sql.ErrNoRows) {
		// The legacy snapshot is a read-only migration source/rollback backup. Import
		// and the migration marker share one transaction, including after a crash.
		err = withStateFileLock(s.taskStatePath(), func() error {
			data, readErr := os.ReadFile(s.taskStatePath())
			if errors.Is(readErr, os.ErrNotExist) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			var legacy persistedTaskState
			if err := json.Unmarshal(data, &legacy); err != nil {
				return fmt.Errorf("task snapshot migration: %w", err)
			}
			return store.save(legacy)
		})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO task_store_meta(id,version) VALUES(1,2)`); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if version == 1 {
		if _, err = tx.Exec(`ALTER TABLE task_records ADD COLUMN revision INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE task_store_meta SET version=2 WHERE id=1`); err != nil {
			return err
		}
	} else if version != 2 {
		return fmt.Errorf("unsupported task database version %d", version)
	}
	if err = fn(store); err != nil {
		return err
	}
	if err = s.commitTaskQuorumChanges(store); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		if store.quorumIndex != 0 {
			s.taskQuorumDBMu.RUnlock()
			locked = false
			if reconcileErr := s.applyTaskQuorumMutations(0, store.quorumIndex, store.quorumBatch); reconcileErr != nil {
				return fmt.Errorf("task SQLite commit failed after quorum commit (%v); local quorum reconciliation failed: %w", err, reconcileErr)
			}
		}
		return err
	}
	return nil
}

func (t *taskTransaction) save(state persistedTaskState) error {
	t.revisions = map[string]int64{}
	changed := map[string]bool{}
	put := func(kind, key string, updated, done float64, revision int64, value any) error {
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		var oldPayload []byte
		var oldRevision int64
		err = t.tx.QueryRow(`SELECT payload,revision FROM task_records WHERE kind=? AND key=?`, kind, key).Scan(&oldPayload, &oldRevision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && bytes.Equal(payload, oldPayload) {
			t.revisions[kind+":"+key] = oldRevision
			return nil
		}
		if kind != "idempotency" && ((err == nil && oldRevision != revision) || (errors.Is(err, sql.ErrNoRows) && revision != 0)) {
			return fmt.Errorf("%w: %s/%s", errTaskConflict, kind, key)
		}
		next := oldRevision + 1
		if errors.Is(err, sql.ErrNoRows) {
			_, err = t.tx.Exec(`INSERT INTO task_records(kind,key,updated,done,payload,revision) VALUES(?,?,?,?,?,?)`, kind, key, updated, done, payload, next)
		} else {
			_, err = t.tx.Exec(`UPDATE task_records SET updated=?,done=?,payload=?,revision=? WHERE kind=? AND key=?`, updated, done, payload, next, kind, key)
		}
		if err != nil {
			return err
		}
		if err := t.captureQuorumRow("task_records/"+kind, key, quorumTaskRecordRow{
			Kind: kind, Key: key, Updated: updated, Done: done, Payload: payload, Revision: next,
		}, false); err != nil {
			return err
		}
		changed[kind+":"+key] = true
		t.revisions[kind+":"+key] = next
		return nil
	}
	for key, p := range state.Relay {
		if p.Revision == 0 {
			if err := t.linkRequestTask(p.RequestKey, p.ID); err != nil {
				return err
			}
		}
		if err := put("relay", key, persistedTaskUpdated(p.CreatedAt, p.StartedAt, p.DoneAt), p.DoneAt, p.Revision, p); err != nil {
			return err
		}
	}
	for key, p := range state.Shell {
		if p.Revision == 0 {
			if err := t.linkRequestTask(p.RequestKey, p.ID); err != nil {
				return err
			}
		}
		if err := put("shell", key, persistedTaskUpdated(p.CreatedAt, p.StartedAt, p.DoneAt), p.DoneAt, p.Revision, p); err != nil {
			return err
		}
		if changed["shell:"+key] && p.Status == "cancelled" && p.StartedAt > 0 && p.Server != "" {
			result, err := t.tx.Exec(`INSERT OR IGNORE INTO task_controls(task_id,server) VALUES(?,?)`, p.ID, p.Server)
			if err != nil {
				return err
			}
			if rows, err := result.RowsAffected(); err != nil {
				return err
			} else if rows > 0 {
				if err := t.captureQuorumRow("task_controls", p.ID, quorumTaskControlRow{TaskID: p.ID, Server: p.Server}, false); err != nil {
					return err
				}
			}
		}

	}
	for key, p := range state.Idempotency {
		if err := put("idempotency", key, float64(p.CreatedAt.UnixNano())/1e9, 0, 0, p); err != nil {
			return err
		}
	}
	return nil
}

func (t *taskTransaction) prune(taskCutoff, idempotencyCutoff float64) error {
	rows, err := t.tx.Query(pruneDoneRecordsSQL, taskCutoff)
	if err != nil {
		return err
	}
	type keyRevision struct {
		kind, key string
		revision  int64
	}
	var expired []keyRevision
	for rows.Next() {
		var row keyRevision
		if err := rows.Scan(&row.kind, &row.key); err != nil {
			_ = rows.Close()
			return err
		}
		expired = append(expired, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, row := range expired {
		if row.kind == "shell" {
			var control quorumTaskControlRow
			err := t.tx.QueryRow(`SELECT task_id,server,next_attempt FROM task_controls WHERE task_id=?`, row.key).Scan(&control.TaskID, &control.Server, &control.NextAttempt)
			if err == nil {
				if err := t.captureQuorumRow("task_controls", row.key, nil, true); err != nil {
					return err
				}
				if _, err := t.tx.Exec(`DELETE FROM task_controls WHERE task_id=?`, row.key); err != nil {
					return err
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if err := t.captureQuorumRow("task_records/"+row.kind, row.key, nil, true); err != nil {
			return err
		}
		if _, err := t.tx.Exec(`DELETE FROM task_records WHERE kind=? AND key=?`, row.kind, row.key); err != nil {
			return err
		}
	}
	rows, err = t.tx.Query(`SELECT key,revision FROM task_records WHERE kind='idempotency' AND updated<? ORDER BY updated,key LIMIT 128`, idempotencyCutoff)
	if err != nil {
		return err
	}
	expired = expired[:0]
	for rows.Next() {
		var row keyRevision
		row.kind = "idempotency"
		if err := rows.Scan(&row.key, &row.revision); err != nil {
			_ = rows.Close()
			return err
		}
		expired = append(expired, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, row := range expired {
		if err := t.captureQuorumRow("task_records/"+row.kind, row.key, nil, true); err != nil {
			return err
		}
		if _, err := t.tx.Exec(`DELETE FROM task_records WHERE kind=? AND key=?`, row.kind, row.key); err != nil {
			return err
		}
	}
	return nil
}

// readTaskState is a full export/test read; startup and normal saves never
// load or serialize unrelated completed results.
func (s *Server) readTaskState() (persistedTaskState, error) { return s.readTaskRecords() }

func (s *Server) readTaskRecords(ids ...string) (persistedTaskState, error) {
	return s.readTaskRecordsQuery("", false, ids...)
}

func (s *Server) readTaskRecordsQuery(where string, metadata bool, ids ...string) (persistedTaskState, error) {
	return s.readTaskRecordsSQL(where, metadata, nil, ids...)
}

func (s *Server) readTaskRecordsSQL(where string, metadata bool, sqlArgs []any, ids ...string) (persistedTaskState, error) {
	state := persistedTaskState{Relay: map[string]persistedRelayTask{}, Shell: map[string]persistedShellTask{}, Idempotency: map[string]persistedIdempotencyEntry{}}
	read := func(tx *sql.Tx) error {
		payload := "payload"
		if metadata {
			payload = `json_object('id',key,'server',json_extract(payload,'$.server'),'agent_id',json_extract(payload,'$.agent_id'),'method',json_extract(payload,'$.method'),'params',json_object('name',json_extract(payload,'$.params.name')),'tool_name',json_extract(payload,'$.tool_name'),'created_at',json_extract(payload,'$.created_at'),'started_at',json_extract(payload,'$.started_at'),'completed_at',done,'status',json_extract(payload,'$.status'))`
		}
		query := `SELECT kind,key,` + payload + `,revision FROM task_records`
		if metadata && strings.Contains(where, "ORDER BY") {
			index := "task_records_inventory"
			if strings.Contains(where, "$.status") {
				index = "task_records_status"
			}
			if strings.Contains(where, "$.server") {
				index = "task_records_server_page"
			}
			query += " INDEXED BY " + index
		}
		if where != "" {
			query += " WHERE " + where
		}
		args := append([]any(nil), sqlArgs...)
		if len(ids) > 0 {
			query += ` WHERE kind IN ('shell','relay') AND key IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
			for _, id := range ids {
				args = append(args, id)
			}
		}
		rows, err := tx.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind, key string
			var payload []byte
			var revision int64
			if err := rows.Scan(&kind, &key, &payload, &revision); err != nil {
				return err
			}
			switch kind {
			case "relay":
				var p persistedRelayTask
				if err = json.Unmarshal(payload, &p); err != nil {
					return err
				}
				p.Revision = revision
				state.Relay[key] = p
			case "shell":
				var p persistedShellTask
				if err = json.Unmarshal(payload, &p); err != nil {
					return err
				}
				p.Revision = revision
				state.Shell[key] = p
			case "idempotency":
				var p persistedIdempotencyEntry
				if err = json.Unmarshal(payload, &p); err != nil {
					return err
				}
				state.Idempotency[key] = p
			default:
				return fmt.Errorf("unknown persisted task kind %q", kind)
			}
		}
		return rows.Err()
	}
	err := s.withTaskReadDatabase(read)
	if errors.Is(err, errTaskSchemaNeedsInit) {
		// Only cold/legacy stores need the existing atomic migration writer.
		// Close the read transaction before taking the writer lock.
		if err = s.withTaskDatabase(func(*taskTransaction) error { return nil }); err == nil {
			err = s.withTaskReadDatabase(read)
		}
	}
	return state, err
}
