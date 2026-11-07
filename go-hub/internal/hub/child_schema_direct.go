package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"time"
)

// DirectChildTools is implemented by the embedded ShellMCP executor. A schema
// read on a real Node never needs a durable task ID.
type directChildToolReader interface {
	DirectChildTools(context.Context, map[string]any) (map[string]any, error)
}

const directChildSchemaTimeout = 8 * time.Second

// directChildSchemaForRequest reads one authorized child schema directly from
// its owning Node (or the embedded local executor). Only an online legacy
// long-poll owner without a Node route can use the existing same-ID schema
// transport. Pending legacy receipts are reused, never replayed on timeout.
func (s *Server) directChildSchemaForRequest(r *http.Request, candidate Agent) map[string]any {
	target := candidate.AgentID
	result := map[string]any{"server_id": target, "status": "failed"}
	fail := func(reason string) map[string]any {
		result["error"] = map[string]any{"code": "child_schema_unavailable", "message": reason}
		return result
	}
	if r == nil || r.Context().Err() != nil {
		return fail("schema request unavailable")
	}
	if !profileAllowsTarget(r, target) || !agentOwnerConnectionAllowed(r, candidate) {
		return fail("child schema access denied")
	}

	s.mu.Lock()
	child, exists := s.exposedAgentByIDLocked(target)
	parentID := firstString(child.Meta, "parent_server_id")
	ref := firstString(child.Meta, "child_ref")
	parent := s.agents[parentID]
	approved := parent != nil && truthy(parent.Meta["approved"])
	local := s.localExecutors[parentID] != nil
	runtime := s.nodeRuntime
	s.mu.Unlock()
	if !exists || !isChildMCPAgent(child) || !approved || !strings.HasPrefix(parentID, "shell:") || ref == "" {
		return fail("child schema owner unavailable")
	}
	if !profileAllowsTarget(r, child.AgentID) || !agentOwnerConnectionAllowed(r, child) {
		return fail("child schema access denied")
	}

	ctx, cancel := context.WithTimeout(r.Context(), directChildSchemaTimeout)
	defer cancel()
	var tools []map[string]any
	if local {
		reader, ok := runtime.(directChildToolReader)
		if !ok {
			return fail("local child schema reader unavailable")
		}
		payload, err := reader.DirectChildTools(ctx, childConnectionArguments(r, child, map[string]any{"ref": ref}))
		if err != nil {
			return fail("local child schema reader failed")
		}
		structured := mapValue(payload["structuredContent"])
		if !validChildToolsArray(structured["tools"]) || truthy(payload["isError"]) {
			return fail("local child schema response invalid")
		}
		tools = searchChildToolMaps(structured["tools"])
	} else {
		// Always address the physical owner. The public logical Browser ID is
		// not an executable target on node-44.
		body, err := json.Marshal(map[string]any{"target": "mcp:" + parentID + ":" + ref})
		if err != nil {
			return fail("child schema target invalid")
		}
		request := r.Clone(ctx)
		recorder := httptest.NewRecorder()
		if !s.forwardNodePeerRequest(recorder, request, parentID, body, "/mcp-relay/tools") {
			if response, eligible := s.legacyChildSchemaForRequest(request, child); eligible {
				if isLogicalBrowserAgent(child) && firstString(response, "status") == "completed" {
					body := mapValue(response["response"])
					body["tools"] = hideBrowserRetargetTool(searchChildToolMaps(body["tools"]))
				}
				return response
			}
			return fail("direct child schema transport unavailable")
		}
		var reply map[string]any
		if recorder.Code >= http.StatusBadRequest || json.Unmarshal(recorder.Body.Bytes(), &reply) != nil ||
			firstString(reply, "status") != "completed" {
			return fail("direct child schema owner unavailable")
		}
		response := mapValue(reply["response"])
		if !validChildToolsArray(response["tools"]) {
			return fail("direct child schema response invalid")
		}
		tools = searchChildToolMaps(response["tools"])
	}

	if isLogicalBrowserAgent(child) {
		tools = hideBrowserRetargetTool(tools)
	}
	result["status"] = "completed"
	result["response"] = map[string]any{"tools": toolsForRequest(r, child.AgentID, tools)}
	return result
}

// Reuse the native mcp_tools receipt/queue for old executors only. All selection
// and dispatch happens under the existing task mutex, so concurrent cold reads
// cannot allocate multiple pending jobs for the same authorized connection.
func (s *Server) legacyChildSchemaForRequest(r *http.Request, child Agent) (map[string]any, bool) {
	s.mu.Lock()
	current, exists := s.exposedAgentByIDLocked(child.AgentID)
	parentID := firstString(current.Meta, "parent_server_id")
	parent := s.agents[parentID]
	_, routed := s.nodePeerRouteLocked(parentID)
	if !exists || !isChildMCPAgent(current) || parent == nil || !truthy(parent.Meta["approved"]) ||
		parent.Transport != "long_poll" || parent.Status != "online" || s.agentLifecycleStatusLocked(parent.LastSeen) != "online" ||
		s.localExecutors[parentID] != nil || routed || s.nodeConns.get(parentID) != nil ||
		r.Context().Err() != nil || !profileAllowsTarget(r, current.AgentID) || !agentOwnerConnectionAllowed(r, current) {
		s.mu.Unlock()
		return nil, false
	}
	caller := taskCallerFromRequest(r)
	args := childConnectionArguments(r, current, map[string]any{"ref": firstString(current.Meta, "child_ref")})
	// A refreshed lease is not a new connection; scope and instance still must
	// match. Use the existing receipt caller binding (nil is internal/control).
	delete(args, "_connection_expires_at")
	id := ""
	for _, receipt := range s.relayJobs {
		job := s.shellJobs[receipt.ID]
		if receipt.AgentID != current.AgentID || receipt.Method != childSchemaMethod || taskTerminal(receipt.Status) ||
			!(sameTaskCaller(caller, receipt.Caller) || caller == nil && receipt.Caller == nil) ||
			job == nil || job.ToolName != "mcp_tools" || "shell:"+job.Server != parentID {
			continue
		}
		previousArgs := cloneMap(job.Arguments)
		delete(previousArgs, "_connection_expires_at")
		if reflect.DeepEqual(args, previousArgs) {
			id = receipt.ID
			break
		}
	}
	if id == "" {
		id = newID()
		receipt := &relayJob{ID: id, AgentID: current.AgentID, Caller: caller,
			CreatedAt: nowFloat(), TraceID: requestTraceID(r), TraceParent: requestTraceParent(r)}
		if err := s.dispatchChildSchemaLocked(r, current, receipt); err != nil {
			s.mu.Unlock()
			return taskPersistenceFailure(err), true
		}
	}
	s.mu.Unlock()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, found := s.childSchemaJobForRequest(r, id)
		if !found {
			return map[string]any{"server_id": child.AgentID, "status": "failed",
				"error": map[string]any{"code": "child_schema_unavailable", "message": "legacy schema receipt unavailable"}}, true
		}
		if taskTerminal(firstString(response, "status")) {
			return response, true
		}
		select {
		case <-r.Context().Done():
			// Keep the same pending read-only receipt for the next cold search.
			// No caller-owned input is replayed and no background waiter survives.
			response["status"] = "failed"
			response["error"] = map[string]any{"code": "child_schema_unavailable", "message": "legacy schema pending; reuse existing receipt"}
			return response, true
		case <-ticker.C:
		}
	}
}

func validChildToolsArray(value any) bool {
	switch value.(type) {
	case []any, []map[string]any:
		return true
	}
	return false
}
