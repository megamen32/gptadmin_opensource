package hub

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

// The external control image is a sequence of bounded frames. Operations
// already live in the Raft FSM header. No SQLite pages enter this format.
const controlImageMagic = "GPTCTRL1\n"
const controlFrameLimit = quorum.ControlAuthorityLimit + 4096

type controlHeader struct {
	Cluster           string `json:"cluster"`
	Sequence          uint64 `json:"sequence"`
	Digest            []byte `json:"digest"`
	AuthorityRevision uint64 `json:"authority_revision"`
	Ready             int    `json:"ready"`
	Rows              int    `json:"rows"`
}

func writeControlFrame(w io.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(raw) > controlFrameLimit {
		return errors.New("control snapshot frame exceeds its limit")
	}
	if err = binary.Write(w, binary.BigEndian, uint32(len(raw))); err != nil {
		return err
	}
	_, err = w.Write(raw)
	return err
}
func readControlFrame(r io.Reader, max int, v any) error {
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return err
	}
	if n == 0 || uint64(n) > uint64(max) {
		return errors.New("invalid control snapshot frame length")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(r, raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return errors.New("invalid control snapshot frame")
	}
	// Reject unknown fields, duplicate fields and noncanonical persisted bytes.
	canonical, err := json.Marshal(v)
	if err != nil || !bytes.Equal(raw, canonical) {
		return errors.New("unexpected fields in control snapshot frame")
	}
	return nil
}

