package hub

import (
	"fmt"
	"net/http"
)

// taskMutation is the memory half of a durable transition. Callers hold s.mu,
// replace reference-valued fields instead of mutating their contents, then
// commit before acknowledging or publishing work to an executor. Rollback
// preserves existing pointers held by waiters, including group children.
type taskMutation struct {
	s             *Server
	ids           []string
	shell         map[string]*shellJob
	relay         map[string]*relayJob
	shellOriginal map[string]*shellJob
	relayOriginal map[string]*relayJob
	controls      map[string][]shellControl
	queues        map[string][]string
	relayQueues   map[string][]string
	idempotency   map[string]idempotencyEntry
	approvals     map[string]approvalRequest
}

func (s *Server) beginTaskMutationLocked(ids ...string) *taskMutation {
	m := &taskMutation{s: s, shell: map[string]*shellJob{}, relay: map[string]*relayJob{}, shellOriginal: map[string]*shellJob{}, relayOriginal: map[string]*relayJob{}, controls: map[string][]shellControl{}, queues: map[string][]string{}, relayQueues: map[string][]string{}, approvals: map[string]approvalRequest{}}
	seen := map[string]bool{}
	pending := append([]string(nil), ids...)
	for len(pending) > 0 {
		id := pending[0]
		pending = pending[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		m.ids = append(m.ids, id)
		if j := s.shellJobs[id]; j != nil {
			copy := *j
			m.shell[id] = &copy
			m.shellOriginal[id] = j
			if _, ok := m.controls[j.Server]; !ok {
				m.controls[j.Server] = append([]shellControl(nil), s.shellControls[j.Server]...)
				m.queues[j.Server] = append([]string(nil), s.shellQueues[j.Server]...)
			}
			if a := s.approvals[j.ApprovalID]; a != nil {
				m.approvals[j.ApprovalID] = *a
			}
		}
		if j := s.relayJobs[id]; j != nil {
			copy := *j
			m.relay[id] = &copy
			m.relayOriginal[id] = j
			if _, ok := m.relayQueues[j.AgentID]; !ok {
				m.relayQueues[j.AgentID] = append([]string(nil), s.relayQueues[j.AgentID]...)
			}
			if j.Method == "task/group" {
				pending = append(pending, taskGroupChildIDs(j)...)
			}
		}
	}
	m.idempotency = map[string]idempotencyEntry{}

	for key, entry := range s.idempotency {
		if entry != nil && seen[entry.JobID] {
			m.idempotency[key] = *entry
		}
	}
	return m
}
func (m *taskMutation) rollback() {

	for key, previous := range m.idempotency {
		if entry := m.s.idempotency[key]; entry != nil {
			*entry = previous
		}
	}
	for _, id := range m.ids {
		if old := m.shell[id]; old != nil {
			*m.shellOriginal[id] = *old
			m.s.shellJobs[id] = m.shellOriginal[id]
		} else {
			delete(m.s.shellJobs, id)
		}
		if old := m.relay[id]; old != nil {
			*m.relayOriginal[id] = *old
			m.s.relayJobs[id] = m.relayOriginal[id]
		} else {
			delete(m.s.relayJobs, id)
		}
	}
	for server, q := range m.controls {
		m.s.shellControls[server] = q
	}
	for server, q := range m.queues {
		m.s.shellQueues[server] = q
	}
	for server, q := range m.relayQueues {
		m.s.relayQueues[server] = q
	}
	for id, old := range m.approvals {
		if a := m.s.approvals[id]; a != nil {
			*a = old
		}
	}
}
func (m *taskMutation) commit() error {
	if err := m.s.saveTaskStateLocked(m.ids...); err != nil {
		m.rollback()
		return err
	}
	return nil
}
func taskCommitRPCError(err error) map[string]any {
	return map[string]any{"code": -32603, "message": "task persistence failed; operation was not committed", "data": map[string]any{"retryable": true, "detail": err.Error()}}
}

func taskPersistenceFailure(err error) map[string]any {
	return map[string]any{"status": "failed", "error": map[string]any{"code": "task_persistence_failed", "message": fmt.Sprintf("task persistence failed: %v", err)}}
}
func taskPersistenceRPCError(err error) map[string]any { return taskCommitRPCError(err) }
func taskResponseStatus(response map[string]any) int {
	if firstString(mapValue(response["error"]), "code") == "task_persistence_failed" {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

func (s *Server) relayEnqueueFailure(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job := s.relayJobs[id]; job != nil && firstString(mapValue(job.Error), "code") == "task_persistence_failed" {
		return relayJobResponse(job)
	}
	return nil
}
