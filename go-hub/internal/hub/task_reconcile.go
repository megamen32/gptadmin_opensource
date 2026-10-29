package hub

// refreshTaskRecordsLocked reads only the requested task/group, not the history.
// Persistent revision is the authority; updating existing objects preserves
// local waiter pointers and creator-only executable arguments.
func (s *Server) refreshTaskRecordsLocked(ids ...string) error {
	if s.cfg.ConfigDir == "" || len(ids) == 0 {
		return nil
	}
	s.pruneExpiredTaskMemoryLocked()
	pending := append([]string(nil), ids...)
	seen := map[string]bool{}
	for len(pending) > 0 {
		batch := []string{}
		for len(pending) > 0 && len(batch) < 256 {
			id := pending[0]
			pending = pending[1:]
			if seen[id] {
				continue
			}
			seen[id] = true
			batch = append(batch, id)
		}
		if len(batch) == 0 {
			continue
		}
		state, err := s.readTaskRecords(batch...)
		if err != nil {
			return err
		}
		for _, id := range batch {
			if p, ok := state.Shell[id]; ok {
				j := s.shellJobs[id]
				if j == nil || p.Revision > j.Revision {
					if j == nil {
						j = &shellJob{CachedAt: nowFloat()}
						s.shellJobs[id] = j
					}
					ownedHere := s.taskOwner != nil && p.OwnerID != "" && p.OwnerID == s.taskOwner.id
					if !ownedHere {
						j.quorumPendingSealed = append([]byte(nil), p.SealedPending...)
						j.quorumPendingUnmaterialized = s.cfg.QuorumConfigFile != "" && p.Status == "queued" && p.StartedAt == 0 && len(p.SealedPending) > 0
					}
					j.RequestKey = p.RequestKey
					j.ID = p.ID
					j.Revision = p.Revision
					j.OwnerID = p.OwnerID
					j.Caller = p.Caller
					j.Server = p.Server
					j.TraceID = p.TraceID
					j.TraceParent = p.TraceParent
					j.ToolName = p.ToolName
					j.Timeout = p.Timeout
					j.CreatedAt = p.CreatedAt
					j.StartedAt = p.StartedAt
					j.DoneAt = p.DoneAt
					j.Status = p.Status
					j.Result = p.Result
					j.Error = p.Error
					j.ApprovalID = p.ApprovalID
					j.ParentTaskID = p.ParentTaskID
					if j.Status == "cancelled" && j.StartedAt > 0 && j.Server != "" {
						s.addTaskCancelControlLocked(j)
					}
				}
			} else if j := s.shellJobs[id]; j != nil && j.Revision > 0 && taskTerminal(j.Status) {
				// A per-ID SQLite read is authoritative for persisted receipts. A
				// terminal cache entry missing from it was pruned or acknowledged
				// elsewhere and must not be served from this Hub's stale memory.
				delete(s.shellJobs, id)
			}
			if p, ok := state.Relay[id]; ok {
				j := s.relayJobs[id]
				if j == nil || p.Revision > j.Revision {
					if j == nil {
						j = &relayJob{CachedAt: nowFloat()}
						s.relayJobs[id] = j
					}
					*j = relayJob{CachedAt: nowFloat(), ID: p.ID, RequestKey: p.RequestKey, Revision: p.Revision, OwnerID: p.OwnerID, Caller: p.Caller, AgentID: p.AgentID, TraceID: p.TraceID, TraceParent: p.TraceParent, Method: p.Method, Params: p.Params, CreatedAt: p.CreatedAt, StartedAt: p.StartedAt, DoneAt: p.DoneAt, Status: p.Status, Result: p.Result, Error: p.Error, ParentTaskID: p.ParentTaskID}
				}
				if j.Method == "task/group" {
					pending = append(pending, taskGroupChildIDs(j)...)
				}
			} else if j := s.relayJobs[id]; j != nil && j.Revision > 0 && taskTerminal(j.Status) {
				// See the Shell branch above: don't let a secondary Hub resurrect
				// a durable receipt after its owning Hub acknowledged it.
				delete(s.relayJobs, id)
			}
			if err := s.recoverOrphanedTaskLocked(id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) addTaskCancelControlLocked(j *shellJob) {
	for _, control := range s.shellControls[j.Server] {
		if control.TaskID == j.ID {
			return
		}
	}
	s.shellControls[j.Server] = append(s.shellControls[j.Server], shellControl{TaskID: j.ID})
}

// A standby notices exited task creators while serving, without a restart.
func (s *Server) recoverOrphanedTaskLocked(id string) error {
	if group := s.relayJobs[id]; group != nil && group.Method == "task/group" {
		return nil
	}
	if job := s.shellJobs[id]; job != nil && s.cfg.QuorumConfigFile != "" && job.Status == "queued" && job.StartedAt == 0 && job.quorumPendingUnmaterialized && len(job.quorumPendingSealed) > 0 {
		// Keep the encrypted input quarantined until a quorum-confirmed leader
		// can safely recover it; never fail or dispatch it during startup.
		return nil
	}
	owner, status := "", ""
	if j := s.shellJobs[id]; j != nil {
		owner, status = j.OwnerID, j.Status
	}
	if j := s.relayJobs[id]; j != nil {
		owner, status = j.OwnerID, j.Status
	}
	if owner == "" || (status != "queued" && status != "input_required") {
		return nil
	}
	alive, err := s.taskOwnerAlive(owner)
	if err != nil {
		return err
	}
	if alive {
		return nil
	}
	change := s.beginTaskMutationLocked(id)
	failure := map[string]any{"code": "task_owner_exited_before_dispatch", "message": "creator Hub exited before dispatch; task was not replayed"}
	if j := s.shellJobs[id]; j != nil {
		j.Status = "failed"
		j.DoneAt = nowFloat()
		j.Error = failure
	}
	if j := s.relayJobs[id]; j != nil {
		j.Status = "failed"
		j.DoneAt = nowFloat()
		j.Error = failure
	}
	return change.commit()
}
