package hub

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

func normalizedAccessRole(role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		role = "client"
	}
	if role != "client" && role != "admin" && role != "owner" {
		return "", errors.New("role must be client, admin or owner")
	}
	return role, nil
}

// Authentication establishes the principal; stored role establishes authority.
// Full execution, a JWT sub=admin, or a caller-supplied role claim is NOT a role.
func (s *Server) requestHasAdminRole(r *http.Request) bool {
	if r == nil || requestAccessMode(r) == accessModeReadonly {
		return false
	}
	claims, _ := r.Context().Value(authClaimsContextKey{}).(map[string]any)
	s.mu.Lock()
	defer s.mu.Unlock()
	role := ""
	if record, ok := s.managedMCP[firstString(claims, "jti")]; ok {
		if record.RevokedAt != 0 {
			return false
		}
		role = record.Role
	}
	if role == "" {
		role = s.oauthClients[firstString(claims, "client_id")].Role
	}
	return role == "admin" || role == "owner"
}
func (s *Server) clientHTTPPathAllowed(r *http.Request) bool {
	if mcpClientHTTPPathAllowed(r.URL.Path) {
		return true
	}
	if !s.requestHasAdminRole(r) {
		return false
	}
	tool := ""
	switch {
	case strings.HasPrefix(r.URL.Path, "/admin/api/access-profiles"):
		tool = "access_profiles"
	case strings.HasPrefix(r.URL.Path, "/admin/api/client"), strings.HasPrefix(r.URL.Path, "/admin/api/mcp/tokens/"), r.URL.Path == "/admin/api/mcp/issue-token":
		tool = "access_clients"
	case r.URL.Path == "/admin/api/operations":
		tool = "operations"
	default:
		if _, bound := AccessProfileFromRequest(r); bound {
			return false
		}
	}
	return profileAllowsTarget(r, "hub") && (tool == "" || profileAllowsTool(r, tool))
}
func (s *Server) adminClientRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeJSON(w, 405, map[string]any{"detail": "use PUT"})
		return
	}
	id, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/admin/api/client-roles/"))
	if err != nil || id == "" || strings.ContainsAny(id, "/\\") {
		writeJSON(w, 400, map[string]any{"detail": "invalid client id"})
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := readBoundedJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"detail": err.Error()})
		return
	}
	role, err := normalizedAccessRole(req.Role)
	if err != nil {
		writeJSON(w, 400, map[string]any{"detail": err.Error()})
		return
	}
	if err = s.refreshAccessState(); err != nil {
		writeJSON(w, 503, map[string]any{"detail": "access state unavailable"})
		return
	}
	s.mu.Lock()
	if record, ok := s.managedMCP[id]; ok {
		record.Role = role
		s.managedMCP[id] = record
		err = s.saveManagedMCPStateLocked()
	} else if record, ok := s.oauthClients[id]; ok {
		record.Role = role
		s.oauthClients[id] = record
		err = s.saveOAuthClientsStateLocked()
	} else {
		s.mu.Unlock()
		writeJSON(w, 404, map[string]any{"detail": "client not found"})
		return
	}
	s.mu.Unlock()
	if err != nil {
		writeJSON(w, 409, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": id, "role": role})
}
func (s *Server) adminTokenValue(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Cache-Control", "no-store")
	if err := s.refreshAccessState(); err != nil {
		writeJSON(w, 503, map[string]any{"detail": "access state unavailable"})
		return
	}
	s.mu.Lock()
	record, exists := s.managedMCP[id]
	value := record.TokenValue
	if value == "" && record.TokenKind == configuredMCPBearerTokenKind {
		for name, candidate := range s.cfg.ExistingMCPBearers {
			if configuredMCPBearerID(name) == id && configuredMCPBearerDigest(candidate) == record.TokenDigest {
				value = candidate
				break
			}
		}
	}
	s.mu.Unlock()
	if !exists {
		writeJSON(w, 404, map[string]any{"detail": "token not found"})
		return
	}
	if value == "" {
		writeJSON(w, 409, map[string]any{"detail": "original token value was not retained; explicit rotation is required", "code": "token_value_unavailable", "token_id": id})
		return
	}
	writeJSON(w, 200, map[string]any{"token_id": id, "client_id": record.ClientID, "access_token": value, "role": record.Role, "revoked": record.RevokedAt != 0, "token_type": "Bearer", "mcp_url": s.origin(r) + "/mcp"})
}
