package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

// DirectChildTools is implemented by the embedded ShellMCP executor. A schema
// read is not executable work: never enqueue it or reserve a durable task ID.
type directChildToolReader interface {
	DirectChildTools(context.Context, map[string]any) (map[string]any, error)
}

const directChildSchemaTimeout = 8 * time.Second

// directChildSchemaForRequest reads one authorized child schema directly from
// its owning Node (or the embedded local executor). No legacy queue, outbox,
// durable Jobs receipt or replay is allowed on this read-only path.
//
// Historical child/tools/list receipts continue to be served separately by
// childSchemaJobForRequest; this function never touches their state.
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

func validChildToolsArray(value any) bool {
	switch value.(type) {
	case []any, []map[string]any:
		return true
	}
	return false
}
