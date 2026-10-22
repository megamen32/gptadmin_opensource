package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Task lifecycle transitions share the durable mutation/reconciliation boundary.

func (s *Server) expireOrphanedShellJobsLocked() []string {
	now := nowFloat()
	minAge := s.hubSettingIntLocked("orphan_shell_job_min_age_seconds")
	if minAge < 300 {
		minAge = 300
	}
	expired := []string{}
	for _, job := range s.shellJobs {
		if job == nil || job.Status != "running" || job.StartedAt <= 0 || s.taskOwnedElsewhereLocked(job.OwnerID) {
			continue
		}
		requestedTimeout := job.Timeout
		if requestedTimeout <= 0 {
			requestedTimeout = 300
		}
		maxAge := requestedTimeout + 300
		if maxAge < minAge {
			maxAge = minAge
		}
		if now-job.StartedAt <= float64(maxAge) {
			continue
		}
		job.Status = "failed"
		job.DoneAt = now
		job.Error = map[string]any{
			"code":    "orphaned_running_task",
			"message": fmt.Sprintf("shell task remained running for %.0fs, exceeding orphan threshold %ds", now-job.StartedAt, maxAge),
		}
		// A receipt that shares the task id follows the expired job into its
		// terminal state.
		s.finishTaskRecordsLocked(job.ID, "failed", job.Error)
		if job.Server != "" {
			s.shellControls[job.Server] = append(s.shellControls[job.Server], shellControl{TaskID: job.ID})
		}
		expired = append(expired, job.ID)
	}
	return expired
}

// reconcileSharedTaskRecordsLocked finalizes receipts that can no longer
// finish. A receipt shares the id of the dispatch job that carries the call.
// When that job is already terminal, the receipt follows it. When the job is
// gone and the receipt has waited past the orphan threshold, the receipt is
// failed: a receipt that never finishes keeps the node from draining, and a
// node that cannot drain cannot be updated.
func (s *Server) reconcileSharedTaskRecordsLocked() []string {
	now := nowFloat()
	minAge := s.hubSettingIntLocked("orphan_shell_job_min_age_seconds")
	if minAge < 300 {
		minAge = 300
	}
	repaired := []string{}
	for id, receipt := range s.relayJobs {
		if receipt == nil || taskTerminal(receipt.Status) {
			continue
		}
		if job := s.shellJobs[id]; job != nil {
			if !taskTerminal(job.Status) {
				continue
			}
			s.finishTaskRecordsLocked(id, job.Status, job.Error)
			repaired = append(repaired, id)
			continue
		}
		started := receipt.StartedAt
		if started <= 0 {
			started = receipt.CreatedAt
		}
		if started <= 0 || now-started <= float64(minAge) {
			continue
		}
		s.finishTaskRecordsLocked(id, "failed", map[string]any{
			"code":    "orphaned_task_receipt",
			"message": "the dispatch job of this receipt is gone; the receipt was failed instead of waiting forever",
		})
		repaired = append(repaired, id)
	}
	return repaired
}

func (s *Server) refreshTaskGroupsLocked() int {
	changed := 0
	for _, group := range s.relayJobs {
		if group == nil || group.Method != "task/group" || taskTerminal(group.Status) || s.taskOwnedElsewhereLocked(group.OwnerID) {
			continue
		}
		before := fmt.Sprintf("%s|%.6f|%v", group.Status, group.DoneAt, group.Result)
		s.refreshTaskGroupLocked(group)
		after := fmt.Sprintf("%s|%.6f|%v", group.Status, group.DoneAt, group.Result)
		if before != after {
			changed++
		}
	}
	return changed
}

