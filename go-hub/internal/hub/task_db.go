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

	_ "modernc.org/sqlite"
)

const taskDatabaseFilename = "tasks_state.sqlite"

// Each operation owns a bounded connection. There is no background writer,
// debounce window, process-lifetime file lock, or unclosed per-Server DB pool.
// SQLite serializes primary/standby writers; the transaction is durable before
// the caller acknowledges the operation. FULL must not be changed to NORMAL.
var errTaskConflict = errors.New("task changed in another Hub; reload and retry")

type taskTransaction struct {
	tx        *sql.Tx
	revisions map[string]int64
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

func (s *Server) withTaskDatabase(fn func(*taskTransaction) error) error {
	path := s.taskDatabasePath()
	if path == "" {
		return nil
	}
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
		`CREATE TABLE IF NOT EXISTS task_controls (task_id TEXT PRIMARY KEY, server TEXT NOT NULL, next_attempt REAL NOT NULL DEFAULT 0)`,
		`CREATE INDEX IF NOT EXISTS task_controls_server ON task_controls(server,next_attempt)`,
		`CREATE TABLE IF NOT EXISTS task_requests (key TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, owner TEXT NOT NULL, created REAL NOT NULL, job_id TEXT NOT NULL DEFAULT '', response BLOB, status INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS task_store_meta (id INTEGER PRIMARY KEY CHECK(id=1), version INTEGER NOT NULL)`,
	} {
		if _, err = tx.Exec(ddl); err != nil {
			return err
		}
	}
	var version int
	err = tx.QueryRow(`SELECT version FROM task_store_meta WHERE id=1`).Scan(&version)
	store := &taskTransaction{tx: tx}
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
	return tx.Commit()
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
			if _, err := t.tx.Exec(`INSERT OR IGNORE INTO task_controls(task_id,server) VALUES(?,?)`, p.ID, p.Server); err != nil {
				return err
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
	if _, err := t.tx.Exec(`DELETE FROM task_controls WHERE task_id IN (SELECT key FROM task_records WHERE kind='shell' AND done>0 AND done<?)`, taskCutoff); err != nil {
		return err
	}
	if _, err := t.tx.Exec(`DELETE FROM task_records WHERE done>0 AND done<?`, taskCutoff); err != nil {
		return err
	}
	_, err := t.tx.Exec(`DELETE FROM task_records WHERE kind='idempotency' AND updated<?`, idempotencyCutoff)
	return err
}

// readTaskState is the only full read, on restart/export; normal saves never
// load or serialize unrelated completed results.
func (s *Server) readTaskState() (persistedTaskState, error) { return s.readTaskRecords() }

func (s *Server) readTaskRecords(ids ...string) (persistedTaskState, error) {
	state := persistedTaskState{Relay: map[string]persistedRelayTask{}, Shell: map[string]persistedShellTask{}, Idempotency: map[string]persistedIdempotencyEntry{}}
	err := s.withTaskDatabase(func(t *taskTransaction) error {
		query := `SELECT kind,key,payload,revision FROM task_records`
		args := []any{}
		if len(ids) > 0 {
			query += ` WHERE kind IN ('shell','relay') AND key IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
			for _, id := range ids {
				args = append(args, id)
			}
		}
		rows, err := t.tx.Query(query, args...)
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
	})
	return state, err
}
