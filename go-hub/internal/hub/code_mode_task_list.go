package hub

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
)

type taskListCursor struct {
	CreatedAt float64 `json:"created_at"`
	TaskID    string  `json:"task_id"`
}

func encodeTaskListCursor(cursor taskListCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeTaskListCursor(encoded string) (taskListCursor, error) {
	if encoded == "" {
		return taskListCursor{}, errors.New("empty task cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return taskListCursor{}, errors.New("invalid task cursor")
	}
	var cursor taskListCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.TaskID == "" || math.IsNaN(cursor.CreatedAt) || math.IsInf(cursor.CreatedAt, 0) {
		return taskListCursor{}, errors.New("invalid task cursor")
	}
	return cursor, nil
}

func taskListCursorFromRow(row map[string]any) (taskListCursor, error) {
	createdAt, ok := row["created_at"].(float64)
	id := firstString(row, "task_id", "job_id")
	if !ok || id == "" || math.IsNaN(createdAt) || math.IsInf(createdAt, 0) {
		return taskListCursor{}, errors.New("task ledger row has no pagination key")
	}
	return taskListCursor{CreatedAt: createdAt, TaskID: id}, nil
}

// listOwnedTasksForRequest reads the existing durable ledger, never queues an
// operation or replays a prior write. Task details remain protected by the
// exact same per-receipt authorization as MCP tasks/get.
func (s *Server) listOwnedTasksForRequest(r *http.Request, limit int, encodedCursor string) (map[string]any, error) {
	if r == nil || limit < 1 || limit > 20 {
		return nil, errors.New("task list needs an authenticated request and limit 1..20")
	}
	var cursor *taskListCursor
	if encodedCursor != "" {
		decoded, err := decodeTaskListCursor(encodedCursor)
		if err != nil {
			return nil, errors.New("invalid task cursor")
		}
		cursor = &decoded
	}
	// Only profile-authorized callers may see job metadata. Even a known task ID
	// is re-authorized by mcpTaskGetForRequest below.
	if err := authorizeToolCall(r, "hub", "job"); err != nil {
		return nil, err
	}
	claims, _ := r.Context().Value(authClaimsContextKey{}).(map[string]any)
	profile, bound := AccessProfileFromRequest(r)
	admin := s.requestHasAdminRole(r) && !bound
	if len(claims) == 0 && !admin {
		return nil, errors.New("task listing requires an authenticated principal")
	}
	where := ""
	var args []any
	if !admin {
		caller := taskCallerFromRequest(r)
		if caller == nil {
			return nil, errors.New("no task owner is bound to this principal")
		}
		where = "coalesce(json_extract(payload,'$.caller.client_id'),'')=? AND coalesce(json_extract(payload,'$.caller.token_id'),'')=? AND coalesce(json_extract(payload,'$.caller.subject'),'')=? AND coalesce(json_extract(payload,'$.caller.profile_id'),'')=? AND coalesce(json_extract(payload,'$.caller.profile_version'),0)=?"
		args = []any{caller.ClientID, caller.TokenID, caller.Subject, caller.ProfileID, caller.ProfileVersion}
		if bound && profile.ID != caller.ProfileID {
			return nil, errors.New("task owner profile mismatch")
		}
	}
	const pageSize = 40
	const scanMax = 200
	items := make([]map[string]any, 0, limit)
	scanned := 0
	exhausted := false
	for scanned < scanMax && len(items) < limit {
		count := pageSize
		if scanMax-scanned < count {
			count = scanMax - scanned
		}
		state, err := s.taskMetadataPageAfter(where, args, cursor, count)
		if err != nil {
			return nil, fmt.Errorf("task ledger unavailable: %w", err)
		}
		rows := taskInventoryItems(state)
		if len(rows) == 0 {
			exhausted = true
			break
		}
		consumed := 0
		for _, row := range rows {
			consumed++
			last, cursorErr := taskListCursorFromRow(row)
			if cursorErr != nil {
				return nil, fmt.Errorf("task ledger unavailable: %w", cursorErr)
			}
			cursor = &last
			id := firstString(row, "task_id")
			value, rpcErr := s.mcpTaskGetForRequest(r, id, "")
			if rpcErr != nil || firstString(mapValue(value), "taskId") != id {
				continue
			}
			task := mapValue(value)
			progress := mapValue(mapValue(task["_meta"])["io.gptadmin/taskProgress"])
			summary := map[string]any{
				"task_id":      id,
				"status":       firstString(task, "status"),
				"target":       firstString(progress, "target"),
				"tool":         firstString(progress, "tool", "method"),
				"created_at":   row["created_at"],
				"get":          fmt.Sprintf("mcp.tasks.get(%q)", id),
				"receipt_path": "/admin/api/tasks/" + id,
			}
			if s := firstString(row, "trace_id"); s != "" {
				summary["trace_id"] = s
			}
			if parent := firstString(task, "parentTaskId"); parent != "" {
				summary["parent_task_id"] = parent
			}
			items = append(items, summary)
			if len(items) >= limit {
				break
			}
		}
		scanned += consumed
		if consumed < len(rows) {
			// A full authorized page may stop mid-query; never skip unread
			// records when the next invocation uses this cursor.
			break
		}
		if len(rows) < count {
			exhausted = true
			break
		}
	}
	out := map[string]any{"tasks": items, "complete": exhausted, "replay": false}
	if !exhausted {
		encoded, err := encodeTaskListCursor(*cursor)
		if err != nil {
			return nil, fmt.Errorf("task cursor unavailable: %w", err)
		}
		out["next_cursor"] = encoded
		out["complete"] = false
	}
	return out, nil
}
