package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type durableRequestDecision struct {
	response map[string]any
	status   int
	replay   bool
}

// Reserve before executing. The reservation and task association survive
// process death. A request with an uncertain outcome is NEVER replayed as a
// new operation merely because its owner exited or its TTL elapsed.
func (s *Server) reserveDurableTaskRequest(key, fingerprint string) (durableRequestDecision, error) {
	var decision durableRequestDecision
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, err := s.ensureTaskOwnerLocked()
	if err != nil {
		return decision, err
	}
	err = s.withTaskDatabase(func(t *taskTransaction) error {
		_, err := t.tx.Exec(`DELETE FROM task_requests WHERE response IS NOT NULL AND created<? AND NOT EXISTS (SELECT 1 FROM task_records WHERE task_records.key=task_requests.job_id AND task_records.kind IN ('shell','relay') AND task_records.done=0)`, nowFloat()-idempotencyTTL.Seconds())
		if err != nil {
			return err
		}
		var storedFingerprint, storedOwner, jobID string
		var payload []byte
		var status int
		err = t.tx.QueryRow(`SELECT fingerprint,owner,job_id,response,status FROM task_requests WHERE key=?`, key).Scan(&storedFingerprint, &storedOwner, &jobID, &payload, &status)
		if errors.Is(err, sql.ErrNoRows) {
			// Read pre-reservation-format idempotency records during migration.
			var legacyBytes []byte
			legacyErr := t.tx.QueryRow(`SELECT payload FROM task_records WHERE kind='idempotency' AND key=?`, key).Scan(&legacyBytes)
			if legacyErr != nil && !errors.Is(legacyErr, sql.ErrNoRows) {
				return legacyErr
			}
			if legacyErr == nil {
				var p persistedIdempotencyEntry
				if err := json.Unmarshal(legacyBytes, &p); err != nil {
					return err
				}
				if time.Since(p.CreatedAt) <= idempotencyTTL {
					if p.Fingerprint != fingerprint {
						decision = durableRequestConflict()
						return nil
					}
					decision = durableRequestDecision{p.Response, p.Status, true}
					return nil
				}
			}
			var count int
			if err := t.tx.QueryRow(`SELECT count(*) FROM task_requests`).Scan(&count); err != nil {
				return err
			}
			if count >= idempotencyMaxSize {
				decision = durableRequestDecision{map[string]any{"status": "failed", "error": "idempotency store full; unresolved requests require inspection"}, http.StatusTooManyRequests, true}
				return nil
			}
			_, err = t.tx.Exec(`INSERT INTO task_requests(key,fingerprint,owner,created,job_id,status) VALUES(?,?,?,?,?,0)`, key, fingerprint, owner, nowFloat(), "")
			return err
		}
		if err != nil {
			return err
		}
		if storedFingerprint != fingerprint {
			decision = durableRequestConflict()
			return nil
		}
		decision.replay = true
		if payload != nil {
			if err = json.Unmarshal(payload, &decision.response); err != nil {
				return err
			}
			decision.status = status
			return nil
		}
		if jobID != "" {
			decision.response = map[string]any{"job_id": jobID, "task_id": jobID, "status": "running", "background": true}
			decision.status = http.StatusOK
			return nil
		}
		alive, err := s.taskOwnerAlive(storedOwner)
		if err != nil {
			return err
		}
		if alive {
			decision.response = map[string]any{"status": "running", "message": "original request is being committed; retry the same key"}
			decision.status = http.StatusAccepted
			return nil
		}
		decision.response = map[string]any{"status": "failed", "error": map[string]any{"code": "task_outcome_unknown", "message": "request owner exited before recording its outcome; inspect before submitting a new operation"}}
		decision.status = http.StatusConflict
		return nil
	})
	return decision, err
}

func durableRequestConflict() durableRequestDecision {
	return durableRequestDecision{map[string]any{"detail": "idempotency_key was already used for different arguments"}, http.StatusConflict, true}
}

func (t *taskTransaction) finishRequest(key string, response map[string]any, status int) error {
	if key == "" {
		return nil
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(`UPDATE task_requests SET response=?,status=?,job_id=CASE WHEN ?='' THEN job_id ELSE ? END WHERE key=?`, payload, status, firstString(response, "task_id", "job_id"), firstString(response, "task_id", "job_id"), key)
	return err
}
func (t *taskTransaction) linkRequestTask(key, id string) error {
	if key == "" {
		return nil
	}
	result, err := t.tx.Exec(`UPDATE task_requests SET job_id=? WHERE key=? AND (job_id='' OR job_id=?)`, id, key, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("request reservation is missing or already belongs to another task")
	}
	return nil
}

func (s *Server) executeDurableTaskRequest(key, fingerprint string, operation func() (map[string]any, int)) (map[string]any, int) {
	decision, err := s.reserveDurableTaskRequest(key, fingerprint)
	if err != nil {
		return taskPersistenceFailure(err), http.StatusServiceUnavailable
	}
	if decision.replay {
		if id := firstString(decision.response, "task_id", "job_id"); id != "" && decision.status == http.StatusOK {
			s.mu.Lock()
			err := s.refreshTaskRecordsLocked(id)
			if err == nil {
				if job := s.shellJobs[id]; job != nil && (job.Status == "completed" || job.Status == "failed" || job.Status == "cancelled") {
					decision.response = shellJobResponse(job)
				}
				if job := s.relayJobs[id]; job != nil && (job.Status == "completed" || job.Status == "failed" || job.Status == "cancelled") {
					decision.response = relayJobResponse(job)
				}
			}
			s.mu.Unlock()
			if err != nil {
				return taskPersistenceFailure(err), http.StatusServiceUnavailable
			}
		}
		return decision.response, decision.status
	}
	response, status := operation()
	if taskResponseStatus(response) == http.StatusServiceUnavailable {
		// A known pre-commit failure is retryable with the SAME key. Release
		// only a reservation with no committed task association. If a task
		// exists, preserve its pending association rather than cache a transient
		// response-read failure as a permanent result or enqueue another task.
		var released bool
		err = s.withTaskDatabase(func(t *taskTransaction) error {
			result, err := t.tx.Exec(`DELETE FROM task_requests WHERE key=? AND job_id='' AND response IS NULL`, key)
			if err != nil {
				return err
			}
			rows, err := result.RowsAffected()
			released = rows == 1
			return err
		})
		if err != nil {
			return response, http.StatusServiceUnavailable
		}
		if released {
			delete(response, "job_id")
			delete(response, "task_id")
		}
		return response, http.StatusServiceUnavailable
	}
	err = s.withTaskDatabase(func(t *taskTransaction) error { return t.finishRequest(key, response, status) })
	if err != nil {
		failure := taskPersistenceFailure(err)
		if id := firstString(response, "task_id", "job_id"); id != "" {
			failure["job_id"] = id
			failure["task_id"] = id
		}
		return failure, http.StatusServiceUnavailable
	}
	return response, status
}
