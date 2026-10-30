package hub

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// NodeDrainCounters is the drain readiness view of locally owned work.
type NodeDrainCounters struct {
	ActiveSubmissions    int `json:"active_submissions"`
	ActiveCallbacks      int `json:"active_callbacks"`
	UnfinishedExecutions int `json:"unfinished_executions"`
	OutboxPending        int `json:"outbox_pending"`
}

func (c NodeDrainCounters) allZero() bool {
	return c.ActiveSubmissions == 0 && c.ActiveCallbacks == 0 && c.UnfinishedExecutions == 0 && c.OutboxPending == 0
}

// NodeDrainStatus is the owner-facing drain state. ready_to_stop is only true
// while draining once no gated submission, executor callback, locally owned
// task or undelivered outbox entry remains.
type NodeDrainStatus struct {
	Draining    bool              `json:"draining"`
	Reason      string            `json:"reason"`
	ReadyToStop bool              `json:"ready_to_stop"`
	Since       string            `json:"since,omitempty"`
	Counters    NodeDrainCounters `json:"counters"`
}

// NodeRuntimeCounters lets drain read the in-process executor's in-flight
// callbacks and undelivered outbox depth without importing the executor.
type NodeRuntimeCounters interface {
	ActiveCallbacks() int
	OutboxPending() int
}

const nodeDrainRetryDetail = "node is draining; retry after drain completes"

// EnableNodeDrain flips the node into draining state. It is idempotent: a
// second call only updates the reason and never moves the drain start time.
func (s *Server) EnableNodeDrain(reason string) {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	s.mu.Lock()
	hook := s.nodeRuntime
	s.mu.Unlock()
	if gate, ok := hook.(interface{ SetQueueDraining(bool) }); ok {
		gate.SetQueueDraining(true)
	}
	if gate, ok := hook.(interface{ SetQueueDrainPending(bool) }); ok {
		gate.SetQueueDrainPending(s.unfinishedExecutionCount() > 0)
	}
	if !s.nodeDrainActive {
		s.nodeDrainActive = true
		s.nodeDrainSince = time.Now()
	}
	s.nodeDrainReason = reason
}

// CancelNodeDrain returns the node to normal admission.
func (s *Server) CancelNodeDrain() {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	s.mu.Lock()
	hook := s.nodeRuntime
	s.mu.Unlock()
	s.nodeDrainActive = false
	s.nodeDrainReason = ""
	s.nodeDrainSince = time.Time{}
	if gate, ok := hook.(interface{ SetQueueDraining(bool) }); ok {
		gate.SetQueueDraining(false)
	}
}

// NodeDrainStatus reports the current drain state and readiness counters.
func (s *Server) NodeDrainStatus() NodeDrainStatus {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	draining, reason, since, submissions := s.nodeDrainActive, s.nodeDrainReason, s.nodeDrainSince, s.nodeDrainSubmissions
	counters := NodeDrainCounters{ActiveSubmissions: submissions, UnfinishedExecutions: s.unfinishedExecutionCount()}
	s.mu.Lock()
	hook := s.nodeRuntime
	s.mu.Unlock()
	if gate, ok := hook.(interface{ SetQueueDrainPending(bool) }); ok {
		gate.SetQueueDrainPending(draining && counters.UnfinishedExecutions > 0)
	}
	counters.ActiveCallbacks, counters.OutboxPending = s.nodeRuntimeCounterValues()
	if draining && s.quorumNode != nil && s.quorumPrivateTLS != nil && !s.quorumNode.Status().Leader {
		// Until a leader-authoritative running-task check is wired, a follower
		// cannot prove that an already delivered remote lease has finished.
		counters.UnfinishedExecutions++
		if reason == "" {
			reason = "quorum follower in-flight task verification unavailable"
		}
	}
	return NodeDrainStatus{Draining: draining, Reason: reason, Since: drainTimestamp(since), ReadyToStop: draining && counters.allZero(), Counters: counters}
}

func drainTimestamp(when time.Time) string {
	if when.IsZero() {
		return ""
	}
	return when.Format(time.RFC3339)
}

func (s *Server) nodeRuntimeCounterValues() (callbacks, outbox int) {
	s.mu.Lock()
	hook := s.nodeRuntime
	s.mu.Unlock()
	if hook == nil {
		return 0, 0
	}
	return hook.ActiveCallbacks(), hook.OutboxPending()
}

