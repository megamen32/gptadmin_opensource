package hub

import (
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

type localShellAdmissionKey struct{}
type localShellAuthStateKey struct{}

type localShellExecutorIdentity struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
}

func (s *Server) localShellIdentityLocked(target string) *localShellExecutorIdentity {
	pin := s.localExecutors[target]
	if pin["server_id"] == "" || pin["fingerprint"] == "" || pin["public_key"] == "" || s.localExecutorTokens[target] == "" {
		return nil
	}
	return &localShellExecutorIdentity{ID: pin["server_id"], Fingerprint: pin["fingerprint"]}
}

func (s *Server) localShellEligibleLocked(target string) bool {
	agent := s.agents[target]
	if agent == nil || agent.Status == "disabled" || agent.Status == "awaiting_approval" || !truthyAny(agent.Meta["approved"]) {
		return false
	}
	owner, err := s.executorEnrollmentLocked(target)
	return err == nil && owner != nil && owner.bound() && owner.NodeID == s.quorumNode.Status().NodeID && owner.State != nodeStateDraining
}

// Move the majority/term fence to the admission of an exact addressable
// command. Its publishing transaction still binds any cross-Hub retry key.
// Only this live task owner holds the inputs and can hand them to its executor.
func (s *Server) fenceLocalShellHandoffLocked(r *http.Request, target string, job *shellJob) error {
	if s.quorumNode == nil && offlineShellMCP(r) {
		return s.fenceMemberOfflineShellLocked(r, target, job)
	}
	if !s.quorumControlOnly() || r == nil ||
		r.Context().Value(localShellAdmissionKey{}) != target || job.ToolName != "shell_exec" ||
		job.FilesystemACL != nil || job.ReceiptTarget != target || target != "shell:"+job.Server {
		return nil
	}
	if offlineShellMCP(r) {
		authority, ok := r.Context().Value(offlineShellAuthorityKey{}).(offlineShellAuthority)
		pin := s.localShellIdentityLocked(target)
		_, applyErr := s.quorumNode.ExternalApplyError()
		if !ok || applyErr != nil || authority.index == 0 || authority.index != s.quorumNode.ExternalAppliedIndex() || job.OwnerID == "" || !offlineShellProfileAllows(r, target) ||
			pin == nil || !s.offlineShellExecutorAllowedLocked(r, target) || r.Context().Err() != nil {
			return errExecutorUnavailable
		}
		job.LocalExecutor = pin
		job.offlineShell = true
		return nil
	}
	if err := s.verifyOwnerDispatch(r.Context()); err != nil {
		return err
	}
	term, ok := r.Context().Value(ownerAdmissionTermKey{}).(uint64)
	if !ok || term == 0 || job.OwnerID == "" {
		return nil
	}
	pin := s.localShellIdentityLocked(target)
	if pin == nil {
		return nil // Remote executors still take the leader-owned poll path.
	}
	state, ok := r.Context().Value(localShellAuthStateKey{}).(authSnapshotState)
	if !ok || state.Pending || state.Generation == 0 || state != s.authState || !s.localShellEligibleLocked(target) {
		return errExecutorUnavailable
	}
	job.LocalExecutor = pin
	return nil
}

// A receipt can outlive the Hub process and its per-process token. Compare
// the re-registered identity to the enrollment fixed at original admission.
func (s *Server) localShellReceiptMatchesLocked(r *http.Request, job *shellJob) bool {
	if job.LocalExecutor == nil {
		return true
	}
	target := "shell:" + job.Server
	pin := s.localShellIdentityLocked(target)
	return pin != nil && *pin == *job.LocalExecutor && tokenMatches(r, s.localExecutorTokens[target])
}

func (s *Server) localShellDeliveryFencedLocked(job *shellJob) bool {
	return s.quorumControlOnly() && !s.taskRuntimeClosed && s.taskOwner != nil && job.OwnerID == s.taskOwner.id && job.LocalExecutor != nil
}

