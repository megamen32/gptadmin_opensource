package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// The model uses the exact same authenticated profile API as the browser.
// Forward credentials/context without manufacturing an owner or weakening gates.
func (s *Server) callAccessProfileTool(r *http.Request, args map[string]any) (map[string]any, int) {
	if r == nil {
		return map[string]any{"detail": "authenticated owner request required"}, http.StatusUnauthorized
	}
	action, id := firstString(args, "action"), firstString(args, "id")
	method, path := http.MethodGet, "/admin/api/access-profiles"
	handler := s.adminAccessProfiles
	body := map[string]any{}
	if action != "list" {
		if id == "" || strings.ContainsAny(id, "/\\") {
			return map[string]any{"detail": "a single profile id is required"}, 400
		}
		path += "/" + url.PathEscape(id)
		handler = s.adminAccessProfile
		switch action {
		case "get":
		case "create", "update":
			method = http.MethodPut
			body = cloneMap(mapValue(args["profile"]))
		default:
			return map[string]any{"detail": "action must be list, get, create or update"}, 400
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return map[string]any{"detail": "invalid profile"}, 400
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
		etag := firstString(args, "etag")
		if action == "create" {
			etag = "*"
		}
		request.Header.Set("If-Match", etag)
	}
	output := &accessToolResponse{header: make(http.Header)}
	s.requireCtl(s.trackAccessOperation(handler))(output, request)
	var result map[string]any
	if err := json.Unmarshal(output.body.Bytes(), &result); err != nil {
		return map[string]any{"detail": "invalid profile response"}, 502
	}
	if etag := output.Header().Get("ETag"); etag != "" {
		result["etag"] = etag
	}
	return result, output.status
}

type accessToolResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *accessToolResponse) Header() http.Header { return w.header }
func (w *accessToolResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *accessToolResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(data)
}

func accessProfileTool() map[string]any {
	str := map[string]any{"type": "string"}
	list := map[string]any{"type": "array", "items": str}
	profile := map[string]any{"type": "object", "properties": map[string]any{"name": str, "access_mode": map[string]any{"type": "string", "enum": []string{"full", "readonly"}}, "approval_mode": map[string]any{"type": "string", "enum": []string{"unrestricted", "read_only", "ask_before_write", "bounded_autonomous"}}, "instruction_set_id": str, "allowed_targets": list, "allowed_tools": list, "workspace_refs": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}}, "additionalProperties": false}
	return map[string]any{"name": "access_profiles", "description": "Owner API: list/get/create/update access profiles. Read before update and pass returned etag. Browser/CTL or explicitly delegated admin/owner authorization; no shell required.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"list", "get", "create", "update"}}, "id": str, "etag": str, "profile": profile}, "required": []string{"action"}, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": false}}
}
