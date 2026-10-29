package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

const maxSQLiteQuorumRevision = uint64(1<<63 - 1)

func quorumBatchDigest(mutations []quorum.Mutation) ([32]byte, error) {
	ordered := append([]quorum.Mutation(nil), mutations...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Namespace != ordered[j].Namespace {
			return ordered[i].Namespace < ordered[j].Namespace
		}
		return ordered[i].Key < ordered[j].Key
	})
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

// commitTaskQuorumChanges is called before SQLite COMMIT. A local failure to
// obtain a quorum therefore leaves the transaction uncommitted and invisible
// to the executor queue.
func (s *Server) commitTaskQuorumChanges(t *taskTransaction) error {
	if s.taskQuorumApplying.Load() || s.quorumNode == nil || len(t.quorumChanges) == 0 {
		return nil
	}
	mutations := make([]quorum.Mutation, 0, len(t.quorumChanges))
	for _, mutation := range t.quorumChanges {
		mutations = append(mutations, mutation)
	}
	sort.Slice(mutations, func(i, j int) bool {
		if mutations[i].Namespace != mutations[j].Namespace {
			return mutations[i].Namespace < mutations[j].Namespace
		}
		return mutations[i].Key < mutations[j].Key
	})
	digest, err := quorumBatchDigest(mutations)
	if err != nil {
		return err
	}
	s.taskQuorumMu.Lock()
	if s.taskQuorumPending == nil {
		s.taskQuorumPending = map[[32]byte]uint64{}
	}
	s.taskQuorumPending[digest] = 0
	s.taskQuorumMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = s.quorumNode.ApplyBatch(ctx, mutations)
	cancel()
	if err != nil {
		s.taskQuorumMu.Lock()
		delete(s.taskQuorumPending, digest)
		s.taskQuorumMu.Unlock()
		return err
	}
	s.taskQuorumMu.Lock()
	externalSequence := s.taskQuorumPending[digest]
	delete(s.taskQuorumPending, digest)
	s.taskQuorumMu.Unlock()
	if externalSequence == 0 {
		externalSequence = s.quorumNode.ExternalAppliedIndex()
	}
	t.quorumIndex = externalSequence
	t.quorumBatch = append([]quorum.Mutation(nil), mutations...)
	if err := taskQuorumChainAdvance(t.tx, externalSequence, digest); err != nil {
		return err
	}
	_, err = t.tx.Exec(`INSERT INTO task_quorum_meta(id,cluster_id,applied_index) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET cluster_id=excluded.cluster_id,applied_index=max(applied_index,excluded.applied_index)`, s.quorumClusterID, externalSequence)
	if err != nil {
		return err
	}
	if t.quorumAuthorityChanges > 0 {
		_, err = t.tx.Exec(`UPDATE task_quorum_meta SET authority_revision=authority_revision+? WHERE id=1`, t.quorumAuthorityChanges)
	}
	return err
}

// commitQuorumAuthorityState records owner-managed control state in the same
// majority-committed external sequence as durable tasks. The local file is
// still written by its existing owner after this returns successfully.
func (s *Server) commitQuorumAuthorityState(domain, key string, value any) error {
	if s.taskQuorumApplying.Load() || s.quorumNode == nil || s.cfg.QuorumConfigFile == "" {
		return nil
	}
	if domain == "" || key == "" {
		return errors.New("task quorum authority identity is empty")
	}
	return s.withTaskDatabase(func(t *taskTransaction) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		namespace := "authority/" + domain
		var revision int64
		err = t.tx.QueryRow(`SELECT revision FROM task_quorum_revisions WHERE namespace=? AND row_key=?`, namespace, key).Scan(&revision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if revision < 0 || revision == int64(^uint64(0)>>1) {
			return errors.New("task quorum authority revision is out of range")
		}
		var previous []byte
		var storedRevision int64
		readErr := t.tx.QueryRow(`SELECT payload,revision FROM task_quorum_authority WHERE namespace=? AND row_key=?`, namespace, key).Scan(&previous, &storedRevision)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		if readErr == nil && storedRevision != revision || errors.Is(readErr, sql.ErrNoRows) && revision != 0 {
			return errors.New("task quorum authority row and revision ledger disagree")
		}
		if readErr == nil && storedRevision == revision {
			plaintext, err := s.quorumNode.OpenExternalValue(namespace, key, uint64(revision), previous)
			if err != nil {
				return err
			}
			if bytes.Equal(plaintext, encoded) {
				return nil
			}
		}
		nextRevision := uint64(revision + 1)
		sealed, err := s.quorumNode.SealExternalValue(namespace, key, nextRevision, encoded)
		if err != nil {
			return err
		}
		if err := t.captureQuorumRow(namespace, key, json.RawMessage(sealed), false); err != nil {
			return err
		}
		_, err = t.tx.Exec(`INSERT INTO task_quorum_authority(namespace,row_key,payload,revision) VALUES(?,?,?,?) ON CONFLICT(namespace,row_key) DO UPDATE SET payload=excluded.payload,revision=excluded.revision`, namespace, key, sealed, nextRevision)
		if err == nil {
			t.quorumAuthorityChanges++
		}
		return err
	})
}