func (s *Server) pollFencedLocalShell(w http.ResponseWriter, r *http.Request) bool {
	if !s.quorumControlOnly() || r.Method != http.MethodGet || r.Header.Get("X-GPTAdmin-Quorum-Forwarded") != "" ||
		!strings.HasPrefix(r.URL.Path, "/queue/") {
		return false
	}
	name := strings.TrimPrefix(r.URL.Path, "/queue/")
	if name == "" || name != canonicalShellQueueName(name) || strings.ContainsAny(name, "/\\") {
		return false
	}
	target := "shell:" + name
	s.mu.Lock()
	defer s.mu.Unlock()
	pin := s.localExecutors[target]
	token := s.localExecutorTokens[target]
	q := r.URL.Query()
	if len(s.shellQueues[name]) == 0 || s.taskRuntimeClosed || s.taskOwner == nil || token == "" || !tokenMatches(r, token) ||
		q.Get("server_id") != pin["server_id"] || q.Get("fingerprint") != pin["fingerprint"] || q.Get("public_key") != pin["public_key"] {
		return false
	}
	if agent := s.agents[target]; agent == nil || agent.Status == "disabled" || agent.Status == "awaiting_approval" {
		return false
	}
	eligible := s.localShellEligibleLocked(target)
	identity := localShellExecutorIdentity{ID: pin["server_id"], Fingerprint: pin["fingerprint"]}
	queue := s.shellQueues[name]
	limit := shellQueueResponseLimit(r)
	oversized := false
	inspected := 0
	for _, id := range queue {
		job := s.shellJobs[id]
		if job == nil || job.Status != "queued" || !s.localShellDeliveryFencedLocked(job) || *job.LocalExecutor != identity || (!eligible && !job.offlineShell) {
			continue
		}
		if inspected == 16 {
			break
		}
		inspected++
		if r.Context().Err() != nil {
			return true
		}
		if err := s.refreshTaskRecordsLocked(id); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "task state unavailable; retry"})
			return true
		}
		if job.Status != "queued" || !s.localShellDeliveryFencedLocked(job) || *job.LocalExecutor != identity || (!eligible && !job.offlineShell) {
			continue
		}
		if expired, err := s.expireQueuedRequestLocked(id, job.CreatedAt); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "task state unavailable; retry"})
			return true
		} else if expired {
			continue
		}
		encoded, err := s.encodeShellQueueJobLocked(job)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "queue payload cannot be encoded; job remains queued"})
			return true
		}
		if len(encoded)+1 > limit {
			oversized = true
			continue
		}
		if s.commitShellQueueDispatchLocked(w, job) {
			s.shellQueues[name] = slices.DeleteFunc(s.shellQueues[name], func(queued string) bool { return queued == id })
			writeShellQueueEnvelope(w, encoded)
		}
		return true
	}
	if oversized {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"detail": "queue payload exceeds agent response limit; job remains queued; update agent"})
	}
	return oversized
}

func (s *Server) encodeShellQueueJobLocked(job *shellJob) ([]byte, error) {
	payload := map[string]any{"id": job.ID, "trace_id": job.TraceID, "traceparent": job.TraceParent, "tool_name": job.ToolName, "arguments": job.Arguments, "cmd": job.Cmd, "cwd": job.Cwd, "timeout": job.Timeout, "env": job.Env, "runtime_settings": s.shellRuntimeSettingsLocked()}
	payload["execution_origin"] = executionOriginForCaller(job.Caller)
	if job.FilesystemACL != nil {
		payload["filesystem_acl"] = job.FilesystemACL
	}
	return json.Marshal(payload)
}

func (s *Server) commitShellQueueDispatchLocked(w http.ResponseWriter, job *shellJob) bool {
	previousStatus, previousStarted := job.Status, job.StartedAt
	job.Status = "running"
	job.StartedAt = nowFloat()
	if err := s.saveTaskStateLocked(job.ID); err != nil {
		log.Printf("task dispatch commit failed: %v", err)
		job.Status, job.StartedAt = previousStatus, previousStarted
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "task dispatch persistence failed; retry polling"})
		return false
	}
	return true
}

func writeShellQueueEnvelope(w http.ResponseWriter, encoded []byte) {
	encoded = append(encoded, '\n')
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(encoded)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}
