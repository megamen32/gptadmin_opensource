package hub

import "log"

func taskTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

func (s *Server) taskOwnedElsewhereLocked(owner string) bool {
	if owner == "" || (s.taskOwner != nil && s.taskOwner.id == owner) {
		return false
	}
	alive, err := s.taskOwnerAlive(owner)
	if err != nil {
		log.Printf("task owner check failed; maintenance deferred: %v", err)
		return true
	}
	return alive
}

// Maintenance uses the same commit boundary as HTTP mutations. Refresh only
// known active tasks, never all historical output. This also delivers a
// cancellation/result written through another Hub to the creator's memory.
func (s *Server) maintainTaskStateLocked() ([]string, error) {
	if s.taskRuntimeClosed {
		return nil, nil
	}
	ids := []string{}
	for id, j := range s.shellJobs {
		if j != nil && !taskTerminal(j.Status) {
			ids = append(ids, id)
		}
	}
	for id, j := range s.relayJobs {
		if j != nil && !taskTerminal(j.Status) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if err := s.refreshTaskRecordsLocked(ids...); err != nil {
		return nil, err
	}
	change := s.beginTaskMutationLocked(ids...)
	expired := s.expireOrphanedShellJobsLocked()
	groups := s.refreshTaskGroupsLocked()
	if len(expired) > 0 || groups > 0 {
		if err := change.commit(); err != nil {
			return nil, err
		}
	}
	s.cond.Broadcast()
	return expired, nil
}
