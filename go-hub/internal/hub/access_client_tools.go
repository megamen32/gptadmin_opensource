package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Reuse the existing owner endpoints with the caller's original credentials.
func (s *Server) callAccessClientTool(r *http.Request, name string, args map[string]any) (map[string]any, int) {
	if r == nil {
		return map[string]any{"detail": "authenticated owner request required"}, 401
	}
	method, path := http.MethodGet, "/admin/api/clients"
	handler := s.adminClients
	body := map[string]any{}
	action, id := firstString(args, "action"), firstString(args, "id")
	if name == "access_clients" && action == "whoami" {
		claims, _ := r.Context().Value(authClaimsContextKey{}).(map[string]any)
		return map[string]any{"client_id": firstString(claims, "client_id"), "token_id": firstString(claims, "jti"), "admin": s.requestHasAdminRole(r), "access_mode": requestAccessMode(r)}, 200
	}
	if strings.ContainsAny(id, "/\\") {
		return map[string]any{"detail": "id must be one identifier"}, 400
	}
	if name == "operations" {
		path = "/admin/api/operations"
		handler = s.adminOperations
	} else {
		switch action {
		case "list":
		case "issue":
			method = http.MethodPost
			path = "/admin/api/mcp/issue-token"
			handler = s.adminMCPIssueToken
			for _, key := range []string{"client_id", "profile_id", "ttl_days", "access_mode", "role"} {
				if value, ok := args[key]; ok {
					body[key] = value
				}
			}
		case "token":
			if id == "" {
				return map[string]any{"detail": "id required"}, 400
			}
			path = "/admin/api/mcp/tokens/" + url.PathEscape(id) + "/value"
			handler = s.adminMCPTokenAction
		case "set_role":
			if id == "" {
				return map[string]any{"detail": "id required"}, 400
			}
			path = "/admin/api/client-roles/" + url.PathEscape(id)
			method = http.MethodPut
			handler = s.adminClientRole
			body["role"] = firstString(args, "role")
		case "bind", "unbind":
			if id == "" {
				return map[string]any{"detail": "id required"}, 400
			}
			method = http.MethodPut
			path = "/admin/api/client-bindings/" + url.PathEscape(id)
			handler = s.adminClientBinding
			body["profile_id"] = firstString(args, "profile_id")
			if action == "unbind" {
				method = http.MethodDelete
			}
		case "revoke":
			if id == "" {
				return map[string]any{"detail": "id required"}, 400
			}
			method = http.MethodDelete
			path = "/admin/api/clients/" + url.PathEscape(id)
			handler = s.adminClientDelete
		default:
			return map[string]any{"detail": "use list, issue, bind, unbind, revoke, token or set_role"}, 400
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return map[string]any{"detail": "invalid request"}, 400
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
	if name == "operations" {
		query := url.Values{}
		for _, key := range []string{"limit", "offset"} {
			if value, ok := args[key]; ok {
				query.Set(key, strconv.Itoa(intFromAny(value)))
			}
		}
		request.URL.RawQuery = query.Encode()
	}
	output := &accessToolResponse{header: make(http.Header)}
	s.requireCtl(s.trackAccessOperation(handler))(output, request)
	var result map[string]any
	if err := json.Unmarshal(output.body.Bytes(), &result); err != nil {
		return map[string]any{"detail": "invalid owner API response"}, 502
	}
	return result, output.status
}

func accessClientTools() []map[string]any {
	str := map[string]any{"type": "string"}
	return []map[string]any{
		{"name": "access_clients", "description": "Admin API for named MCP connections: list, issue with profile_id/role, bind/unbind, revoke, set_role, token (read saved value for exact id). Explicit admin/owner authority required; full execution alone is not admin.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"list", "issue", "bind", "unbind", "revoke", "token", "set_role", "whoami"}}, "id": str, "client_id": str, "profile_id": str, "role": map[string]any{"type": "string", "enum": []string{"client", "admin", "owner"}}, "ttl_days": map[string]any{"type": "integer", "minimum": 0, "maximum": 3650}, "access_mode": map[string]any{"type": "string", "enum": []string{"full", "readonly"}}}, "required": []string{"action"}, "additionalProperties": false}},
		{"name": "operations", "description": "Owner API: persisted access-operation history. A started operation without completion was interrupted; inspect rather than blindly repeat.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}, "offset": map[string]any{"type": "integer", "minimum": 0}}, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false}},
	}
}
