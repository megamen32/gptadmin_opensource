package hub

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

type ownerRequestUpdate struct {
	key    string
	op     quorum.Operation
	action string
}

type ownerAdmissionTermKey struct{}

// A long poll may have authenticated before an election. Recheck majority
// contact and the term whose policies admitted it before handing out work.
func (s *Server) verifyOwnerDispatch(ctx context.Context) error {
	if !s.quorumControlOnly() {
		return nil
	}
	node := s.quorumNode
	term, ok := ctx.Value(ownerAdmissionTermKey{}).(uint64)
	if !ok {
		term = s.quorumAuthorityTerm.Load()
	}
	if node == nil || term == 0 || !s.quorumAuthorityActiveWriter.Load() || term != node.CurrentTerm() {
		return errors.New("quorum authority changed before dispatch")
	}
	if err := node.VerifyLeader(ctx); err != nil {
		return err
	}
	if term != node.CurrentTerm() || !s.quorumAuthorityActiveWriter.Load() {
		return errors.New("quorum authority changed before dispatch")
	}
	return nil
}

func ownerRequestWitness(localOwner string, created float64) string {
	return sha256Hex([]byte(localOwner + "\x00" + strconv.FormatFloat(created, 'g', -1, 64)))
}

func (s *Server) updateOwnerRequest(action, digest string, op quorum.Operation) (quorum.OperationDecision, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.quorumNode.UpdateOperation(ctx, action, digest, op)
}

// A locally generated row witness survives restarts. An ambiguous majority
// commit followed by a missing local row is never treated as a fresh request.
func (s *Server) claimOwnerRequest(key, fingerprint, localOwner string, created float64, localExists bool) (durableRequestDecision, error) {
	op := quorum.Operation{Owner: s.quorumNode.Status().NodeID, Fingerprint: sha256Hex([]byte(fingerprint)), Witness: ownerRequestWitness(localOwner, created)}
	res, err := s.updateOwnerRequest("claim", sha256Hex([]byte(key)), op)
	switch {
	case errors.Is(err, quorum.ErrOperationCapacity):
		return durableRequestDecision{status: http.StatusTooManyRequests, replay: true, response: map[string]any{"detail": "owner request reservation budget exhausted"}}, nil
	case errors.Is(err, quorum.ErrOperationConflict):
		return durableRequestConflict(), nil
	case err != nil:
		return durableRequestDecision{}, err
	case res.Created || localExists && res.Operation.Owner == op.Owner && res.Operation.Witness == op.Witness:
		return durableRequestDecision{}, nil
	case res.Operation.JobID != "":
		// Access to the body is checked on read by its original Hub. The
		// scoped key proves only which submission this client is retrying.
		return durableRequestDecision{status: http.StatusOK, replay: true, referenceOnly: true, response: map[string]any{
			"status": "accepted", "job_id": res.Operation.JobID, "task_id": res.Operation.JobID,
			"owner_node_id": res.Operation.Owner, "background": true,
		}}, nil
	case res.Operation.Expires != 0:
		return durableRequestDecision{status: http.StatusServiceUnavailable, replay: true, response: map[string]any{
			"status": "unavailable", "owner_node_id": res.Operation.Owner,
			"error": map[string]any{"code": "result_unavailable", "message": "the original request finished; its response is stored on its owner"},
		}}, nil
	default:
		return durableRequestDecision{status: http.StatusConflict, replay: true, response: map[string]any{
			"status": "failed", "error": map[string]any{"code": "task_outcome_unknown", "message": "the original owner reserved this request; delivery has not been confirmed"},
		}}, nil
	}
}

// A reference is installed before dispatch. Re-delivered results only mutate
// owner-local SQLite. A fenced operation is never rebound to a new job.
func (t *taskTransaction) ownerRequest(key, jobID string) (*ownerRequestUpdate, error) {
	if t.ownerControl == nil || key == "" {
		return nil, nil
	}
	s := t.ownerControl
	digest := sha256Hex([]byte(key))
	op, ok := s.quorumNode.LocalOperation(digest)
	if !ok {
		// An ordinary task admitted before quorum can have no reservation.
		return nil, nil
	}
	if op.Owner != s.quorumNode.Status().NodeID {
		return nil, quorum.ErrOperationOwner
	}
	var localOwner, fingerprint string
	var created float64
	if err := t.tx.QueryRow("SELECT owner,created,fingerprint FROM task_requests WHERE key=?", key).Scan(&localOwner, &created, &fingerprint); err != nil {
		return nil, err
	}
	if op.Witness != ownerRequestWitness(localOwner, created) || op.Fingerprint != sha256Hex([]byte(fingerprint)) || op.JobID != "" && op.JobID != jobID {
		return nil, quorum.ErrOperationOwner
	}
	if op.Expires != 0 {
		return nil, nil
	}
	op.JobID = jobID
	return &ownerRequestUpdate{key: digest, op: op}, nil
}

func (t *taskTransaction) bindOwnerRequest(key, id string) error {
	u, err := t.ownerRequest(key, id)
	if err != nil || u == nil {
		return err
	}
	_, err = t.ownerControl.updateOwnerRequest("bind", u.key, u.op)
	return err
}

func (t *taskTransaction) completeOwnerRequest(key, id string) error {
	u, err := t.ownerRequest(key, id)
	if err != nil || u == nil {
		return err
	}
	u.action = "terminal"
	t.afterOwnerCommit = append(t.afterOwnerCommit, *u)
	return nil
}

func (t *taskTransaction) publishOwnerCompletions() {
	for _, u := range t.afterOwnerCommit {
		// A failed notification retains the safe unresolved reservation.
		// A later receipt read or reconciliation can establish completion.
		_, _ = t.ownerControl.updateOwnerRequest(u.action, u.key, u.op)
	}
}

// The client may already have received its response when this Hub lost
// leadership. Reconcile a durable completion before deleting the only local
// evidence that allows its cluster reservation to expire. Read metadata only.
func (t *taskTransaction) ownerRequestCanExpire(key string) (bool, error) {
	s := t.ownerControl
	digest := sha256Hex([]byte(key))
	op, ok := s.quorumNode.LocalOperation(digest)
	if !ok || op.Owner != s.quorumNode.Status().NodeID {
		return true, nil
	}
	var process, jobID string
	var created float64
	if err := t.tx.QueryRow("SELECT owner,created,job_id FROM task_requests WHERE key=?", key).Scan(&process, &created, &jobID); err != nil {
		return false, err
	}
	if op.Witness != ownerRequestWitness(process, created) {
		return true, nil
	}
	if op.Expires > 0 {
		return op.Expires <= time.Now().Unix(), nil
	}
	if err := t.reconcileOwnerResponse(key, jobID); err != nil {
		return false, err
	}
	return false, nil
}

func (t *taskTransaction) reconcileOwnerResponse(key, jobID string) error {
	if t.ownerControl == nil {
		return nil
	}
	if jobID != "" {
		var done float64
		err := t.tx.QueryRow("SELECT done FROM task_records WHERE key=? AND kind IN ('shell','relay','group') ORDER BY done DESC LIMIT 1", jobID).Scan(&done)
		if errors.Is(err, sql.ErrNoRows) || err == nil && done <= 0 {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return t.completeOwnerRequest(key, jobID)
}
