package hub

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
)

const taskStateFilename = "tasks_state.json"

type persistedTaskState struct {
	SavedAt     float64                              `json:"saved_at"`
	Relay       map[string]persistedRelayTask        `json:"relay_jobs,omitempty"`
	Shell       map[string]persistedShellTask        `json:"shell_jobs,omitempty"`
	Idempotency map[string]persistedIdempotencyEntry `json:"idempotency,omitempty"`
}

type persistedIdempotencyEntry struct {
	Fingerprint string         `json:"fingerprint"`
	CreatedAt   time.Time      `json:"created_at"`
	JobID       string         `json:"job_id"`
	Response    map[string]any `json:"response"`
	Status      int            `json:"status"`
}

type persistedRelayTask struct {
	RequestKey   string         `json:"request_key,omitempty"`
	Revision     int64          `json:"-"`
	OwnerID      string         `json:"owner_id,omitempty"`
	ID           string         `json:"id"`
	AgentID      string         `json:"agent_id,omitempty"`
	TraceID      string         `json:"trace_id,omitempty"`
	TraceParent  string         `json:"traceparent,omitempty"`
	Method       string         `json:"method,omitempty"`
	CreatedAt    float64        `json:"created_at"`
	StartedAt    float64        `json:"started_at,omitempty"`
	DoneAt       float64        `json:"completed_at,omitempty"`
	Status       string         `json:"status"`
	Result       map[string]any `json:"result,omitempty"`
	Error        any            `json:"error,omitempty"`
	Params       map[string]any `json:"params,omitempty"`
	ParentTaskID string         `json:"parent_task_id,omitempty"`
}

type persistedShellTask struct {
	Timeout      int     `json:"timeout,omitempty"`
	RequestKey   string  `json:"request_key,omitempty"`
	Revision     int64   `json:"-"`
	OwnerID      string  `json:"owner_id,omitempty"`
	ID           string  `json:"id"`
	Server       string  `json:"server,omitempty"`
	TraceID      string  `json:"trace_id,omitempty"`
	TraceParent  string  `json:"traceparent,omitempty"`
	ToolName     string  `json:"tool_name,omitempty"`
	CreatedAt    float64 `json:"created_at"`
	StartedAt    float64 `json:"started_at,omitempty"`
	DoneAt       float64 `json:"completed_at,omitempty"`
	Status       string  `json:"status"`
	Result       any     `json:"result,omitempty"`
	Error        any     `json:"error,omitempty"`
	ApprovalID   string  `json:"approval_id,omitempty"`
	ParentTaskID string  `json:"parent_task_id,omitempty"`
}

func (s *Server) taskStatePath() string {
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, taskStateFilename)
}

