package hub

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
)

// A fixed-size authenticated history checkpoint lets a restarted Raft FSM
// replay its own already-applied log without reverting newer SQLite rows.
// No executable input or per-operation digest is stored in plaintext.
type taskQuorumReplay struct {
	sequence, target uint64
	digest           [32]byte
}

func taskQuorumChainStep(previous [32]byte, sequence uint64, batch [32]byte) [32]byte {
	var input [72]byte
	copy(input[:32], previous[:])
	binary.BigEndian.PutUint64(input[32:40], sequence)
	copy(input[40:], batch[:])
	return sha256.Sum256(input[:])
}

func taskQuorumChainAdvance(tx *sql.Tx, sequence uint64, batch [32]byte) error {
	var previousSequence uint64
	var previousBytes []byte
	err := tx.QueryRow(`SELECT sequence,digest FROM task_quorum_chain WHERE id=1`).Scan(&previousSequence, &previousBytes)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if sequence != previousSequence+1 {
		return fmt.Errorf("task quorum chain sequence mismatch: local=%d incoming=%d", previousSequence, sequence)
	}
	var previous [32]byte
	if previousSequence != 0 {
		if len(previousBytes) != len(previous) {
			return errors.New("task quorum chain checkpoint is invalid")
		}
		copy(previous[:], previousBytes)
	}
	next := taskQuorumChainStep(previous, sequence, batch)
	_, err = tx.Exec(`INSERT INTO task_quorum_chain(id,sequence,digest) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET sequence=excluded.sequence,digest=excluded.digest`, sequence, next[:])
	return err
}

// verifyTaskQuorumHistoricalReplay accepts a complete sequential replay only
// when its cumulative digest matches the atomically committed SQLite head.
// While replay is incomplete the FSM index is behind SQLite, so readiness
// stays closed. A wrong history fails closed without overwriting state.
func (s *Server) verifyTaskQuorumHistoricalReplay(tx *sql.Tx, sequence, target uint64, batch [32]byte) (bool, error) {
	var storedSequence uint64
	var stored []byte
	err := tx.QueryRow(`SELECT sequence,digest FROM task_quorum_chain WHERE id=1`).Scan(&storedSequence, &stored)
	if err != nil {
		return false, fmt.Errorf("task quorum replay checkpoint unavailable: %w", err)
	}
	if storedSequence != target || len(stored) != sha256.Size {
		return false, errors.New("task quorum replay checkpoint does not match SQLite")
	}
	s.taskQuorumReplayMu.Lock()
	defer s.taskQuorumReplayMu.Unlock()
	if s.taskQuorumReplay.target == 0 {
		if sequence != 1 {
			return false, errors.New("task quorum replay did not start at sequence one")
		}
		s.taskQuorumReplay.target = target
	}
	replay := &s.taskQuorumReplay
	if replay.target != target || sequence != replay.sequence+1 {
		return false, errors.New("task quorum replay sequence is inconsistent")
	}
	replay.digest = taskQuorumChainStep(replay.digest, sequence, batch)
	replay.sequence = sequence
	if sequence == target {
		if !bytes.Equal(replay.digest[:], stored) {
			return false, errors.New("task quorum replay history does not match SQLite checkpoint")
		}
		s.taskQuorumReplay = taskQuorumReplay{}
		return true, nil
	}
	return false, nil
}

func taskQuorumSnapshotChainMatches(tx *sql.Tx, sequence uint64) error {
	var actual uint64
	var digest []byte
	if err := tx.QueryRow(`SELECT sequence,digest FROM task_quorum_chain WHERE id=1`).Scan(&actual, &digest); err != nil {
		return fmt.Errorf("task quorum snapshot chain unavailable: %w", err)
	}
	if actual != sequence || len(digest) != sha256.Size {
		return errors.New("task quorum snapshot chain does not match its applied sequence")
	}
	return nil
}
