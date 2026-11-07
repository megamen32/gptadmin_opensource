package hub

import "sort"

func (s *Server) requestTTLExpiredLocked(created float64) bool {
	ttl := s.hubSettingIntLocked("request_ttl_seconds")
	if ttl <= 0 {
		ttl = 10800
	}
	return created > 0 && nowFloat()-created >= float64(ttl)
}

// Expiration is cancellation, not deletion or retry. The executor's existing
// durable cancel control prevents a late result from reviving an expired task.
func (s *Server) expireRequestLocked(id string) {
	shellActive := s.shellJobs[id] != nil && !taskTerminal(s.shellJobs[id].Status)
	relayActive := s.relayJobs[id] != nil && !taskTerminal(s.relayJobs[id].Status)
	if !shellActive && !relayActive {
		return
	}
	if group := s.relayJobs[id]; group != nil && group.Method == "task/group" {
		for _, child := range taskGroupChildIDs(group) {
			s.expireRequestLocked(child)
		}
	}
	s.cancelTaskLocked(id)
	detail := map[string]any{"code": "request_ttl_expired", "message": "Срок ожидания запроса истёк; повторное выполнение не запускалось", "ttl_seconds": s.hubSettingIntLocked("request_ttl_seconds")}
	if j := s.shellJobs[id]; j != nil && shellActive && j.Status == "cancelled" {
		j.Error = detail
	}
	if j := s.relayJobs[id]; j != nil && relayActive && j.Status == "cancelled" {
		j.Error = detail
	}
}

func (s *Server) expireRequestsLocked() []string {
	ids := map[string]bool{}
	for id, j := range s.shellJobs {
		if j != nil && !taskTerminal(j.Status) && s.requestTTLExpiredLocked(j.CreatedAt) && !s.taskOwnedElsewhereLocked(j.OwnerID) {
			ids[id] = true
		}
	}
	for id, j := range s.relayJobs {
		if j != nil && !taskTerminal(j.Status) && s.requestTTLExpiredLocked(j.CreatedAt) && !s.taskOwnedElsewhereLocked(j.OwnerID) {
			ids[id] = true
		}
	}
	expired := make([]string, 0, len(ids))
	for id := range ids {
		s.expireRequestLocked(id)
		expired = append(expired, id)
	}
	sort.Strings(expired)
	return expired
}

// Poll checks the same deadline immediately before dispatch, without relying
// on the maintenance ticker winning a race with a reconnecting executor.
func (s *Server) expireQueuedRequestLocked(id string, created float64) (bool, error) {
	if !s.requestTTLExpiredLocked(created) {
		return false, nil
	}
	change := s.beginTaskMutationLocked(id)
	s.expireRequestLocked(id)
	if err := change.commit(); err != nil {
		return false, err
	}
	return true, nil
}
