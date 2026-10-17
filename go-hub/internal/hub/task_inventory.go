package hub

import (
	"database/sql"
	"sort"
)

// Inventory queries project metadata inside SQLite: results are never decoded
// merely to list history. The returned snapshot is request-local, not cached.
func (s *Server) taskInventoryLocked() (persistedTaskState, error) {
	state := persistedTaskState{Shell: map[string]persistedShellTask{}, Relay: map[string]persistedRelayTask{}}
	if s.cfg.ConfigDir != "" {
		var err error
		state, err = s.readTaskRecordsQuery("kind IN ('shell','relay')", true)
		if err != nil {
			return state, err
		}
	}
	for id, j := range s.shellJobs {
		if j != nil {
			state.Shell[id] = persistedShellTask{ID: id, Server: j.Server, ToolName: j.ToolName, Status: j.Status, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, DoneAt: j.DoneAt}
		}
	}
	for id, j := range s.relayJobs {
		if j != nil {
			state.Relay[id] = persistedRelayTask{ID: id, AgentID: j.AgentID, Method: j.Method, Params: j.Params, Status: j.Status, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, DoneAt: j.DoneAt}
		}
	}
	return state, nil
}

// The legacy list is paged as well: an inventory request must not decode a day
// of output or refill the execution cache. Exact task GET stays lossless.
func (s *Server) shellTaskPageLocked(server string, offset, limit int) (map[string]any, error) {
	state := persistedTaskState{}
	var err error
	count := 0
	if s.cfg.ConfigDir != "" {
		err = s.withTaskReadDatabase(func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT coalesce(sum(n),0) FROM task_status_counts WHERE kind='shell' AND server=?`, server).Scan(&count)
		})
		if err != nil {
			return nil, err
		}
		if offset > count {
			offset = count
		}
		state, err = s.taskMetadataPage("kind='shell' AND json_extract(payload,'$.server')=?", []any{server}, offset, limit)
	} else {
		state, err = s.taskInventoryLocked()
	}
	if err != nil {
		return nil, err
	}
	jobs := make([]persistedShellTask, 0)
	for _, j := range state.Shell {
		if j.Server == server {
			jobs = append(jobs, j)
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt == jobs[j].CreatedAt {
			return jobs[i].ID > jobs[j].ID
		}
		return jobs[i].CreatedAt > jobs[j].CreatedAt
	})
	if s.cfg.ConfigDir == "" {
		count = len(jobs)
		if offset > count {
			offset = count
		}
		last := offset + limit
		if last > count {
			last = count
		}
		jobs = jobs[offset:last]
	}
	if offset > count {
		offset = count
	}
	end := offset + limit
	if end > count {
		end = count
	}
	items := []map[string]any{}
	for _, meta := range jobs {
		var job *shellJob
		if live := s.shellJobs[meta.ID]; live != nil {
			job = live
		} else {
			row, err := s.readTaskRecords(meta.ID)
			if err != nil {
				return nil, err
			}
			if p, ok := row.Shell[meta.ID]; ok {
				job = &shellJob{ID: p.ID, Server: p.Server, Status: p.Status, Result: p.Result, Error: p.Error, CreatedAt: p.CreatedAt, StartedAt: p.StartedAt, DoneAt: p.DoneAt}
			}
		}
		if job != nil {
			row := shellJobResponse(job)
			row["server"] = job.Server
			row["cmd"] = redactSecretValues(job.Cmd, job.SecretValues)
			items = append(items, row)
		}
	}
	return map[string]any{"tasks": items, "count": count, "offset": offset, "limit": limit, "truncated": end < count}, nil
}

func taskInventoryItems(state persistedTaskState) []map[string]any {
	items := make([]map[string]any, 0, len(state.Relay)+len(state.Shell))
	for _, j := range state.Relay {
		items = append(items, map[string]any{"job_id": j.ID, "task_id": j.ID, "server_id": j.AgentID, "kind": "mcp_relay", "method": j.Method, "tool_name": firstString(j.Params, "name"), "status": j.Status, "created_at": j.CreatedAt, "started_at": j.StartedAt, "completed_at": j.DoneAt})
	}
	for _, j := range state.Shell {
		items = append(items, map[string]any{"job_id": j.ID, "task_id": j.ID, "server": j.Server, "server_id": "shell:" + j.Server, "kind": "shell", "tool_name": j.ToolName, "status": j.Status, "created_at": j.CreatedAt, "started_at": j.StartedAt, "completed_at": j.DoneAt})
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i]["created_at"].(float64), items[j]["created_at"].(float64)
		if a == b {
			return firstString(items[i], "job_id") > firstString(items[j], "job_id")
		}
		return a > b
	})
	return items
}

func (s *Server) taskMetadataPage(where string, args []any, offset, limit int) (persistedTaskState, error) {
	predicate := "kind IN ('shell','relay')"
	if where != "" {
		predicate += " AND (" + where + ")"
	}
	return s.readTaskRecordsSQL(predicate+" ORDER BY json_extract(payload,'$.created_at') DESC,key DESC LIMIT ? OFFSET ?", true, append(append([]any(nil), args...), limit, offset))
}

func (s *Server) taskHistoryCountsLocked() (int, map[string]int, error) {
	count := 0
	counts := map[string]int{}
	err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT kind,status,n FROM task_status_counts WHERE n>0`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind, status string
			var n int
			if err := rows.Scan(&kind, &status, &n); err != nil {
				return err
			}
			count += n
			counts[status] += n
		}
		return rows.Err()
	})
	return count, counts, err
}
