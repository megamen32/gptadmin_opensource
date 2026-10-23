package hub

import (
	"database/sql"
	"encoding/json"
	"strings"
)

func adminPreview(value any) string {
	if value == nil {
		return ""
	}
	var text string
	if v, ok := value.(string); ok {
		text = v
	} else {
		data, err := json.Marshal(value)
		if err != nil {
			return "Не удалось сформировать превью"
		}
		text = string(data)
	}
	runes := []rune(text)
	if len(runes) > 600 {
		return string(runes[:600]) + "…"
	}
	return text
}

func (s *Server) adminJobsDataLocked(page ...int) map[string]any {
	offset, limit := 0, 200
	if len(page) == 2 {
		offset, limit = page[0], page[1]
	}
	if offset < 0 {
		offset = 0
	}
	if limit < 1 || limit > 500 {
		limit = 200
	}
	var recent, queued, background []map[string]any
	counts := map[string]int{}
	count, end := 0, 0
	if s.cfg.ConfigDir != "" {
		n, c, err := s.taskHistoryCountsLocked()
		if err != nil {
			return map[string]any{"error": "task history unavailable"}
		}
		count, counts = n, c
		if offset > count {
			offset = count
		}
		for _, spec := range []struct {
			where         string
			offset, limit int
			out           *[]map[string]any
		}{
			{"", offset, limit, &recent},
			{"json_extract(payload,'$.status') IN ('queued','queued_offline')", 0, 200, &queued},
			{"json_extract(payload,'$.status') IN ('running','dispatching')", 0, 200, &background},
		} {
			state, err := s.taskMetadataPage(spec.where, nil, spec.offset, spec.limit)
			if err != nil {
				return map[string]any{"error": "task history unavailable"}
			}
			*spec.out = taskInventoryItems(state)
		}
		end = offset + len(recent)
	} else {
		inventory, _ := s.taskInventoryLocked()
		items := taskInventoryItems(inventory)
		count = len(items)
		queued, background = []map[string]any{}, []map[string]any{}
		for _, item := range items {
			status := firstString(item, "status")
			counts[status]++
			if (status == "queued" || status == "queued_offline") && len(queued) < 200 {
				queued = append(queued, item)
			}
			if (status == "running" || status == "dispatching") && len(background) < 200 {
				background = append(background, item)
			}
		}
		if offset > count {
			offset = count
		}
		end = offset + limit
		if end > count {
			end = count
		}
		recent = items[offset:end]
	}
	ids, seen := []string{}, map[string]bool{}
	for _, list := range [][]map[string]any{recent, queued, background} {
		for _, item := range list {
			id := firstString(item, "job_id")
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	state := persistedTaskState{}
	if s.cfg.ConfigDir != "" && len(ids) > 0 {
		var err error
		state, err = s.readTaskRecords(ids...)
		if err != nil {
			return map[string]any{"error": "task history unavailable"}
		}
	}
	// Build previews only for displayed records, not every historical result.
	for _, list := range [][]map[string]any{recent, queued, background} {
		for _, item := range list {
			if _, done := item["preview_loaded"]; done {
				continue
			}
			item["preview_loaded"] = true
			id := firstString(item, "job_id")

			if j, ok := state.Relay[id]; ok {
				item["arguments_preview"] = adminPreview(j.Params)
				item["result_preview"] = adminPreview(j.Result)
				item["error_preview"] = adminPreview(j.Error)
			}
			if j, ok := state.Shell[id]; ok {
				item["result_preview"] = adminPreview(j.Result)
				item["error_preview"] = adminPreview(j.Error)
			}
			if j := s.relayJobs[id]; j != nil {
				item["arguments_preview"] = adminPreview(j.Params)
				item["result_preview"] = adminPreview(j.Result)
				item["error_preview"] = adminPreview(j.Error)
			}
			if j := s.shellJobs[id]; j != nil {
				item["command"] = adminPreview(redactSecretValues(j.Cmd, j.SecretValues))
				item["arguments_preview"] = adminPreview(redactSecretValues(j.Arguments, j.SecretValues))
				item["result_preview"] = adminPreview(redactSecretValues(j.Result, j.SecretValues))
				item["error_preview"] = adminPreview(redactSecretValues(j.Error, j.SecretValues))
			}
		}
	}
	return map[string]any{"count": count, "status_counts": counts, "queued": queued, "background": background, "recent": recent, "offset": offset, "recent_limit": limit, "recent_truncated": end < count}
}

// adminJobsForServerLocked filters on the exact ShellMCP queue identity before
// ordering and applying the requested page. It never searches rendered labels
// or decodes unrelated task results.
func (s *Server) adminJobsForServerLocked(serverID string, offset, limit int) map[string]any {
	name := strings.TrimPrefix(serverID, "shell:")
	if offset < 0 {
		offset = 0
	}
	if limit < 1 || limit > 500 {
		limit = 200
	}
	state := persistedTaskState{Shell: map[string]persistedShellTask{}, Relay: map[string]persistedRelayTask{}}
	counts := map[string]int{}
	count := 0
	var err error
	if s.cfg.ConfigDir != "" {
		err = s.withTaskReadDatabase(func(tx *sql.Tx) error {
			rows, err := tx.Query(`SELECT status,n FROM task_status_counts WHERE kind='shell' AND server=? AND n>0`, name)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var status string
				var n int
				if err := rows.Scan(&status, &n); err != nil {
					return err
				}
				counts[status] = n
				count += n
			}
			return rows.Err()
		})
		if err != nil {
			return map[string]any{"error": "task history unavailable"}
		}
		if offset > count {
			offset = count
		}
		state, err = s.taskMetadataPage("kind='shell' AND json_extract(payload,'$.server')=?", []any{name}, offset, limit)
		if err != nil {
			return map[string]any{"error": "task history unavailable"}
		}
	} else {
		all, err := s.taskInventoryLocked()
		if err != nil {
			return map[string]any{"error": "task history unavailable"}
		}
		for id, job := range all.Shell {
			if job.Server != name {
				continue
			}
			state.Shell[id] = job
			counts[job.Status]++
			count++
		}
		if offset > count {
			offset = count
		}
		items := taskInventoryItems(state)
		end := min(offset+limit, count)
		filtered := persistedTaskState{Shell: map[string]persistedShellTask{}, Relay: map[string]persistedRelayTask{}}
		for _, item := range items[offset:end] {
			id := firstString(item, "job_id")
			filtered.Shell[id] = state.Shell[id]
		}
		state = filtered
	}
	recent := taskInventoryItems(state)
	ids := make([]string, 0, len(recent))
	for _, item := range recent {
		ids = append(ids, firstString(item, "job_id"))
	}
	full := persistedTaskState{Shell: map[string]persistedShellTask{}, Relay: map[string]persistedRelayTask{}}
	if s.cfg.ConfigDir != "" && len(ids) > 0 {
		var err error
		full, err = s.readTaskRecords(ids...)
		if err != nil {
			return map[string]any{"error": "task history unavailable"}
		}
	} else {
		full = state
	}
	for _, item := range recent {
		id := firstString(item, "job_id")
		if job, ok := full.Shell[id]; ok {
			item["result_preview"] = adminPreview(job.Result)
			item["error_preview"] = adminPreview(job.Error)
		}
		if job := s.shellJobs[id]; job != nil {
			item["command"] = adminPreview(redactSecretValues(job.Cmd, job.SecretValues))
			item["arguments_preview"] = adminPreview(redactSecretValues(job.Arguments, job.SecretValues))
			item["result_preview"] = adminPreview(redactSecretValues(job.Result, job.SecretValues))
			item["error_preview"] = adminPreview(redactSecretValues(job.Error, job.SecretValues))
		}
	}
	end := offset + len(recent)
	return map[string]any{"count": count, "status_counts": counts, "queued": []map[string]any{}, "background": []map[string]any{}, "recent": recent, "offset": offset, "recent_limit": limit, "recent_truncated": end < count}
}