// saveTaskStateLocked commits only the selected tasks and their idempotency
// records. No IDs means an explicit full reconciliation (tests/imports only).
// A group includes its children so parent links/cancellation commit atomically.
func (s *Server) saveTaskStateLocked(taskIDs ...string) error {
	if s.taskRuntimeClosed {
		return errors.New("task runtime is closed")
	}
	path := s.taskStatePath()
	if path == "" {
		return nil
	}
	state := persistedTaskState{
		SavedAt:     nowFloat(),
		Relay:       make(map[string]persistedRelayTask),
		Shell:       make(map[string]persistedShellTask),
		Idempotency: make(map[string]persistedIdempotencyEntry),
	}
	cutoff := state.SavedAt - float64(s.hubSettingIntLocked("completed_job_retention_hours")*3600)
	selected := map[string]bool{}
	pending := append([]string(nil), taskIDs...)
	for len(pending) > 0 {
		id := pending[0]
		pending = pending[1:]
		if selected[id] {
			continue
		}
		selected[id] = true
		if group := s.relayJobs[id]; group != nil && group.Method == "task/group" {
			pending = append(pending, taskGroupChildIDs(group)...)
		}
	}
	relay := s.relayJobs
	shell := s.shellJobs
	if len(taskIDs) > 0 {
		relay = make(map[string]*relayJob, len(selected))
		shell = make(map[string]*shellJob, len(selected))
		for id := range selected {
			relay[id] = s.relayJobs[id]
			shell[id] = s.shellJobs[id]
		}
	}
	for id, j := range relay {
		if j == nil {
			continue
		}
		if j.DoneAt > 0 && j.DoneAt < cutoff {
			continue
		}
		state.Relay[id] = persistedRelayTask{ID: j.ID, RequestKey: j.RequestKey, Revision: j.Revision, OwnerID: j.OwnerID, AgentID: j.AgentID, TraceID: j.TraceID, TraceParent: j.TraceParent, Method: j.Method, Params: persistableTaskParams(j.Params), CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, DoneAt: j.DoneAt, Status: j.Status, Result: j.Result, Error: j.Error, ParentTaskID: j.ParentTaskID}
	}
	for id, j := range shell {
		if j == nil {
			continue
		}
		if j.DoneAt > 0 && j.DoneAt < cutoff {
			continue
		}
		state.Shell[id] = persistedShellTask{Timeout: j.Timeout, ID: j.ID, RequestKey: j.RequestKey, Revision: j.Revision, OwnerID: j.OwnerID, Server: j.Server, TraceID: j.TraceID, TraceParent: j.TraceParent, ToolName: j.ToolName, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, DoneAt: j.DoneAt, Status: j.Status, Result: persistableShellResult(j), Error: redactSecretValues(j.Error, j.SecretValues), ApprovalID: j.ApprovalID, ParentTaskID: j.ParentTaskID}
	}
	nowTime := time.Now()
	for key, entry := range s.idempotency {
		if entry == nil || entry.JobID == "" || entry.Response == nil || entry.Status == 0 || nowTime.Sub(entry.CreatedAt) > idempotencyTTL || (len(taskIDs) > 0 && !selected[entry.JobID]) {
			continue
		}
		select {
		case <-entry.Done:
			state.Idempotency[key] = persistedIdempotencyEntry{Fingerprint: entry.Fingerprint, CreatedAt: entry.CreatedAt, JobID: entry.JobID, Response: cloneMap(entry.Response), Status: entry.Status}
		default:
		}
	}

	var revisions map[string]int64
	err := s.withTaskDatabase(func(tx *taskTransaction) error {
		if err := tx.save(state); err != nil {
			return err
		}
		revisions = tx.revisions
		return tx.prune(cutoff, nowFloat()-idempotencyTTL.Seconds())
	})
	if err != nil {
		return err
	}
	for key, revision := range revisions {
		if strings.HasPrefix(key, "shell:") {
			if j := s.shellJobs[strings.TrimPrefix(key, "shell:")]; j != nil {
				j.Revision = revision
			}
		}
		if strings.HasPrefix(key, "relay:") {
			if j := s.relayJobs[strings.TrimPrefix(key, "relay:")]; j != nil {
				j.Revision = revision
			}
		}
	}
	return nil
}

func persistableTaskParams(params map[string]any) map[string]any {
	if len(params) == 0 {
		return nil
	}
	if compact, ok := compactPersistedOutput(params).(map[string]any); ok {
		return compact
	}
	return nil
}

func persistableShellResult(j *shellJob) any {
	return compactPersistedOutput(redactSecretValues(j.Result, j.SecretValues))
}

func compactPersistedOutput(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		spilled := truthy(v["_spilled"])
		for key, item := range v {
			if spilled && (key == "stdout" || key == "stderr") {
				if text, ok := item.(string); ok && len(text) > 8192 {
					out[key] = text[len(text)-8192:]
					continue
				}
			}
			out[key] = compactPersistedOutput(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = compactPersistedOutput(item)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(v))
		for i, item := range v {
			if compact, ok := compactPersistedOutput(item).(map[string]any); ok {
				out[i] = compact
			}
		}
		return out
	case string:
		const maxPersistedString = 64 * 1024
		if len(v) > maxPersistedString {
			return "<persisted-output-truncated>\n" + v[len(v)-maxPersistedString:]
		}
		return v
	default:
		return value
	}
}