func taskGroupChildIDs(j *relayJob) []string {
	if j == nil || j.Method != "task/group" {
		return nil
	}
	items, _ := j.Params["children"].([]any)
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id, ok := item.(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *Server) taskStatusLocked(taskID string) string {
	if j := s.shellJobs[taskID]; j != nil {
		return mcpTaskStatus(j.Status)
	}
	if j := s.relayJobs[taskID]; j != nil {
		return mcpTaskStatus(j.Status)
	}
	return "failed"
}

func (s *Server) refreshTaskGroupLocked(group *relayJob) {
	if group == nil || group.Method != "task/group" {
		return
	}
	ids := taskGroupChildIDs(group)
	if len(ids) == 0 {
		return
	}
	counts := map[string]int{"working": 0, "input_required": 0, "completed": 0, "failed": 0, "cancelled": 0}
	for _, id := range ids {
		counts[s.taskStatusLocked(id)]++
	}
	countPayload := map[string]any{}
	for key, value := range counts {
		countPayload[key] = value
	}
	group.Result = map[string]any{"children": append([]string(nil), ids...), "counts": countPayload}
	switch {
	case counts["input_required"] > 0:
		group.Status = "input_required"
	case counts["working"] > 0:
		group.Status = "running"
		if group.StartedAt <= 0 {
			group.StartedAt = nowFloat()
		}
	case counts["failed"] > 0:
		group.Status = "failed"
		if group.DoneAt <= 0 {
			group.DoneAt = nowFloat()
		}
	case counts["cancelled"] > 0 && counts["completed"]+counts["cancelled"] == len(ids):
		group.Status = "cancelled"
		if group.DoneAt <= 0 {
			group.DoneAt = nowFloat()
		}
	default:
		group.Status = "completed"
		if group.DoneAt <= 0 {
			group.DoneAt = nowFloat()
		}
	}
}

// finishTaskRecordsLocked brings every record that shares one task id to a
// terminal state. A child schema receipt and the shell job that carries the
// actual call share the id, so finishing only one of them leaves the node
// with permanently unfinished work: the receipt keeps the drain from ever
// becoming ready, the task can never be read as finished, and the node cannot
// be updated until somebody cleans it up by hand.
func (s *Server) finishTaskRecordsLocked(taskID string, status string, taskErr any) {
	now := nowFloat()
	if j := s.shellJobs[taskID]; j != nil && !taskTerminal(j.Status) {
		j.Status = status
		j.DoneAt = now
		if taskErr != nil {
			j.Error = taskErr
		}
	}
	if j := s.relayJobs[taskID]; j != nil && !taskTerminal(j.Status) {
		j.Status = status
		j.DoneAt = now
		if taskErr != nil {
			j.Error = taskErr
		}
	}
}

func (s *Server) cancelTaskLocked(taskID string) {
	if j := s.shellJobs[taskID]; j != nil && !taskTerminal(j.Status) {
		wasRunning := j.Status == "running"
		j.Status = "cancelled"
		j.DoneAt = nowFloat()
		if wasRunning {
			s.shellControls[j.Server] = append(s.shellControls[j.Server], shellControl{TaskID: j.ID})
		}
	}
	// The receipt and the dispatch job share the task id. Both have to reach a
	// terminal state, otherwise the receipt stays unfinished forever.
	if j := s.relayJobs[taskID]; j != nil && !taskTerminal(j.Status) {
		j.Status = "cancelled"
		j.DoneAt = nowFloat()
		j.Error = map[string]any{"code": "task_cancelled", "message": "the task was cancelled"}
	}
}

func (s *Server) mcpTaskCancel(taskID, owner string) (any, any) {
	if taskID == "" {
		return nil, map[string]any{"code": -32602, "message": "taskId is required"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshTaskRecordsLocked(taskID); err != nil {
		return nil, taskPersistenceRPCError(err)
	}
	if j := s.shellJobs[taskID]; j != nil {
		if owner != "" && owner != "shell:"+j.Server && owner != "hub" {
			return nil, map[string]any{"code": -32602, "message": "invalid taskId for this MCP server"}
		}
	} else if j := s.relayJobs[taskID]; j != nil {
		if owner != "" && owner != j.AgentID && owner != "hub" {
			return nil, map[string]any{"code": -32602, "message": "invalid taskId for this MCP server"}
		}
	} else {
		return nil, map[string]any{"code": -32602, "message": "unknown taskId"}
	}
	change := s.beginTaskMutationLocked(taskID)
	if j := s.relayJobs[taskID]; j != nil && j.Method == "task/group" {
		for _, id := range taskGroupChildIDs(j) {
			s.cancelTaskLocked(id)
		}
		s.refreshTaskGroupLocked(j)
	} else {
		s.cancelTaskLocked(taskID)
	}
	if err := change.commit(); err != nil {
		return nil, taskPersistenceRPCError(err)
	}
	s.cond.Broadcast()
	return map[string]any{"resultType": "complete"}, nil
}

func (s *Server) mcpTaskUpdate(r *http.Request, taskID string, inputResponses map[string]any, owner string) (any, any) {
	decision := firstString(mapValue(inputResponses["approval"]), "action")
	if decision == "accept" || firstString(inputResponses, "approvalId", "approval_id") != "" {
		if failure := s.requireInstructionContext(r, "hub", "tasks/update"); failure != nil {
			return nil, map[string]any{"code": -32006, "message": "current instructions are required to resume this task", "data": failure}
		}
	}

	if taskID == "" {
		return nil, map[string]any{"code": -32602, "message": "taskId is required"}
	}
	s.mu.Lock()
	if err := s.refreshTaskRecordsLocked(taskID); err != nil {
		s.mu.Unlock()
		return nil, taskCommitRPCError(err)
	}
	if group := s.relayJobs[taskID]; group != nil && group.Method == "task/group" {
		if owner != "" && owner != "hub" {
			s.mu.Unlock()
			return nil, map[string]any{"code": -32602, "message": "invalid taskId for this MCP server"}
		}
		s.refreshTaskGroupLocked(group)
		if group.Status != "input_required" {
			out := s.detailedTaskGroup(group)
			s.mu.Unlock()
			return out, nil
		}
		decision, decisionErr := approvalInputDecision(inputResponses)
		if decisionErr != nil {
			s.mu.Unlock()
			return nil, map[string]any{"code": -32602, "message": decisionErr.Error()}
		}
		if decision == "decline" {
			change := s.beginTaskMutationLocked(group.ID)
			for _, childID := range taskGroupChildIDs(group) {
				s.cancelTaskLocked(childID)
			}
			s.refreshTaskGroupLocked(group)
			if err := change.commit(); err != nil {
				s.mu.Unlock()
				return nil, taskCommitRPCError(err)
			}
			out := s.detailedTaskGroup(group)
			s.mu.Unlock()
			s.cond.Broadcast()
			return out, nil
		}
		children := []string{}
		for _, childID := range taskGroupChildIDs(group) {
			if child := s.shellJobs[childID]; child != nil && child.Status == "input_required" {
				if err := s.shellApprovalReadyLocked(child); err != nil {
					s.mu.Unlock()
					return nil, map[string]any{"code": -32004, "message": err.Error()}
				}
				children = append(children, childID)
			}
		}
		s.mu.Unlock()
		for _, childID := range children {
			if _, childErr := s.mcpTaskUpdate(r, childID, inputResponses, "hub"); childErr != nil {
				return nil, childErr
			}
		}
		s.mu.Lock()
		group = s.relayJobs[taskID]
		if group == nil {
			s.mu.Unlock()
			return nil, map[string]any{"code": -32602, "message": "unknown taskId"}
		}
		change := s.beginTaskMutationLocked(group.ID)
		s.refreshTaskGroupLocked(group)
		if err := change.commit(); err != nil {
			s.mu.Unlock()
			return nil, taskCommitRPCError(err)
		}
		out := s.detailedTaskGroup(group)
		s.mu.Unlock()
		s.cond.Broadcast()
		return out, nil
	}
	job := s.shellJobs[taskID]
	if job == nil {
		s.mu.Unlock()
		if _, err := s.mcpTaskGet(taskID, owner); err != nil {
			return nil, err
		}
		return map[string]any{"resultType": "complete"}, nil
	}
	if owner != "" && owner != "shell:"+job.Server && owner != "hub" {
		s.mu.Unlock()
		return nil, map[string]any{"code": -32602, "message": "invalid taskId for this MCP server"}
	}
	if job.Status != "input_required" {
		s.mu.Unlock()
		return map[string]any{"resultType": "complete"}, nil
	}
	approvalID := job.ApprovalID
	approvalResponse := mapValue(inputResponses["approval"])
	if len(approvalResponse) == 0 {
		// Temporary compatibility with the first GPTAdmin v2 task draft.
		legacyID := firstString(inputResponses, "approvalId", "approval_id")
		if legacyID == "" {
			s.mu.Unlock()
			return map[string]any{"resultType": "complete"}, nil
		}
		if legacyID != approvalID {
			s.mu.Unlock()
			return nil, map[string]any{"code": -32602, "message": "approval response does not match this task"}
		}
	} else {
		action := firstString(approvalResponse, "action")
		if action != "accept" {
			if action == "decline" || action == "cancel" {
				change := s.beginTaskMutationLocked(job.ID)
				job.Status = "cancelled"
				job.DoneAt = nowFloat()
				if err := change.commit(); err != nil {
					s.mu.Unlock()
					return nil, taskCommitRPCError(err)
				}
				s.mu.Unlock()
				return map[string]any{"resultType": "complete"}, nil
			}
			s.mu.Unlock()
			return nil, map[string]any{"code": -32602, "message": "approval input response action must be accept or decline"}
		}
		content := mapValue(approvalResponse["content"])
		if !truthy(content["approved"]) {
			s.mu.Unlock()
			return nil, map[string]any{"code": -32602, "message": "approval response must contain content.approved=true"}
		}
	}
	originalArgs := cloneMap(job.Arguments)
	server := job.Server
	toolName := job.ToolName
	expectedApprovalID := job.ApprovalID
	expectedActor := job.ApprovalActor
	expectedProfileID := job.ApprovalProfileID
	s.mu.Unlock()

	resolvedArgs := originalArgs
	secretValues := []string(nil)
	if toolName == "shell_exec" {
		var err error
		resolvedArgs, secretValues, err = s.resolveSecretEnvForRequest(r, "shell:"+server, originalArgs)
		if err != nil {
			return nil, map[string]any{"code": -32003, "message": err.Error()}
		}
	}
	digestBytes, err := json.Marshal(originalArgs)
	if err != nil {
		return nil, map[string]any{"code": -32602, "message": "task arguments cannot be serialized"}
	}
	digest := sha256Hex(digestBytes)

	s.mu.Lock()
	defer s.mu.Unlock()
	job = s.shellJobs[taskID]
	if job == nil || job.Status != "input_required" || job.ApprovalID != expectedApprovalID {
		return nil, map[string]any{"code": -32004, "message": "task is no longer awaiting this approval"}
	}
	approval := s.approvals[approvalID]
	now := s.now()
	if approval == nil || approval.Status != "approved" || !now.Before(approval.ExpiresAt) || approval.Target != "shell:"+server || approval.Tool != toolName || approval.ArgumentsDigest != digest || approval.ProfileID != expectedProfileID || approval.Actor != expectedActor {
		return nil, map[string]any{"code": -32004, "message": "approval is not approved, expired, or does not match this task"}
	}
	change := s.beginTaskMutationLocked(job.ID)
	approval.Status = "consumed"
	job.ApprovalID = ""
	job.Arguments = cloneMap(resolvedArgs)
	job.SecretValues = append([]string(nil), secretValues...)
	if toolName == "shell_exec" {
		job.Cmd = firstString(resolvedArgs, "cmd", "command")
		job.Cwd = firstString(resolvedArgs, "cwd")
		job.Timeout = intFromAny(resolvedArgs["timeout"])
		job.Env = mapValue(resolvedArgs["env"])
	}
	job.Status = "queued"
	if err := change.commit(); err != nil {
		return nil, taskCommitRPCError(err)
	}
	s.shellQueues[job.Server] = append(s.shellQueues[job.Server], job.ID)
	s.cond.Broadcast()
	return map[string]any{"resultType": "complete"}, nil
}
