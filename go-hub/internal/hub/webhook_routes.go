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

	switch {
	case routeID == "" && r.Method == http.MethodGet:
		s.mu.Lock()
		routes := make([]webhookRouteSummary, 0, len(s.webhookRoutes))
		for _, route := range s.webhookRoutes {
			routes = append(routes, summarizeWebhookRoute(route))
		}
		s.mu.Unlock()
		sort.Slice(routes, func(i, j int) bool { return routes[i].ID < routes[j].ID })
		writeJSON(w, http.StatusOK, map[string]any{"routes": routes})
	case routeID == "" && r.Method == http.MethodPost:
		s.writeWebhookRoute(w, r, "", http.StatusCreated)
	case routeID != "" && r.Method == http.MethodPut:
		s.writeWebhookRoute(w, r, routeID, http.StatusOK)
	case routeID != "" && r.Method == http.MethodDelete:
		s.mu.Lock()
		if _, ok := s.webhookRoutes[routeID]; !ok {
			s.mu.Unlock()
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown webhook route"})
			return
		}
		delete(s.webhookRoutes, routeID)
		err := s.saveWebhookRoutesLocked()
		s.mu.Unlock()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func (s *Server) writeWebhookRoute(w http.ResponseWriter, r *http.Request, routeID string, status int) {
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
	if routeID != "" {
		if route.ID != "" && route.ID != routeID {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "route id does not match path"})
			return
		}
		route.ID = routeID
	}
	if err := validateWebhookRoutes([]WebhookRoute{route}); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}

	s.mu.Lock()
	_, exists := s.webhookRoutes[route.ID]
	if routeID == "" && exists {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "webhook route already exists"})
		return
	}
	if routeID != "" && !exists {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown webhook route"})
		return
	}
	s.webhookRoutes[route.ID] = route
	err = s.saveWebhookRoutesLocked()
	s.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, status, summarizeWebhookRoute(route))
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