func (s *Server) snapshotControlState(sequence uint64) (io.ReadCloser, int64, error) {
	limit := min(s.quorumSnapshotMaxBytes, int64(quorum.ControlSnapshotLimit))
	if sequence == 0 || limit <= 0 {
		return nil, 0, errors.New("invalid control snapshot budget or sequence")
	}
	if err := requireTaskQuorumSnapshotCapacity(s.quorumSnapshotDataDir, limit, s.quorumSnapshotFreeReserve, 3); err != nil {
		return nil, 0, err
	}
	f, err := os.CreateTemp(s.quorumSnapshotDataDir, ".control-image-*")
	if err != nil {
		return nil, 0, err
	}
	file := &taskQuorumSnapshotFile{File: f, path: f.Name()}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()
	digest := sha256.New()
	w := io.MultiWriter(f, digest)
	if _, err = io.WriteString(w, controlImageMagic); err != nil {
		return nil, 0, err
	}
	err = s.withQuorumReadDatabase(func(tx *sql.Tx) error {
		var h controlHeader
		var authReady int
		if err := tx.QueryRow(`SELECT m.cluster_id,c.sequence,c.digest,m.authority_revision,m.baseline_ready,m.authority_ready,(SELECT count(*) FROM task_quorum_authority)
			FROM task_quorum_meta m JOIN task_quorum_chain c USING(id) WHERE m.id=1 AND m.applied_index=c.sequence`).Scan(&h.Cluster, &h.Sequence, &h.Digest, &h.AuthorityRevision, &h.Ready, &authReady, &h.Rows); err != nil {
			return err
		}
		if !s.validControlHeader(h, sequence) || authReady != h.Ready {
			return errors.New("invalid control snapshot checkpoint")
		}
		if err := writeControlFrame(w, h); err != nil {
			return err
		}
		// Reject a damaged local row before asking SQLite to allocate its BLOB.
		var bad bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_quorum_authority WHERE length(payload)>? OR length(CAST(namespace AS BLOB))>128 OR length(CAST(row_key AS BLOB))>128)`, quorum.ControlAuthorityLimit).Scan(&bad); err != nil {
			return err
		}
		if bad {
			return errors.New("oversized control authority row")
		}
		rows, err := tx.Query("SELECT namespace,row_key,revision,payload FROM task_quorum_authority ORDER BY namespace,row_key")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m := quorum.Mutation{External: true}
			if err = rows.Scan(&m.Namespace, &m.Key, &m.Record.Revision, &m.Record.Value); err != nil {
				return err
			}
			if err = quorum.ValidateControlMutation(m, h.Cluster); err != nil {
				return err
			}
			offset, err := f.Seek(0, io.SeekCurrent)
			if err != nil {
				return err
			}
			if offset+int64(len(m.Record.Value))+sha256.Size > limit {
				return errors.New("control snapshot exceeds its budget")
			}
			if err = writeControlFrame(w, m); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	if _, err = f.Write(digest.Sum(nil)); err != nil {
		return nil, 0, err
	}
	n, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, 0, err
	}
	if n > limit {
		return nil, 0, errors.New("control snapshot exceeds its budget")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, 0, err
	}
	ok = true
	return file, n, nil
}

func (s *Server) validControlHeader(h controlHeader, seq uint64) bool {
	n := len(s.requiredQuorumAuthorities())
	return h.Cluster == s.quorumClusterID && seq > 0 && h.Sequence == seq && len(h.Digest) == sha256.Size &&
		h.AuthorityRevision <= seq && h.AuthorityRevision >= uint64(h.Rows) && h.Rows >= 0 && h.Rows <= n && (h.Ready == 0 || (h.Ready == 1 && h.Rows == n))
}

func (s *Server) restoreControlState(sequence uint64, source io.Reader, size int64) error {
	limit := min(s.quorumSnapshotMaxBytes, int64(quorum.ControlSnapshotLimit))
	if size < int64(len(controlImageMagic)+sha256.Size+4) || size > limit {
		return errors.New("control snapshot size outside budget")
	}
	if err := requireTaskQuorumSnapshotCapacity(s.quorumSnapshotDataDir, limit, s.quorumSnapshotFreeReserve, 2); err != nil {
		return err
	}
	frames := &io.LimitedReader{R: source, N: size - sha256.Size}
	digest := sha256.New()
	reader := io.TeeReader(frames, digest)
	var magic [len(controlImageMagic)]byte
	if _, err := io.ReadFull(reader, magic[:]); err != nil {
		return err
	}
	if string(magic[:]) != controlImageMagic {
		return errors.New("expected owner_local control snapshot")
	}
	var h controlHeader
	if err := readControlFrame(reader, 4096, &h); err != nil {
		return err
	}
	if !s.validControlHeader(h, sequence) {
		return errors.New("control checkpoint does not match cluster or sequence")
	}
	// Holding the exclusive store gate covers both replacement and the
	// replay cursor reset. Any bad frame rolls back the whole replacement.
	s.quorumControlDBMu.Lock()
	defer s.quorumControlDBMu.Unlock()
	db, err := openTaskDatabase(s.quorumDatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = initializeControlSchema(tx); err != nil {
		return err
	}
	var existing string
	err = tx.QueryRow("SELECT cluster_id FROM task_quorum_meta WHERE id=1").Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existing != "" && existing != h.Cluster {
		return errors.New("control storage belongs to a different cluster")
	}
	for _, name := range []string{"task_quorum_authority", "task_quorum_revisions", "task_quorum_chain", "task_quorum_meta"} {
		if _, err = tx.Exec("DELETE FROM " + name); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for i := 0; i < h.Rows; i++ {
		var m quorum.Mutation
		if err = readControlFrame(reader, controlFrameLimit, &m); err != nil {
			return err
		}
		if !m.External || seen[m.Namespace] || m.Record.Revision > h.AuthorityRevision {
			return errors.New("invalid control snapshot authority revision or duplicate")
		}
		if err = quorum.ValidateControlMutation(m, h.Cluster); err != nil {
			return err
		}
		if err = applyTaskQuorumMutation(tx, m); err != nil {
			return err
		}
		seen[m.Namespace] = true
	}
	if frames.N != 0 {
		return errors.New("unexpected bytes in control image")
	}
	var sum [sha256.Size]byte
	if _, err = io.ReadFull(source, sum[:]); err != nil || !bytes.Equal(sum[:], digest.Sum(nil)) {
		return errors.New("invalid control snapshot checksum")
	}
	if _, err = tx.Exec("INSERT INTO task_quorum_meta(id,cluster_id,applied_index,authority_revision,baseline_ready,authority_ready) VALUES(1,?,?,?,?,?)", h.Cluster, h.Sequence, h.AuthorityRevision, h.Ready, h.Ready); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO task_quorum_chain(id,sequence,digest) VALUES(1,?,?)", h.Sequence, h.Digest); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.taskQuorumReplayMu.Lock()
	s.taskQuorumReplay = taskQuorumReplay{}
	s.taskQuorumReplayMu.Unlock()
	return nil
}