func (s *Server) readQuorumAuthorityState(domain, key string, destination any) error {
	if s.quorumNode == nil || domain == "" || key == "" || destination == nil {
		return errors.New("task quorum authority state is unavailable")
	}
	namespace := "authority/" + domain
	var payload []byte
	var revision uint64
	if err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT payload,revision FROM task_quorum_authority WHERE namespace=? AND row_key=?`, namespace, key).Scan(&payload, &revision)
	}); err != nil {
		return err
	}
	plaintext, err := s.quorumNode.OpenExternalValue(namespace, key, revision, payload)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(plaintext, destination); err != nil {
		return fmt.Errorf("decode quorum authority state %s/%s: %w", domain, key, err)
	}
	return nil
}

// applyTaskQuorumMutations is the durable FSM callback. Followers apply the
// whole batch in one SQLite transaction; the leader recognizes its own pending
// batch and leaves the already-open originating transaction to commit it.
func (s *Server) applyTaskQuorumMutations(logIndex, externalSequence uint64, mutations []quorum.Mutation) error {
	digest, err := quorumBatchDigest(mutations)
	if err != nil {
		return err
	}
	s.taskQuorumMu.Lock()
	if _, ok := s.taskQuorumPending[digest]; ok {
		s.taskQuorumPending[digest] = externalSequence
		s.taskQuorumMu.Unlock()
		return nil
	}
	s.taskQuorumMu.Unlock()

	var external []quorum.Mutation
	baselineReady := false
	baselineAuthorityReady := false
	for _, mutation := range mutations {
		if mutation.External {
			external = append(external, mutation)
			continue
		}
		if mutation.Namespace == "cluster_meta" && mutation.Key == "task_baseline" && !mutation.Record.Deleted {
			var baseline struct {
				ClusterID        string   `json:"cluster_id"`
				AuthorityDomains []string `json:"authority_domains"`
			}
			if err := json.Unmarshal(mutation.Record.Value, &baseline); err != nil {
				return err
			}
			if baseline.ClusterID == "" || baseline.ClusterID != s.quorumClusterID {
				return errors.New("task quorum baseline cluster identity mismatch")
			}
			if len(baseline.AuthorityDomains) != len(quorumRequiredAuthorityRecords) {
				return errors.New("task quorum baseline is missing an authoritative state domain")
			}
			seenDomains := make(map[string]bool, len(baseline.AuthorityDomains))
			for _, domain := range baseline.AuthorityDomains {
				if _, required := quorumRequiredAuthorityRecords[domain]; !required || seenDomains[domain] {
					return errors.New("task quorum baseline has an invalid authoritative state domain")
				}
				seenDomains[domain] = true
			}
			baselineAuthorityReady = true
			baselineReady = true
		}
	}
	if len(external) == 0 && !baselineReady {
		return nil
	}
	externalDigest, err := quorumBatchDigest(external)
	if err != nil {
		return err
	}
	s.taskQuorumApplying.Store(true)
	defer s.taskQuorumApplying.Store(false)
	return s.withTaskDatabase(func(t *taskTransaction) error {
		if len(external) > 0 {
			var clusterID string
			var previousSequence uint64
			err := t.tx.QueryRow(`SELECT cluster_id,applied_index FROM task_quorum_meta WHERE id=1`).Scan(&clusterID, &previousSequence)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if clusterID != "" && clusterID != s.quorumClusterID {
				return errors.New("task quorum database belongs to a different cluster")
			}
			if externalSequence < previousSequence {
				_, err := s.verifyTaskQuorumHistoricalReplay(t.tx, externalSequence, previousSequence, externalDigest)
				return err
			}
			if externalSequence == previousSequence {
				s.taskQuorumReplayMu.Lock()
				replaying := s.taskQuorumReplay.target != 0
				s.taskQuorumReplayMu.Unlock()
				if replaying {
					complete, err := s.verifyTaskQuorumHistoricalReplay(t.tx, externalSequence, previousSequence, externalDigest)
					if err != nil {
						return err
					}
					if !complete {
						return errors.New("task quorum replay is incomplete")
					}
				}
				matches, err := taskQuorumBatchMatches(t.tx, external)
				if err != nil {
					return err
				}
				if matches {
					// SQLite may report an ambiguous COMMIT after the durable
					// transaction actually landed. Reconcile only the identical
					// committed sequence; never replay a different batch.
					return nil
				}
				return errors.New("task quorum sequence already committed with different local state")
			}
			if externalSequence != previousSequence+1 {
				return fmt.Errorf("task quorum replica sequence gap: local=%d incoming=%d", previousSequence, externalSequence)
			}
		}
		if len(external) > 0 {
			if err := taskQuorumChainAdvance(t.tx, externalSequence, externalDigest); err != nil {
				return err
			}
		}
		for _, mutation := range external {
			if err := applyTaskQuorumMutation(t.tx, mutation); err != nil {
				return err
			}
		}
		authorityChanges := 0
		for _, mutation := range external {
			if taskQuorumAuthorityNamespace(mutation.Namespace) {
				authorityChanges++
			}
		}
		if baselineReady {
			for domain, key := range quorumRequiredAuthorityRecords {
				var count int
				if err := t.tx.QueryRow(`SELECT count(*) FROM task_quorum_authority WHERE namespace=? AND row_key=?`, "authority/"+domain, key).Scan(&count); err != nil {
					return err
				}
				if count != 1 {
					baselineAuthorityReady = false
					break
				}
			}
			if !baselineAuthorityReady {
				return errors.New("task quorum baseline is missing replicated authority rows")
			}
			// Each node still has a separate local materialization fence; this bit
			// asserts only that every authoritative row is majority committed.
			_, err := t.tx.Exec(`INSERT INTO task_quorum_meta(id,cluster_id,baseline_ready,authority_ready) VALUES(1,?,1,1) ON CONFLICT(id) DO UPDATE SET cluster_id=excluded.cluster_id,baseline_ready=1,authority_ready=1`, s.quorumClusterID)
			return err
		}
		_, err := t.tx.Exec(`INSERT INTO task_quorum_meta(id,cluster_id,applied_index,authority_revision) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET cluster_id=excluded.cluster_id,applied_index=excluded.applied_index,authority_revision=task_quorum_meta.authority_revision+excluded.authority_revision`, s.quorumClusterID, externalSequence, authorityChanges)
		return err
	})
}

func taskQuorumBatchMatches(tx *sql.Tx, mutations []quorum.Mutation) (bool, error) {
	for _, mutation := range mutations {
		var revision int64
		err := tx.QueryRow(`SELECT revision FROM task_quorum_revisions WHERE namespace=? AND row_key=?`, mutation.Namespace, mutation.Key).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) || err == nil && uint64(revision) != mutation.Record.Revision {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		matches, err := taskQuorumRowMatches(tx, mutation.Namespace, mutation.Key, mutation.Record)
		if err != nil {
			return false, err
		}
		if !matches {
			return false, nil
		}
	}
	return true, nil
}

func applyTaskQuorumMutation(tx *sql.Tx, mutation quorum.Mutation) error {
	namespace, rowKey := mutation.Namespace, mutation.Key
	if kind, ok := taskRecordKind(namespace); ok {
		if kind == "" || rowKey == "" {
			return errors.New("invalid task record quorum key")
		}
	} else if namespace != "task_controls" && namespace != "task_requests" && !taskQuorumAuthorityNamespace(namespace) {
		return fmt.Errorf("unsupported external task namespace %q", namespace)
	}
	var previous int64
	err := tx.QueryRow(`SELECT revision FROM task_quorum_revisions WHERE namespace=? AND row_key=?`, namespace, rowKey).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if mutation.Record.Revision < uint64(previous) {
		return errTaskConflict
	}
	if mutation.Record.Revision == uint64(previous) {
		equal, err := taskQuorumRowMatches(tx, namespace, rowKey, mutation.Record)
		if err != nil {
			return err
		}
		if equal {
			return nil
		}
		return errTaskConflict
	}
	if mutation.Record.Deleted {
		if err := deleteTaskQuorumRow(tx, namespace, rowKey); err != nil {
			return err
		}
	} else if err := upsertTaskQuorumRow(tx, namespace, rowKey, mutation.Record.Value, mutation.Record.Revision); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO task_quorum_revisions(namespace,row_key,revision) VALUES(?,?,?) ON CONFLICT(namespace,row_key) DO UPDATE SET revision=excluded.revision`, namespace, rowKey, mutation.Record.Revision)
	return err
}

