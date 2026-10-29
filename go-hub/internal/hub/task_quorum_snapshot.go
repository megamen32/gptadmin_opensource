package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	moderncsqlite "modernc.org/sqlite"
)

type taskQuorumSnapshotFile struct {
	*os.File
	path string
}

func (f *taskQuorumSnapshotFile) Close() error {
	err := f.File.Close()
	removeErr := os.Remove(f.path)
	if err != nil {
		return err
	}
	if !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	return nil
}

func (s *Server) snapshotTaskQuorumState(sequence uint64) (io.ReadCloser, int64, error) {
	path := s.taskDatabasePath()
	if path == "" || sequence == 0 || s.quorumSnapshotMaxBytes <= 0 {
		return nil, 0, errors.New("task quorum snapshot is not configured")
	}
	if err := requireTaskQuorumSnapshotCapacity(filepath.Dir(path), s.quorumSnapshotMaxBytes, s.quorumSnapshotFreeReserve, 3); err != nil {
		return nil, 0, err
	}
	if err := requireTaskQuorumSnapshotCapacity(s.quorumSnapshotDataDir, s.quorumSnapshotMaxBytes, s.quorumSnapshotFreeReserve, 3); err != nil {
		return nil, 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, 0, err
	}
	destination, err := os.CreateTemp(filepath.Dir(path), ".tasks-quorum-snapshot-*.sqlite")
	if err != nil {
		return nil, 0, err
	}
	destinationPath := destination.Name()
	if err := destination.Close(); err != nil {
		_ = os.Remove(destinationPath)
		return nil, 0, err
	}
	removeDestination := true
	defer func() {
		if removeDestination {
			_ = os.Remove(destinationPath)
		}
	}()

	source, err := openTaskDatabase(path)
	if err != nil {
		return nil, 0, err
	}
	defer source.Close()
	var pageSize int64
	if err := source.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		return nil, 0, err
	}
	if pageSize <= 0 {
		return nil, 0, errors.New("task database has an invalid SQLite page size")
	}
	connection, err := source.Conn(context.Background())
	if err != nil {
		return nil, 0, err
	}
	defer connection.Close()
	if err := connection.Raw(func(driverConnection any) error {
		backuper, ok := driverConnection.(interface {
			NewBackup(string) (*moderncsqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite driver does not support online backups")
		}
		backup, err := backuper.NewBackup(destinationPath)
		if err != nil {
			return err
		}
		finished := false
		defer func() {
			if !finished {
				_ = backup.Finish()
			}
		}()
		for {
			more, err := backup.Step(128)
			if err != nil {
				return err
			}
			if pages := int64(backup.PageCount()); pages > 0 && pages > s.quorumSnapshotMaxBytes/pageSize {
				return fmt.Errorf("task database snapshot exceeds configured byte limit %d", s.quorumSnapshotMaxBytes)
			}
			if !more {
				break
			}
		}
		finished = true
		return backup.Finish()
	}); err != nil {
		return nil, 0, err
	}
	if info, err := os.Stat(destinationPath); err != nil {
		return nil, 0, err
	} else if info.Size() <= 0 || info.Size() > s.quorumSnapshotMaxBytes {
		return nil, 0, fmt.Errorf("task database snapshot size %d exceeds configured byte limit %d", info.Size(), s.quorumSnapshotMaxBytes)
	}
	var clusterID string
	var applied uint64
	if err := withTaskSnapshotRead(destinationPath, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT cluster_id,applied_index FROM task_quorum_meta WHERE id=1`).Scan(&clusterID, &applied); err != nil {
			return err
		}
		return taskQuorumSnapshotChainMatches(tx, sequence)
	}); err != nil {
		return nil, 0, err
	}
	if clusterID != s.quorumClusterID || applied != sequence {
		return nil, 0, fmt.Errorf("task database snapshot sequence mismatch: cluster=%q sequence=%d expected=%d", clusterID, applied, sequence)
	}
	file, err := os.Open(destinationPath)
	if err != nil {
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	removeDestination = false
	return &taskQuorumSnapshotFile{File: file, path: destinationPath}, info.Size(), nil
}

func withTaskSnapshotRead(path string, fn func(*sql.Tx) error) error {
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
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) restoreTaskQuorumState(sequence uint64, source io.Reader, size int64) error {
	path := s.taskDatabasePath()
	if path == "" || sequence == 0 || size <= 0 || size > s.quorumSnapshotMaxBytes {
		return errors.New("task quorum snapshot restore is not configured or exceeds its byte limit")
	}
	if err := requireTaskQuorumSnapshotCapacity(filepath.Dir(path), s.quorumSnapshotMaxBytes, s.quorumSnapshotFreeReserve, 2); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tasks-quorum-restore-*.sqlite")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := io.CopyN(temporary, source, size); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("copy task quorum database snapshot: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	var clusterID string
	var applied uint64
	if err := withTaskSnapshotRead(temporaryPath, func(tx *sql.Tx) error {
		var integrity string
		if err := tx.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
			return err
		}
		if integrity != "ok" {
			return fmt.Errorf("task quorum snapshot failed SQLite integrity_check: %s", integrity)
		}
		if err := tx.QueryRow(`SELECT cluster_id,applied_index FROM task_quorum_meta WHERE id=1`).Scan(&clusterID, &applied); err != nil {
			return err
		}
		return taskQuorumSnapshotChainMatches(tx, sequence)
	}); err != nil {
		return err
	}
	if clusterID != s.quorumClusterID || applied != sequence {
		return fmt.Errorf("task quorum restore image mismatch: cluster=%q sequence=%d expected=%d", clusterID, applied, sequence)
	}

	s.taskQuorumDBMu.Lock()
	defer s.taskQuorumDBMu.Unlock()
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
	if _, err := tx.Exec(`ATTACH DATABASE ? AS quorum_snapshot`, temporaryPath); err != nil {
		return fmt.Errorf("attach task quorum snapshot: %w", err)
	}
	var currentCluster string
	if err := tx.QueryRow(`SELECT cluster_id FROM task_quorum_meta WHERE id=1`).Scan(&currentCluster); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if currentCluster != "" && currentCluster != s.quorumClusterID {
		return errors.New("local task database belongs to a different quorum cluster")
	}
	// A restored FSM snapshot can precede newer rows already committed to this
	// local SQLite file. Replace the file's task image with the exact snapshot;
	// Raft replays its retained log suffix before the node can pass admission.
	for _, table := range []string{"task_records", "task_controls", "task_requests", "task_quorum_authority", "task_quorum_revisions", "task_quorum_meta", "task_quorum_chain"} {
		if _, err := tx.Exec(`DELETE FROM ` + table); err != nil {
			return fmt.Errorf("clear local %s before snapshot restore: %w", table, err)
		}
	}
	for _, insert := range []string{
		`INSERT INTO task_records(kind,key,updated,done,payload,revision) SELECT kind,key,updated,done,payload,revision FROM quorum_snapshot.task_records`,
		`INSERT INTO task_controls(task_id,server,next_attempt) SELECT task_id,server,next_attempt FROM quorum_snapshot.task_controls`,
		`INSERT INTO task_requests(key,fingerprint,owner,created,job_id,response,status) SELECT key,fingerprint,owner,created,job_id,response,status FROM quorum_snapshot.task_requests`,
		`INSERT INTO task_quorum_authority(namespace,row_key,payload,revision) SELECT namespace,row_key,payload,revision FROM quorum_snapshot.task_quorum_authority`,
		`INSERT INTO task_quorum_revisions(namespace,row_key,revision) SELECT namespace,row_key,revision FROM quorum_snapshot.task_quorum_revisions`,
		`INSERT INTO task_quorum_meta(id,cluster_id,applied_index,baseline_ready,authority_ready,authority_revision) SELECT id,cluster_id,applied_index,baseline_ready,authority_ready,authority_revision FROM quorum_snapshot.task_quorum_meta`,
		`INSERT INTO task_quorum_chain(id,sequence,digest) SELECT id,sequence,digest FROM quorum_snapshot.task_quorum_chain`,
	} {
		if _, err := tx.Exec(insert); err != nil {
			return fmt.Errorf("copy task quorum snapshot rows: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task quorum snapshot restore: %w", err)
	}
	s.taskQuorumReplayMu.Lock()
	s.taskQuorumReplay = taskQuorumReplay{}
	s.taskQuorumReplayMu.Unlock()
	return nil
}