// SetNodeRuntimeCounters wires the in-process executor so drain can observe
// its callbacks and outbox. The combined node binary calls this before serving.
func (s *Server) SetNodeRuntimeCounters(counters NodeRuntimeCounters) {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	if gate, ok := counters.(interface{ SetQueueDraining(bool) }); ok {
		gate.SetQueueDraining(s.nodeDrainActive)
	}
	if gate, ok := counters.(interface{ SetQueueDrainPending(bool) }); ok {
		gate.SetQueueDrainPending(s.nodeDrainActive && s.unfinishedExecutionCount() > 0)
	}
	s.mu.Lock()
	s.nodeRuntime = counters
	s.mu.Unlock()
}

// unfinishedExecutionCount counts locally owned non-terminal shell and relay
// tasks, mirroring the task store's kinds ('shell','relay') with done=0.
func (s *Server) unfinishedExecutionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	ownerID := ""
	if s.taskOwner != nil {
		ownerID = s.taskOwner.id
	}
	for _, job := range s.shellJobs {
		if job != nil && !taskTerminal(job.Status) && s.drainOwnedLocallyLocked(job.OwnerID, ownerID) {
			count++
		}
	}
	for _, job := range s.relayJobs {
		// A never-dispatched schema read has no running executor or business
		// side effect. Keep its durable receipt, but do not fence deployment
		// on catalog discovery. Started, unknown and tools/call still fence.
		queuedDiscovery := job != nil && job.Method == "tools/list" && job.Status == "queued" &&
			job.StartedAt == 0 && job.DoneAt == 0 && job.Result == nil && job.Error == nil && job.ParentTaskID == ""
		if job != nil && !taskTerminal(job.Status) && !queuedDiscovery && s.drainOwnedLocallyLocked(job.OwnerID, ownerID) {
			count++
		}
	}
	return count
}

// drainOwnedLocallyLocked avoids the alive-check I/O of taskOwnedElsewhereLocked:
// records without an owner or owned by this process count, records owned by
// another live hub never block this node's drain.
func (s *Server) drainOwnedLocallyLocked(ownerID, localOwnerID string) bool {
	return ownerID == "" || localOwnerID == "" || ownerID == localOwnerID
}

// nodeDrainBlocked reports whether admission is currently closed.
func (s *Server) nodeDrainBlocked() bool {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	return s.nodeDrainActive
}

// beginNodeSubmission atomically admits one gated submission. The drain check
// and the in-flight increment share one lock, so a submission admitted before
// EnableNodeDrain always holds ready_to_stop open until it is released.
// Receipt and local Hub reads pass through; executor reads are gated because
// they also create queued work and must not enter after the drain fence.
func (s *Server) beginNodeSubmission(target, toolName string) (func(), bool) {
	if nodePeerJobTool(toolName) || ((target == "hub" || target == "control:hub") && isReadOnlyTool(target, toolName)) {
		return func() {}, true
	}
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	if s.nodeDrainActive {
		return nil, false
	}
	s.nodeDrainSubmissions++
	return func() {
		s.drainMu.Lock()
		s.nodeDrainSubmissions--
		s.drainMu.Unlock()
	}, true
}

// writeNodeDrainFailure rejects a gated submission with 503 and retryable
// semantics. /mcp keeps the node peer failure envelope (-32000 with data);
// every other consumer gets the plain retryable JSON body.
func (s *Server) writeNodeDrainFailure(w http.ResponseWriter, endpoint string, body []byte, target, origin string) {
	payload := map[string]any{"detail": nodeDrainRetryDetail, "retryable": true, "owner_target": target, "owner_endpoint": origin}
	if endpoint == "/mcp" {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal(body, &envelope)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"jsonrpc": "2.0", "id": envelope["id"],
			"error": map[string]any{"code": -32000, "message": nodeDrainRetryDetail, "data": payload}})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, payload)
}

func (s *Server) nodeDrainHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.NodeDrainStatus())
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "POST or GET required"})
		return
	}
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	switch strings.TrimSpace(req.Action) {
	case "begin":
		s.EnableNodeDrain(req.Reason)
	case "status":
	case "cancel":
		s.CancelNodeDrain()
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "action must be one of begin, status, cancel"})
		return
	}
	writeJSON(w, http.StatusOK, s.NodeDrainStatus())
}