func taskQuorumRowMatches(tx *sql.Tx, namespace, rowKey string, record quorum.Record) (bool, error) {
	if record.Deleted {
		var count int
		switch {
		case taskRecordNamespace(namespace):
			kind, _ := taskRecordKind(namespace)
			err := tx.QueryRow(`SELECT count(*) FROM task_records WHERE kind=? AND key=?`, kind, rowKey).Scan(&count)
			return count == 0, err
		case namespace == "task_controls":
			err := tx.QueryRow(`SELECT count(*) FROM task_controls WHERE task_id=?`, rowKey).Scan(&count)
			return count == 0, err
		case namespace == "task_requests":
			err := tx.QueryRow(`SELECT count(*) FROM task_requests WHERE key=?`, rowKey).Scan(&count)
			return count == 0, err
		case taskQuorumAuthorityNamespace(namespace):
			err := tx.QueryRow(`SELECT count(*) FROM task_quorum_authority WHERE namespace=? AND row_key=?`, namespace, rowKey).Scan(&count)
			return count == 0, err
		default:
			return false, fmt.Errorf("unsupported task namespace %q", namespace)
		}
	}
	var value any
	switch {
	case taskRecordNamespace(namespace):
		kind, _ := taskRecordKind(namespace)
		var row quorumTaskRecordRow
		err := tx.QueryRow(`SELECT kind,key,updated,done,payload,revision FROM task_records WHERE kind=? AND key=?`, kind, rowKey).Scan(&row.Kind, &row.Key, &row.Updated, &row.Done, &row.Payload, &row.Revision)
		value = row
		if err != nil {
			return false, err
		}
	case namespace == "task_controls":
		var row quorumTaskControlRow
		err := tx.QueryRow(`SELECT task_id,server,next_attempt FROM task_controls WHERE task_id=?`, rowKey).Scan(&row.TaskID, &row.Server, &row.NextAttempt)
		value = row
		if err != nil {
			return false, err
		}
	case namespace == "task_requests":
		var row quorumTaskRequestRow
		err := tx.QueryRow(`SELECT key,fingerprint,owner,created,job_id,response,status FROM task_requests WHERE key=?`, rowKey).Scan(&row.Key, &row.Fingerprint, &row.Owner, &row.Created, &row.JobID, &row.Response, &row.Status)
		value = row
		if err != nil {
			return false, err
		}
	case taskQuorumAuthorityNamespace(namespace):
		var payload []byte
		var revision int64
		err := tx.QueryRow(`SELECT payload,revision FROM task_quorum_authority WHERE namespace=? AND row_key=?`, namespace, rowKey).Scan(&payload, &revision)
		if err != nil {
			return false, err
		}
		if uint64(revision) != record.Revision {
			return false, nil
		}
		return bytes.Equal(payload, record.Value), nil
	default:
		return false, fmt.Errorf("unsupported task namespace %q", namespace)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	return string(encoded) == string(record.Value), nil
}

func upsertTaskQuorumRow(tx *sql.Tx, namespace, rowKey string, payload []byte, revision uint64) error {
	switch {
	case taskRecordNamespace(namespace):
		kind, _ := taskRecordKind(namespace)
		var row quorumTaskRecordRow
		if err := json.Unmarshal(payload, &row); err != nil {
			return err
		}
		if row.Kind != kind || row.Key != rowKey || row.Revision < 0 || len(row.Payload) == 0 || !json.Valid(row.Payload) {
			return errors.New("invalid replicated task record")
		}
		_, err := tx.Exec(`INSERT INTO task_records(kind,key,updated,done,payload,revision) VALUES(?,?,?,?,?,?) ON CONFLICT(kind,key) DO UPDATE SET updated=excluded.updated,done=excluded.done,payload=excluded.payload,revision=excluded.revision`, row.Kind, row.Key, row.Updated, row.Done, row.Payload, row.Revision)
		return err
	case namespace == "task_controls":
		var row quorumTaskControlRow
		if err := json.Unmarshal(payload, &row); err != nil {
			return err
		}
		if row.TaskID != rowKey || row.Server == "" {
			return errors.New("invalid replicated task control")
		}
		_, err := tx.Exec(`INSERT INTO task_controls(task_id,server,next_attempt) VALUES(?,?,?) ON CONFLICT(task_id) DO UPDATE SET server=excluded.server,next_attempt=excluded.next_attempt`, row.TaskID, row.Server, row.NextAttempt)
		return err
	case namespace == "task_requests":
		var row quorumTaskRequestRow
		if err := json.Unmarshal(payload, &row); err != nil {
			return err
		}
		if row.Key != rowKey || row.Response != nil && !json.Valid(row.Response) {
			return errors.New("invalid replicated task request")
		}
		_, err := tx.Exec(`INSERT INTO task_requests(key,fingerprint,owner,created,job_id,response,status) VALUES(?,?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET fingerprint=excluded.fingerprint,owner=excluded.owner,created=excluded.created,job_id=excluded.job_id,response=excluded.response,status=excluded.status`, row.Key, row.Fingerprint, row.Owner, row.Created, row.JobID, row.Response, row.Status)
		return err
	case taskQuorumAuthorityNamespace(namespace):
		if revision > maxSQLiteQuorumRevision {
			return errors.New("quorum authority revision exceeds SQLite integer range")
		}
		_, err := tx.Exec(`INSERT INTO task_quorum_authority(namespace,row_key,payload,revision) VALUES(?,?,?,?) ON CONFLICT(namespace,row_key) DO UPDATE SET payload=excluded.payload,revision=excluded.revision`, namespace, rowKey, payload, int64(revision))
		return err
	default:
		return fmt.Errorf("unsupported task namespace %q", namespace)
	}
}

func deleteTaskQuorumRow(tx *sql.Tx, namespace, rowKey string) error {
	switch {
	case taskRecordNamespace(namespace):
		kind, _ := taskRecordKind(namespace)
		_, err := tx.Exec(`DELETE FROM task_records WHERE kind=? AND key=?`, kind, rowKey)
		return err
	case namespace == "task_controls":
		_, err := tx.Exec(`DELETE FROM task_controls WHERE task_id=?`, rowKey)
		return err
	case namespace == "task_requests":
		_, err := tx.Exec(`DELETE FROM task_requests WHERE key=?`, rowKey)
		return err
	case taskQuorumAuthorityNamespace(namespace):
		_, err := tx.Exec(`DELETE FROM task_quorum_authority WHERE namespace=? AND row_key=?`, namespace, rowKey)
		return err
	default:
		return fmt.Errorf("unsupported task namespace %q", namespace)
	}
}

func taskRecordNamespace(namespace string) bool {
	_, ok := taskRecordKind(namespace)
	return ok
}

func taskRecordKind(namespace string) (string, bool) {
	kind, ok := strings.CutPrefix(namespace, "task_records/")
	return kind, ok && kind != ""
}

func taskQuorumAuthorityNamespace(namespace string) bool {
	domain, ok := strings.CutPrefix(namespace, "authority/")
	return ok && domain != ""
}
