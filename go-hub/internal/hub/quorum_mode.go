package hub

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

const quorumControlCapability = "quorum-control-only-v1"

func (s *Server) quorumControlOnly() bool { return s.quorumMode == quorum.OwnerLocal }

type quorumStorageIdentity struct {
	ClusterID string `json:"cluster_id"`
	Mode      string `json:"mode"`
}

func readPrivateControlFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, errors.New("quorum: private bounded regular file required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("quorum: input changed before opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, errors.New("quorum: oversized input")
	}
	return data, err
}

func readHubQuorumConfig(path string) (quorum.Config, error) {
	var c quorum.Config
	data, err := readPrivateControlFile(path, 256<<10)
	if err != nil {
		return c, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil || d.Decode(new(any)) != io.EOF {
		return c, errors.New("invalid quorum config")
	}
	return c, nil
}

// An unmarked Raft directory predating owner_local is ambiguous until a
// committed legacy baseline is visible. Never decide from historical row counts.
func (s *Server) resolveQuorumMode(c quorum.Config) (string, bool, error) {
	if c.ClusterID == "" || strings.TrimSpace(c.ClusterID) != c.ClusterID || len(c.ClusterID) > 256 || strings.ContainsRune(c.ClusterID, 0) ||
		(c.Mode != "" && c.Mode != quorum.Replicated && c.Mode != quorum.OwnerLocal) {
		return "", false, errors.New("invalid quorum mode or cluster identity")
	}
	idPath := filepath.Join(c.DataDir, "hub-storage-mode.json")
	data, err := readPrivateControlFile(idPath, 1024)
	if err == nil {
		var id quorumStorageIdentity
		if json.Unmarshal(data, &id) != nil || id.ClusterID != c.ClusterID || (id.Mode != quorum.OwnerLocal && id.Mode != quorum.Replicated) || (c.Mode != "" && c.Mode != id.Mode) {
			return "", false, errors.New("quorum storage identity mismatch")
		}
		return id.Mode, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	for _, name := range []string{"hub-control.sqlite", "raft.db", "snapshots"} {
		_, err = os.Lstat(filepath.Join(c.DataDir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		if name == "hub-control.sqlite" || c.Mode == quorum.OwnerLocal {
			return "", false, errors.New("owner_local requires a fresh or pinned Raft directory")
		}
		if c.Mode == quorum.Replicated {
			return quorum.Replicated, false, nil
		}
		// This bit is set only by application of the committed cluster marker.
		// Reopen still waits for the actual Raft marker before admission.
		// The committed baseline, the committed authority rows and a sealed
		// history checkpoint are the evidence. The last applied task sequence
		// and the checkpoint sequence are independent counters: a baseline seed
		// or an authority-only commit moves one without the other, so requiring
		// them to be equal would refuse every real legacy cluster.
		var ready int
		err = withTaskSnapshotRead(s.taskDatabasePath(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT count(*) FROM task_quorum_meta m JOIN task_quorum_chain c USING(id) WHERE m.id=1 AND cluster_id=? AND baseline_ready=1 AND authority_ready=1 AND c.sequence>0 AND length(digest)=32`, c.ClusterID).Scan(&ready)
		})
		if err != nil || ready != 1 {
			return "", false, errors.New("unmarked Raft state has no committed legacy baseline evidence; explicit replicated selection required")
		}
		return quorum.Replicated, true, nil
	}
	if c.Mode == quorum.Replicated {
		return c.Mode, false, nil
	}
	return quorum.OwnerLocal, false, nil
}

// Publish a complete identity before creating the first Raft or control DB.
// Concurrent opens may agree on this identity but cannot replace it.
func pinQuorumMode(dir string, id quorumStorageIdentity) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("quorum directory must be private")
	}
	path := filepath.Join(dir, "hub-storage-mode.json")
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	match := func() error {
		old, e := readPrivateControlFile(path, 1024)
		if e != nil {
			return e
		}
		if !bytes.Equal(old, data) {
			return errors.New("quorum storage identity mismatch")
		}
		return nil
	}
	if err := match(); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir, ".hub-storage-mode-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Link(f.Name(), path); errors.Is(err, os.ErrExist) {
		return match()
	} else if err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *Server) quorumDatabasePath() string {
	if s.quorumControlOnly() {
		return filepath.Join(s.quorumSnapshotDataDir, "hub-control.sqlite")
	}
	return s.taskDatabasePath()
}
func (s *Server) withQuorumReadDatabase(fn func(*sql.Tx) error) error {
	if !s.quorumControlOnly() {
		return s.withTaskReadDatabase(fn)
	}
	s.quorumControlDBMu.RLock()
	defer s.quorumControlDBMu.RUnlock()
	return withTaskSnapshotRead(s.quorumDatabasePath(), fn)
}
func (s *Server) withQuorumDatabase(fn func(*taskTransaction) error) error {
	if !s.quorumControlOnly() {
		return s.withTaskDatabase(fn)
	}
	return s.withStoreTransaction(s.quorumDatabasePath(), true, fn)
}

const controlApplicationID = 0x47514331

func initializeControlSchema(tx *sql.Tx) error {
	var app, tables int
	if err := tx.QueryRow("PRAGMA application_id").Scan(&app); err != nil {
		return err
	}
	if app == controlApplicationID {
		return nil
	}
	if err := tx.QueryRow("SELECT count(*) FROM sqlite_schema").Scan(&tables); err != nil {
		return err
	}
	if app != 0 || tables != 0 {
		return errors.New("refusing a foreign database as quorum control storage")
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA application_id=%d", controlApplicationID)); err != nil {
		return err
	}
	return ensureTaskQuorumSchema(tx)
}

func (s *Server) legacyMarkerCommitted() bool {
	rec, ok := s.quorumNode.LocalRecord("cluster_meta", "task_baseline")
	var marker struct {
		ClusterID     string `json:"cluster_id"`
		Mode          string `json:"mode"`
		ChainSequence uint64 `json:"chain_sequence"`
	}
	return ok && !rec.Deleted && json.Unmarshal(rec.Value, &marker) == nil && marker.ClusterID == s.quorumClusterID && (marker.Mode == "" || marker.Mode == quorum.Replicated) && marker.ChainSequence > 0
}

// Reject a substituted task database before opening it as a writable WAL
// database. A partial first initialization can have a zero application_id in
// the main header; consult only the committed schema through SQLite then.
func checkControlDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 2*quorum.ControlSnapshotLimit {
		return errors.New("incompatible or oversized control database")
	}
	if info.Size() == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var header [100]byte
	_, err = io.ReadFull(f, header[:])
	_ = f.Close()
	if err != nil || string(header[:16]) != "SQLite format 3\x00" {
		return errors.New("invalid control database header")
	}
	id := binary.BigEndian.Uint32(header[68:72])
	if id != 0 && id != controlApplicationID {
		return errors.New("foreign database format")
	}
	return withTaskSnapshotRead(path, func(tx *sql.Tx) error {
		var app, n, other int
		if err := tx.QueryRow("PRAGMA application_id").Scan(&app); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT count(*),coalesce(sum(CASE WHEN type='table' AND name IN ('task_quorum_meta','task_quorum_revisions','task_quorum_chain','task_quorum_authority') THEN 0 ELSE 1 END),0) FROM sqlite_schema WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\'`).Scan(&n, &other); err != nil {
			return err
		}
		if app == 0 && n == 0 {
			return nil
		}
		if app != controlApplicationID || n != 4 || other != 0 {
			return errors.New("control database contains a foreign schema")
		}
		return nil
	})
}
