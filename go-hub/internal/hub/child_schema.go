package hub

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

const childSchemaMethod = "child/tools/list"

// A child is a descriptor, not a polling relay. Keep the public receipt and
// the parent's executable task under ONE ID, committed before queue release.
func (s *Server) dispatchChildSchemaLocked(r *http.Request, child Agent, receipt *relayJob) error {
	current, exists := s.exposedAgentByIDLocked(child.AgentID)
	if !exists || !isChildMCPAgent(current) || !profileAllowsTarget(r, child.AgentID) || !agentOwnerConnectionAllowed(r, current) {
		return errors.New("child schema is not an authorized active target")
	}
	child = current
	parent := firstString(child.Meta, "parent_server_id")
	ref := firstString(child.Meta, "child_ref")
	a := s.agents[parent]
	if a == nil || !truthy(a.Meta["approved"]) {
		return errors.New("child schema parent is not an approved executor")
	}
	owner, err := s.ensureTaskOwnerLocked()
	if err != nil {
		return err
	}
	change := s.beginTaskMutationLocked(receipt.ID)
	copy := *receipt
	copy.Method, copy.OwnerID, copy.Status = childSchemaMethod, owner, "queued"
	copy.StartedAt, copy.DoneAt, copy.Result, copy.Error = 0, 0, nil, nil
	copy.Params = map[string]any{"parent_server_id": parent, "child_ref": ref}
	s.relayJobs[copy.ID] = &copy
	s.shellJobs[copy.ID] = &shellJob{
		ID: copy.ID, OwnerID: owner, Server: strings.TrimPrefix(parent, "shell:"),
		ToolName: "mcp_tools", Arguments: childConnectionArguments(r, child, map[string]any{"ref": ref}),
		CreatedAt: copy.CreatedAt, Status: "queued", TraceID: copy.TraceID, TraceParent: copy.TraceParent,
	}
	if err := change.commit(); err != nil {
		return err
	}
	queue := s.relayQueues[child.AgentID]
	kept := make([]string, 0, len(queue))
	for _, id := range queue {
		if id != copy.ID {
			kept = append(kept, id)
		}
	}
	s.relayQueues[child.AgentID] = kept
	// Publishing after commit also avoids rolling back a previously absent queue.
	s.shellQueues[strings.TrimPrefix(parent, "shell:")] = append(s.shellQueues[strings.TrimPrefix(parent, "shell:")], copy.ID)
	s.cond.Broadcast()
	return nil
}

func undeliveredChildSchema(j *relayJob) bool {
	if j == nil || (j.Method != "tools/list" && j.Method != childSchemaMethod) || j.StartedAt != 0 || j.Result != nil {
		return false
	}
	if j.Status == "queued" && j.DoneAt == 0 && j.Error == nil {
		return true
	}
	// These two durable failures explicitly establish that dispatch never ran.
	code := firstString(mapValue(j.Error), "code")
	return j.Status == "failed" && (code == "hub_restarted_before_dispatch" || code == "task_owner_exited_before_dispatch")
}

