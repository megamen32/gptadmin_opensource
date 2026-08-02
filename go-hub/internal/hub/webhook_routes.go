package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type webhookRouteSummary struct {
	ID                 string `json:"id"`
	Kind               string `json:"kind"`
	Target             string `json:"target"`
	Tool               string `json:"tool,omitempty"`
	AuthMode           string `json:"auth_mode"`
	CallbackConfigured bool   `json:"callback_configured"`
}

func (s *Server) webhookRoutesEndpoint(w http.ResponseWriter, r *http.Request) {
	suffix := strings.TrimPrefix(r.URL.Path, "/webhook-routes")
	routeID := strings.Trim(suffix, "/")
	if strings.Contains(routeID, "/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown webhook route"})
		return
	}
	toolName := ""
	switch {
	case routeID == "" && r.Method == http.MethodGet:
		toolName = webhookRoutesListTool
	case routeID == "" && r.Method == http.MethodPost:
		toolName = webhookRouteCreateTool
	case routeID != "" && r.Method == http.MethodPut:
		toolName = webhookRouteReplaceTool
	case routeID != "" && r.Method == http.MethodDelete:
		toolName = webhookRouteDeleteTool
	}
	if toolName != "" {
		if err := authorizeFacadeCall(r, toolName, nil); err != nil {
			s.auditToolDecision(r, "hub", toolName, nil, "deny", err.Error(), nil, http.StatusForbidden)
			writeJSON(w, http.StatusForbidden, map[string]any{"detail": err.Error()})
			return
		}
	}

	switch {
	case routeID == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"routes": s.listWebhookRouteSummaries()})
	case routeID == "" && r.Method == http.MethodPost:
		s.writeWebhookRoute(w, r, "", http.StatusCreated, webhookRouteCreateTool)
	case routeID != "" && r.Method == http.MethodPut:
		s.writeWebhookRoute(w, r, routeID, http.StatusOK, webhookRouteReplaceTool)
	case routeID != "" && r.Method == http.MethodDelete:
		args := map[string]any{"id": routeID, "confirm": true}
		if !s.webhookHTTPPolicyGate(w, r, webhookRouteDeleteTool, args) {
			return
		}
		if operationErr := s.deleteWebhookRoute(routeID); operationErr != nil {
			writeJSON(w, operationErr.Status, map[string]any{"detail": operationErr.Detail})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func (s *Server) writeWebhookRoute(w http.ResponseWriter, r *http.Request, routeID string, status int, toolName string) {
	body, err := readWebhookBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	var route WebhookRoute
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(&route); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": fmt.Sprintf("invalid webhook route: %v", err)})
		return
	}
	args := map[string]any{"route": route}
	if routeID != "" {
		args["id"] = routeID
	}
	if !s.webhookHTTPPolicyGate(w, r, toolName, args) {
		return
	}
	var summary webhookRouteSummary
	var operationErr *webhookOperationError
	if routeID == "" {
		summary, operationErr = s.createWebhookRoute(route)
	} else {
		summary, operationErr = s.replaceWebhookRoute(routeID, route)
	}
	if operationErr != nil {
		writeJSON(w, operationErr.Status, map[string]any{"detail": operationErr.Detail})
		return
	}
	writeJSON(w, status, summary)
}

func (s *Server) webhookHTTPPolicyGate(w http.ResponseWriter, r *http.Request, toolName string, args map[string]any) bool {
	approvalID := strings.TrimSpace(r.Header.Get("X-GPTAdmin-Approval-ID"))
	if response, blocked := s.approvalGate(r, "hub", toolName, args, approvalID); blocked {
		s.auditToolDecision(r, "hub", toolName, args, "deny", "approval required", response, http.StatusPreconditionRequired)
		writeJSON(w, http.StatusPreconditionRequired, response)
		return false
	}
	if response, blocked := s.boundedAutonomousGate(r, "hub", toolName); blocked {
		s.auditToolDecision(r, "hub", toolName, args, "deny", "bounded autonomous budget exhausted", response, http.StatusTooManyRequests)
		writeJSON(w, http.StatusTooManyRequests, response)
		return false
	}
	return true
}

func summarizeWebhookRoute(route WebhookRoute) webhookRouteSummary {
	authMode := "token"
	if route.HMACSecret != "" {
		authMode = "hmac"
	}
	return webhookRouteSummary{
		ID:                 route.ID,
		Kind:               route.Action.Kind,
		Target:             route.Action.Target,
		Tool:               route.Action.Tool,
		AuthMode:           authMode,
		CallbackConfigured: route.Callback != nil,
	}
}

func (s *Server) saveWebhookRoutesLocked() error {
	path := s.cfg.WebhookConfigFile
	if path == "" {
		return errors.New("webhook route persistence is not configured")
	}
	routes := make([]WebhookRoute, 0, len(s.webhookRoutes))
	for _, route := range s.webhookRoutes {
		routes = append(routes, route)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].ID < routes[j].ID })
	b, err := json.MarshalIndent(webhookConfigFile{Routes: routes}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
