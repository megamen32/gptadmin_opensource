package hub

// refreshTaskRecordsLocked reads only the requested task/group, not the history.
// Persistent revision is the authority; updating existing objects preserves
// local waiter pointers and creator-only executable arguments.
func (s *Server) refreshTaskRecordsLocked(ids ...string) error {
	if s.cfg.ConfigDir == "" || len(ids) == 0 {
		return nil
	}
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
						j = &shellJob{}
						s.shellJobs[id] = j
					}
					j.RequestKey = p.RequestKey
					j.ID = p.ID
					j.Revision = p.Revision
					j.OwnerID = p.OwnerID
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
			}
			if p, ok := state.Relay[id]; ok {
				j := s.relayJobs[id]
				if j == nil || p.Revision > j.Revision {
					if j == nil {
						j = &relayJob{}
						s.relayJobs[id] = j
					}
					*j = relayJob{ID: p.ID, RequestKey: p.RequestKey, Revision: p.Revision, OwnerID: p.OwnerID, AgentID: p.AgentID, TraceID: p.TraceID, TraceParent: p.TraceParent, Method: p.Method, Params: p.Params, CreatedAt: p.CreatedAt, StartedAt: p.StartedAt, DoneAt: p.DoneAt, Status: p.Status, Result: p.Result, Error: p.Error, ParentTaskID: p.ParentTaskID}
				}
				if j.Method == "task/group" {
					pending = append(pending, taskGroupChildIDs(j)...)
				}
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
