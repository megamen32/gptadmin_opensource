package hub

import (
	"encoding/json"
	"sort"
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
	items := make([]map[string]any, 0, len(s.relayJobs)+len(s.shellJobs))
	for _, j := range s.relayJobs {
		if j != nil {
			items = append(items, map[string]any{"job_id": j.ID, "task_id": j.ID, "server_id": j.AgentID, "kind": "mcp_relay", "method": j.Method, "tool_name": firstString(j.Params, "name"), "status": j.Status, "created_at": j.CreatedAt, "started_at": j.StartedAt, "completed_at": j.DoneAt})
		}
	}
	for _, j := range s.shellJobs {
		if j != nil {
			items = append(items, map[string]any{"job_id": j.ID, "task_id": j.ID, "server": j.Server, "server_id": "shell:" + j.Server, "kind": "shell", "tool_name": j.ToolName, "status": j.Status, "created_at": j.CreatedAt, "started_at": j.StartedAt, "completed_at": j.DoneAt})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i]["created_at"].(float64), items[j]["created_at"].(float64)
		if a == b {
			return firstString(items[i], "job_id") > firstString(items[j], "job_id")
		}
		return a > b
	})
	queued, background := []map[string]any{}, []map[string]any{}
	counts := map[string]int{}
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
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	recent := items[offset:end]
	// Build previews only for displayed records, not every historical result.
	for _, list := range [][]map[string]any{recent, queued, background} {
		for _, item := range list {
			if _, done := item["preview_loaded"]; done {
				continue
			}
			item["preview_loaded"] = true
			id := firstString(item, "job_id")
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
	return map[string]any{"count": len(items), "status_counts": counts, "queued": queued, "background": background, "recent": recent, "offset": offset, "recent_limit": limit, "recent_truncated": end < len(items)}
}
