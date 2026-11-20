package hub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

func (s *Server) requiredQuorumAuthorities() map[string]string {
	if !s.quorumControlOnly() {
		return quorumRequiredAuthorityRecords
	}
	domains := map[string]string{}
	for domain := range quorumRequiredAuthorityRecords {
		if key := quorum.ControlAuthorityKey(domain); key != "" {
			domains[domain] = key
		}
	}
	return domains
}

func (s *Server) seedControlBaseline(ctx context.Context) error {
	s.taskQuorumBaselineSeeding.Store(true)
	defer s.taskQuorumBaselineSeeding.Store(false)
	if err := s.seedOutstandingRequests(ctx); err != nil {
		return err
	}
	if err := s.seedQuorumAuthorityBaseline(ctx); err != nil {
		return err
	}
	var seq uint64
	var chain []byte
	if err := s.withQuorumReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT sequence,digest FROM task_quorum_chain WHERE id=1").Scan(&seq, &chain)
	}); err != nil {
		return err
	}
	if seq == 0 || seq != s.quorumNode.ExternalAppliedIndex() || len(chain) != sha256.Size {
		return errors.New("control checkpoint is not committed")
	}
	var domains []string
	for _, domain := range quorumRequiredAuthorityDomains {
		if quorum.ControlAuthorityKey(domain) != "" {
			domains = append(domains, domain)
		}
	}
	raw, err := json.Marshal(quorum.ControlBaseline{Mode: quorum.OwnerLocal, ClusterID: s.quorumClusterID, AuthorityDomains: domains, ChainSequence: seq, ChainDigest: hex.EncodeToString(chain)})
	if err != nil {
		return err
	}
	return s.quorumNode.ApplyRecord(ctx, "cluster_meta", "task_baseline", quorum.Record{Revision: 1, Value: raw})
}

// Drive from request reservations. The two lookups use the ordinary
// (kind,key) primary key. Neither a response body nor a task payload is read.
const protectedRequestJoin = ` FROM task_requests q
	LEFT JOIN task_records shell ON shell.kind='shell' AND shell.key=q.job_id AND q.job_id<>''
	LEFT JOIN task_records relay ON relay.kind='relay' AND relay.key=q.job_id AND q.job_id<>''
	WHERE q.status=0 OR q.created>=?1 OR shell.done=0 OR relay.done=0 OR shell.done>=?1 OR relay.done>=?1`

func (s *Server) seedOutstandingRequests(ctx context.Context) error {
	path := s.taskDatabasePath()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if _, e := os.Lstat(s.taskStatePath()); errors.Is(e, os.ErrNotExist) {
			return nil
		} else if e != nil {
			return e
		}
		return errors.New("owner_local requires SQLite request metadata; historical JSON is not a bootstrap source")
	} else if err != nil {
		return err
	}
	return withTaskSnapshotRead(path, func(tx *sql.Tx) error {
		// Refuse an unindexed source rather than building a temporary history
		// index. Both receipt kinds can legitimately exist at the same job ID.
		var primary int
		if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('task_records') WHERE (name='kind' AND pk=1) OR (name='key' AND pk=2)`).Scan(&primary); err != nil {
			return err
		}
		if primary != 2 {
			return errors.New("owner_local requires an indexed ordinary receipt store")
		}
		cutoff := float64(s.now().Add(-quorum.OperationRetention).UnixNano()) / 1e9
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM (SELECT 1"+protectedRequestJoin+" LIMIT ?2)", cutoff, quorum.OperationLimitPerOwner+1).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		if count > quorum.OperationLimitPerOwner {
			return quorum.ErrOperationCapacity
		}
		s.authMu.RLock()
		s.mu.Lock()
		isWriter := s.authInitialized && !s.authState.ReadOnly && s.cfg.AuthMode == "writer" && s.authState.WriterID == s.authLocalID && s.authLocalID != ""
		s.mu.Unlock()
		s.authMu.RUnlock()
		if !isWriter {
			return errors.New("protected reservations require their original authoritative writer")
		}
		// octet_length(column) inspects SQLite's byte length without reading
		// the value. Oversized or invalid metadata fails before Go allocates it.
		rows, err := tx.QueryContext(ctx, `SELECT
			CASE WHEN typeof(q.key)='text' AND octet_length(q.key) BETWEEN 1 AND 4096 THEN q.key END,
			CASE WHEN typeof(q.fingerprint)='text' AND octet_length(q.fingerprint) BETWEEN 1 AND 4096 THEN q.fingerprint END,
			CASE WHEN typeof(q.owner)='text' AND octet_length(q.owner) BETWEEN 1 AND 512 THEN q.owner END,
			q.created,CASE WHEN typeof(q.job_id)='text' AND octet_length(q.job_id)<=512 THEN q.job_id END,q.status,shell.done,relay.done`+protectedRequestJoin, cutoff)
		if err != nil {
			return err
		}
		defer rows.Close()
		owner := s.quorumNode.Status().NodeID
		for rows.Next() {
			var key, fp, process, job string
			var created float64
			var status int
			var shell, relay sql.NullFloat64
			if err = rows.Scan(&key, &fp, &process, &created, &job, &status, &shell, &relay); err != nil {
				return errors.New("invalid or oversized protected reservation metadata")
			}
			if created <= 0 || math.IsNaN(created) || math.IsInf(created, 0) || (status != 0 && (status < 100 || status > 599)) {
				return errors.New("invalid protected request witness or status")
			}
			for _, done := range []sql.NullFloat64{shell, relay} {
				if done.Valid && (done.Float64 < 0 || math.IsNaN(done.Float64) || math.IsInf(done.Float64, 0)) {
					return errors.New("invalid receipt completion timestamp")
				}
			}
			active := (shell.Valid && shell.Float64 == 0) || (relay.Valid && relay.Float64 == 0)
			// A cached handle with a missing receipt is uncertain. An HTTP 202
			// without a job ID also cannot establish a terminal outcome.
			terminal := !active && (shell.Valid || relay.Valid || (job == "" && status >= 200 && status != 202))
			op := quorum.Operation{Owner: owner, Fingerprint: sha256Hex([]byte(fp)), Witness: sha256Hex([]byte(process + "\x00" + strconv.FormatFloat(created, 'g', -1, 64)))}
			k := sha256Hex([]byte(key))
			d, err := s.quorumNode.UpdateOperation(ctx, "claim", k, op)
			if err != nil {
				return err
			}
			prior := d.Operation
			if prior.Owner != op.Owner || prior.Fingerprint != op.Fingerprint || prior.Witness != op.Witness || (prior.JobID != "" && prior.JobID != job) || (prior.Expires != 0 && (!terminal || prior.JobID != job)) {
				return errors.New("protected request belongs to another owner or a different outcome")
			}
			op.JobID = job
			if job != "" && prior.JobID == "" {
				d, err = s.quorumNode.UpdateOperation(ctx, "bind", k, op)
				if err != nil {
					return err
				}
				if d.Operation != op {
					return errors.New("protected reservation binding mismatch")
				}
			}
			if terminal && prior.Expires == 0 {
				d, err = s.quorumNode.UpdateOperation(ctx, "terminal", k, op)
				if err != nil {
					return err
				}
				if d.Operation.Expires <= 0 {
					return errors.New("protected terminal outcome was not recorded")
				}
			}
		}
		return rows.Err()
	})
}