func (s *Server) childSchemaReceiptLocked(r *http.Request, id string) (map[string]any, bool) {
	receipt := s.relayJobs[id]
	if receipt == nil || (receipt.Method != "tools/list" && receipt.Method != childSchemaMethod) {
		return nil, false
	}
	child, exists := s.exposedAgentByIDLocked(receipt.AgentID)
	if !exists || !isChildMCPAgent(child) {
		return nil, false
	}
	parent := firstString(child.Meta, "parent_server_id")
	response := map[string]any{"job_id": id, "server_id": child.AgentID, "owner_target": parent}
	failure := func(message string) (map[string]any, bool) {
		response["status"], response["error"] = "failed", map[string]any{"code": "child_schema_dispatch_failed", "message": message}
		return response, true
	}
	if !profileAllowsTarget(r, child.AgentID) || !agentOwnerConnectionAllowed(r, child) {
		return failure("access profile denies the child schema receipt")
	}
	registration := s.agents[parent]
	if registration == nil || !truthy(registration.Meta["approved"]) {
		return failure("child schema parent is not an approved executor")
	}
	job := s.shellJobs[id]
	jobUndelivered := job != nil && job.StartedAt == 0 && job.Status == "failed" &&
		(firstString(mapValue(job.Error), "code") == "hub_restarted_before_dispatch" || firstString(mapValue(job.Error), "code") == "task_owner_exited_before_dispatch")
	if undeliveredChildSchema(receipt) && (job == nil || (receipt.Method == childSchemaMethod && jobUndelivered)) {
		// Only schema reads can be recovered. Arguments are rebuilt from the
		// currently authorized descriptor, never from a persisted business call.
		if err := s.dispatchChildSchemaLocked(r, child, receipt); err != nil {
			return taskPersistenceFailure(err), true
		}
		receipt, job = s.relayJobs[id], s.shellJobs[id]
	}
	if receipt.Method != childSchemaMethod {
		return failure("child schema was already dispatched or has a conflicting task")
	}
	if firstString(receipt.Params, "parent_server_id") != parent || firstString(receipt.Params, "child_ref") != firstString(child.Meta, "child_ref") || job == nil || "shell:"+job.Server != parent || job.ToolName != "mcp_tools" {
		return failure("child schema owner or descriptor changed")
	}
	response["status"] = job.Status
	if receipt.Status != job.Status || receipt.StartedAt != job.StartedAt || receipt.DoneAt != job.DoneAt {
		change := s.beginTaskMutationLocked(id)
		receipt.Status, receipt.StartedAt, receipt.DoneAt, receipt.Error = job.Status, job.StartedAt, job.DoneAt, job.Error
		if err := change.commit(); err != nil {
			return taskPersistenceFailure(err), true
		}
	}
	if job.Status != "completed" && job.Status != "failed" && job.Status != "cancelled" {
		return response, true
	}
	if job.Status != "completed" || job.Error != nil {
		response["error"] = job.Error
		return response, true
	}
	upstream := childMCPResponse(shellJobResponse(job))
	_, list := upstream["tools"].([]any)
	_, typedList := upstream["tools"].([]map[string]any)
	if (!list && !typedList) || truthy(upstream["isError"]) || upstream["error"] != nil {
		return failure("upstream did not return a successful tools/list response")
	}
	response["response"] = map[string]any{"tools": toolsForRequest(r, child.AgentID, childMCPTools(shellJobResponse(job)))}
	return response, true
}

func (s *Server) childSchemaJobForRequest(r *http.Request, id string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshTaskRecordsLocked(id); err != nil {
		return taskPersistenceFailure(err), true
	}
	return s.childSchemaReceiptLocked(r, id)
}

func (s *Server) childSchemaForRequest(r *http.Request, child Agent) map[string]any {
	if !agentOwnerConnectionAllowed(r, child) {
		return map[string]any{"server_id": child.AgentID, "status": "failed", "error": connectionOwnerRPCError()}
	}
	s.mu.Lock()
	id := newID()
	receipt := &relayJob{ID: id, AgentID: child.AgentID, CreatedAt: nowFloat(), TraceID: requestTraceID(r), TraceParent: requestTraceParent(r)}
	if err := s.dispatchChildSchemaLocked(r, child, receipt); err != nil {
		s.mu.Unlock()
		return taskPersistenceFailure(err)
	}
	s.mu.Unlock()
	// A schema facade has a bounded synchronous window, then returns the same
	// durable receipt. Reading it neither resubmits nor allocates another job.
	deadline := time.Now().Add(minDuration(s.cfg.DefaultTimeout, 3*time.Second))
	for {
		response, _ := s.childSchemaJobForRequest(r, id)
		status := firstString(response, "status")
		if status == "completed" || status == "failed" || status == "cancelled" {
			return response
		}
		if time.Now().After(deadline) {
			response["background"] = true
			return response
		}
		if r != nil && r.Context().Err() != nil {
			return response
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A child-only profile may read its own receipt via the parent's owner hint;
// the hint grants no access to the parent's unrelated shell tasks.
func (s *Server) childSchemaHintAllowed(r *http.Request, id, hint string) (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshTaskRecordsLocked(id); err != nil {
		return true, false
	}
	j := s.relayJobs[id]
	if j == nil || (j.Method != "tools/list" && j.Method != childSchemaMethod) {
		return false, false
	}
	child, exists := s.exposedAgentByIDLocked(j.AgentID)
	if !exists || !isChildMCPAgent(child) {
		return false, false
	}
	parent := firstString(child.Meta, "parent_server_id")
	a := s.agents[parent]
	return true, (hint == child.AgentID || hint == parent) && a != nil && truthy(a.Meta["approved"]) && profileAllowsTarget(r, child.AgentID)
}
