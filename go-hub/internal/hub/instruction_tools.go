package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Route the MCP operations through the existing authenticated admin handlers.
// In particular, updates retain the file lock, ETag and quorum commit.
func (s *Server) callInstructionSetTool(r *http.Request, args map[string]any) (map[string]any, int) {
	if r == nil {
		return map[string]any{"detail": "authenticated admin request required"}, http.StatusUnauthorized
	}
	if err := authorizeToolAction(r, "hub", "instruction_sets", args); err != nil {
		return map[string]any{"detail": err.Error()}, http.StatusForbidden
	}
	action, id := firstString(args, "action"), firstString(args, "id")
	method, path := http.MethodGet, "/admin/api/instruction-sets"
	handler := s.adminInstructionSets
	body := map[string]any{}
	switch action {
	case "list":
	case "get", "update":
		if id == "" {
			id = defaultInstructionSetID
		}
		if id != defaultInstructionSetID {
			if err := validateNamedInstructionSetID(id); err != nil {
				return map[string]any{"detail": err.Error()}, http.StatusBadRequest
			}
		}
		path += "/" + id
		handler = s.adminInstructionSet
		if id == defaultInstructionSetID {
			handler = s.adminDefaultInstructionSet
		}
		if action == "update" {
			etag := strings.TrimSpace(firstString(args, "etag"))
			if etag == "" || etag == "*" {
				return map[string]any{"detail": "read the instruction set and pass its exact etag"}, http.StatusPreconditionRequired
			}
			content, ok := args["content"].(string)
			if !ok {
				return map[string]any{"detail": "content must be a string"}, http.StatusBadRequest
			}
			method = http.MethodPut
			body["content"] = content
		}
	default:
		return map[string]any{"detail": "action must be list, get or update"}, http.StatusBadRequest
	}
	data, err := json.Marshal(body)
	if err != nil {
		return map[string]any{"detail": "invalid instruction set"}, http.StatusBadRequest
	}
	request := r.Clone(r.Context())
	u := *r.URL
	request.URL = &u
	request.URL.Path = path
	request.URL.RawPath = ""
	request.URL.RawQuery = ""
	request.Method = method
	request.Header = r.Header.Clone()
	request.Header.Set("Content-Type", "application/json")
	request.Body = io.NopCloser(bytes.NewReader(data))
	request.ContentLength = int64(len(data))
	if method == http.MethodPut {
		request.Header.Set("If-Match", firstString(args, "etag"))
	}
	output := &accessToolResponse{header: make(http.Header)}
	s.requireCtl(s.trackAccessOperation(handler))(output, request)
	var result map[string]any
	if err := json.Unmarshal(output.body.Bytes(), &result); err != nil {
		return map[string]any{"detail": "invalid instruction set response"}, http.StatusBadGateway
	}
	if etag := output.Header().Get("ETag"); etag != "" {
		result["etag"] = etag
	}
	if operation := output.Header().Get("X-Operation-ID"); operation != "" {
		result["operation_id"] = operation
	}
	return result, output.status
}

func instructionSetTool() map[string]any {
	str := map[string]any{"type": "string"}
	return map[string]any{
		"name":        "instruction_sets",
		"description": "Admin API: list, get or update shared (default) and named instruction sets. Read first and pass the exact returned etag with the full updated content. Existing admin/owner authority required. Updates use the existing quorum-backed admin API.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list", "get", "update"}},
			"id":     str, "etag": str, "content": str,
		}, "required": []string{"action"}, "additionalProperties": false},
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": false},
	}
}