func (s *Server) loadTaskState() error {
	state, err := s.readTaskState()
	if err != nil {
		return err
	}

	aliveOwners := map[string]bool{}
	for _, p := range state.Shell {
		if p.OwnerID != "" {
			aliveOwners[p.OwnerID] = false
		}
	}
	for _, p := range state.Relay {
		if p.OwnerID != "" {
			aliveOwners[p.OwnerID] = false
		}
	}
	for owner := range aliveOwners {
		alive, err := s.taskOwnerAlive(owner)
		if err != nil {
			return err
		}
		aliveOwners[owner] = alive
	}
	now := nowFloat()
	var recoveredIDs []string
	for id, p := range state.Relay {
		alive := aliveOwners[p.OwnerID]
		status := p.Status
		errVal := p.Error
		done := p.DoneAt
		if status == "queued" && !alive {
			status = "failed"
			errVal = map[string]any{"code": "hub_restarted_before_dispatch", "message": "Hub restarted before queued relay task was dispatched; task was not replayed"}
			done = now
			recoveredIDs = append(recoveredIDs, id)
		}
		s.relayJobs[id] = &relayJob{ID: p.ID, RequestKey: p.RequestKey, Revision: p.Revision, OwnerID: p.OwnerID, AgentID: p.AgentID, TraceID: p.TraceID, TraceParent: p.TraceParent, Method: p.Method, Params: p.Params, CreatedAt: p.CreatedAt, StartedAt: p.StartedAt, DoneAt: done, Status: status, Result: p.Result, Error: errVal, ParentTaskID: p.ParentTaskID}
	}
	for id, p := range state.Shell {
		alive := aliveOwners[p.OwnerID]
		status := p.Status
		errVal := p.Error
		done := p.DoneAt
		if status == "queued" && !alive {
			status = "failed"
			errVal = map[string]any{"code": "hub_restarted_before_dispatch", "message": "Hub restarted before queued shell task was dispatched; task was not replayed"}
			done = now
			recoveredIDs = append(recoveredIDs, id)
		} else if status == "input_required" && !alive {
			status = "failed"
			errVal = map[string]any{"code": "hub_restarted_during_input_required", "message": "Hub restarted while task awaited approval; original arguments were intentionally not persisted and task was not replayed"}
			done = now
			recoveredIDs = append(recoveredIDs, id)
		}
		s.shellJobs[id] = &shellJob{Timeout: p.Timeout, ID: p.ID, RequestKey: p.RequestKey, Revision: p.Revision, OwnerID: p.OwnerID, Server: p.Server, TraceID: p.TraceID, TraceParent: p.TraceParent, ToolName: p.ToolName, CreatedAt: p.CreatedAt, StartedAt: p.StartedAt, DoneAt: done, Status: status, Result: p.Result, Error: errVal, ApprovalID: p.ApprovalID, ParentTaskID: p.ParentTaskID}
		if status == "cancelled" && p.StartedAt > 0 && p.Server != "" {
			s.shellControls[p.Server] = append(s.shellControls[p.Server], shellControl{TaskID: p.ID})
		}
	}
	for key, p := range state.Idempotency {
		if p.JobID == "" || p.Response == nil || time.Since(p.CreatedAt) > idempotencyTTL {
			continue
		}
		done := make(chan struct{})
		close(done)
		s.idempotency[key] = &idempotencyEntry{Fingerprint: p.Fingerprint, CreatedAt: p.CreatedAt, Done: done, JobID: p.JobID, Response: cloneMap(p.Response), Status: p.Status}
	}
	if len(recoveredIDs) > 0 {
		return s.saveTaskStateLocked(recoveredIDs...)
	}
	return nil
}
