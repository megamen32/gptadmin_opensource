package hub

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	webhookRoutesListTool   = "webhook_routes_list"
	webhookRouteCreateTool  = "webhook_route_create"
	webhookRouteReplaceTool = "webhook_route_replace"
	webhookRouteDeleteTool  = "webhook_route_delete"
	webhookJobGetTool       = "webhook_job_get"
)

type webhookOperationError struct {
	Status int
	Detail string
}

func (e *webhookOperationError) Error() string { return e.Detail }

func isWebhookHubTool(name string) bool {
	switch name {
	case webhookRoutesListTool, webhookRouteCreateTool, webhookRouteReplaceTool, webhookRouteDeleteTool, webhookJobGetTool:
		return true
	default:
		return false
	}
}

func (s *Server) callWebhookHubTool(name string, args map[string]any) (map[string]any, int) {
	switch name {
	case webhookRoutesListTool:
		return map[string]any{"routes": s.listWebhookRouteSummaries()}, http.StatusOK
	case webhookRouteCreateTool:
		route, err := webhookRouteFromAny(args["route"])
		if err != nil {
			return map[string]any{"error": err.Error()}, http.StatusBadRequest
		}
		summary, operationErr := s.createWebhookRoute(route)
		if operationErr != nil {
			return map[string]any{"error": operationErr.Detail}, operationErr.Status
		}
		return webhookRouteSummaryMap(summary), http.StatusCreated
	case webhookRouteReplaceTool:
		routeID := firstString(args, "id", "route_id")
		if routeID == "" {
			return map[string]any{"error": "route id is required"}, http.StatusBadRequest
		}
		route, err := webhookRouteFromAny(args["route"])
		if err != nil {
			return map[string]any{"error": err.Error()}, http.StatusBadRequest
		}
		summary, operationErr := s.replaceWebhookRoute(routeID, route)
		if operationErr != nil {
			return map[string]any{"error": operationErr.Detail}, operationErr.Status
		}
		return webhookRouteSummaryMap(summary), http.StatusOK
	case webhookRouteDeleteTool:
		routeID := firstString(args, "id", "route_id")
		if routeID == "" {
			return map[string]any{"error": "route id is required"}, http.StatusBadRequest
		}
		if !truthy(args["confirm"]) {
			return map[string]any{"error": "confirm=true is required to delete a webhook route"}, http.StatusBadRequest
		}
		if operationErr := s.deleteWebhookRoute(routeID); operationErr != nil {
			return map[string]any{"error": operationErr.Detail}, operationErr.Status
		}
		return map[string]any{"ok": true, "deleted": true, "id": routeID}, http.StatusOK
	case webhookJobGetTool:
		jobID := firstString(args, "id", "job_id")
		if jobID == "" {
			return map[string]any{"error": "job id is required"}, http.StatusBadRequest
		}
		job, operationErr := s.getWebhookJob(jobID)
		if operationErr != nil {
			return map[string]any{"error": operationErr.Detail, "job_id": jobID}, operationErr.Status
		}
		return webhookJobMap(job), http.StatusOK
	default:
		return map[string]any{"error": "unsupported webhook tool", "tool": name}, http.StatusBadRequest
	}
}

func webhookRouteFromAny(value any) (WebhookRoute, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return WebhookRoute{}, err
	}
	var route WebhookRoute
	if err := json.Unmarshal(encoded, &route); err != nil {
		return WebhookRoute{}, err
	}
	return route, nil
}

func (s *Server) listWebhookRouteSummaries() []webhookRouteSummary {
	s.mu.Lock()
	routes := make([]webhookRouteSummary, 0, len(s.webhookRoutes))
	for _, route := range s.webhookRoutes {
		routes = append(routes, summarizeWebhookRoute(route))
	}
	s.mu.Unlock()
	sort.Slice(routes, func(i, j int) bool { return routes[i].ID < routes[j].ID })
	return routes
}

func (s *Server) createWebhookRoute(route WebhookRoute) (webhookRouteSummary, *webhookOperationError) {
	if err := validateWebhookRoutes([]WebhookRoute{route}); err != nil {
		return webhookRouteSummary{}, &webhookOperationError{Status: http.StatusBadRequest, Detail: err.Error()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.webhookRoutes[route.ID]; exists {
		return webhookRouteSummary{}, &webhookOperationError{Status: http.StatusConflict, Detail: "webhook route already exists"}
	}
	s.webhookRoutes[route.ID] = route
	if err := s.saveWebhookRoutesLocked(); err != nil {
		delete(s.webhookRoutes, route.ID)
		return webhookRouteSummary{}, &webhookOperationError{Status: http.StatusInternalServerError, Detail: err.Error()}
	}
	return summarizeWebhookRoute(route), nil
}

func (s *Server) replaceWebhookRoute(routeID string, route WebhookRoute) (webhookRouteSummary, *webhookOperationError) {
	routeID = strings.TrimSpace(routeID)
	if route.ID != "" && route.ID != routeID {
		return webhookRouteSummary{}, &webhookOperationError{Status: http.StatusBadRequest, Detail: "route id does not match path"}
	}
	route.ID = routeID
	if err := validateWebhookRoutes([]WebhookRoute{route}); err != nil {
		return webhookRouteSummary{}, &webhookOperationError{Status: http.StatusBadRequest, Detail: err.Error()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.webhookRoutes[routeID]
	if !exists {
		return webhookRouteSummary{}, &webhookOperationError{Status: http.StatusNotFound, Detail: "unknown webhook route"}
	}
	s.webhookRoutes[routeID] = route
	if err := s.saveWebhookRoutesLocked(); err != nil {
		s.webhookRoutes[routeID] = previous
		return webhookRouteSummary{}, &webhookOperationError{Status: http.StatusInternalServerError, Detail: err.Error()}
	}
	return summarizeWebhookRoute(route), nil
}

func (s *Server) deleteWebhookRoute(routeID string) *webhookOperationError {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.webhookRoutes[routeID]
	if !exists {
		return &webhookOperationError{Status: http.StatusNotFound, Detail: "unknown webhook route"}
	}
	delete(s.webhookRoutes, routeID)
	if err := s.saveWebhookRoutesLocked(); err != nil {
		s.webhookRoutes[routeID] = previous
		return &webhookOperationError{Status: http.StatusInternalServerError, Detail: err.Error()}
	}
	return nil
}

func (s *Server) getWebhookJob(jobID string) (*webhookJob, *webhookOperationError) {
	s.mu.Lock()
	job := cloneWebhookJob(s.webhookJobs[jobID])
	s.mu.Unlock()
	if job == nil {
		return nil, &webhookOperationError{Status: http.StatusNotFound, Detail: "unknown webhook job"}
	}
	return job, nil
}

func webhookRouteSummaryMap(summary webhookRouteSummary) map[string]any {
	encoded, _ := json.Marshal(summary)
	result := map[string]any{}
	_ = json.Unmarshal(encoded, &result)
	return result
}

func webhookJobMap(job *webhookJob) map[string]any {
	encoded, _ := json.Marshal(job)
	result := map[string]any{}
	_ = json.Unmarshal(encoded, &result)
	return result
}

func webhookRouteInputSchema() map[string]any {
	secret := map[string]any{"type": "string", "minLength": 1, "writeOnly": true}
	callback := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":         map[string]any{"type": "string", "minLength": 1},
			"token":       secret,
			"hmac_secret": secret,
		},
		"required":             []string{"url"},
		"additionalProperties": false,
	}
	action := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind":          map[string]any{"type": "string", "enum": []string{"mcp", "prompt", "shell"}},
			"target":        map[string]any{"type": "string", "minLength": 1},
			"approval_mode": map[string]any{"type": "string", "enum": []string{approvalModeAskBeforeWrite, approvalModeBoundedAutonomous}},
			"tool":          map[string]any{"type": "string"},
			"arguments":     map[string]any{"type": "object", "additionalProperties": true},
			"prompt":        map[string]any{"type": "string"},
			"prompt_arg":    map[string]any{"type": "string"},
			"command":       map[string]any{"type": "string"},
			"cwd":           map[string]any{"type": "string"},
			"delay_seconds": map[string]any{"type": "number", "minimum": 0, "maximum": 86400},
		},
		"required":             []string{"kind", "target"},
		"additionalProperties": false,
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":                map[string]any{"type": "string", "minLength": 1},
			"token":             secret,
			"hmac_secret":       secret,
			"signature_version": map[string]any{"type": "string", "enum": []string{"v1", "v2"}},
			"max_skew_seconds":  map[string]any{"type": "integer", "minimum": 1},
			"action":            action,
			"actions":           map[string]any{"type": "array", "minItems": 1, "items": action},
			"callback":          callback,
		},
		"required":             []string{"id"},
		"additionalProperties": false,
	}
}

func webhookHubTools() []map[string]any {
	route := webhookRouteInputSchema()
	return []map[string]any{
		{"name": webhookRoutesListTool, "description": "List secret-free webhook routes", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
		{"name": webhookRouteCreateTool, "description": "Create a fixed-target route; secrets are write-only", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"route": route}, "required": []string{"route"}, "additionalProperties": false}},
		{"name": webhookRouteReplaceTool, "description": "Replace a complete route with write-only secrets", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "route": route}, "required": []string{"id", "route"}, "additionalProperties": false}},
		{"name": webhookRouteDeleteTool, "description": "Delete a route with confirm=true", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "confirm": map[string]any{"type": "boolean"}}, "required": []string{"id", "confirm"}, "additionalProperties": false}},
		{"name": webhookJobGetTool, "description": "Read a durable webhook job", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []string{"id"}, "additionalProperties": false}},
	}
}

func webhookAppsTools(readSecurity, execSecurity []map[string]any, readMeta, execMeta map[string]any) []map[string]any {
	definitions := webhookHubTools()
	result := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		name := firstString(definition, "name")
		readOnly := name == webhookRoutesListTool || name == webhookJobGetTool
		security := execSecurity
		meta := execMeta
		if readOnly {
			security = readSecurity
			meta = readMeta
		}
		result = append(result, map[string]any{
			"name":            name,
			"description":     definition["description"],
			"inputSchema":     compactWebhookAppsInputSchema(name, definition["inputSchema"]),
			"outputSchema":    map[string]any{"type": "object"},
			"annotations":     map[string]any{"readOnlyHint": readOnly, "destructiveHint": name == webhookRouteDeleteTool, "openWorldHint": false},
			"securitySchemes": security,
			"_meta":           meta,
		})
	}
	return result
}

func compactWebhookAppsInputSchema(name string, fallback any) any {
	route := map[string]any{"type": "object", "additionalProperties": true}
	switch name {
	case webhookRoutesListTool:
		return map[string]any{"type": "object"}
	case webhookRouteCreateTool:
		return map[string]any{"type": "object", "properties": map[string]any{"route": route}, "required": []string{"route"}, "additionalProperties": false}
	case webhookRouteReplaceTool:
		return map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "route": route}, "required": []string{"id", "route"}, "additionalProperties": false}
	case webhookRouteDeleteTool:
		return map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "confirm": map[string]any{"type": "boolean"}}, "required": []string{"id", "confirm"}}
	case webhookJobGetTool:
		return map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []string{"id"}}
	default:
		return fallback
	}
}

type webhookJobSummary struct {
	ID             string    `json:"job_id"`
	RouteID        string    `json:"route_id"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
	Error          string    `json:"error,omitempty"`
	CallbackStatus string    `json:"callback_status,omitempty"`
}

func (s *Server) webhookJobOverview(limit int) ([]webhookJobSummary, map[string]webhookJobSummary) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	s.mu.Lock()
	items := make([]webhookJobSummary, 0, len(s.webhookJobs))
	for _, job := range s.webhookJobs {
		if job == nil {
			continue
		}
		items = append(items, webhookJobSummary{
			ID: job.ID, RouteID: job.RouteID, Status: job.Status, CreatedAt: job.CreatedAt,
			CompletedAt: job.CompletedAt, Error: boundedString(job.Error, 240), CallbackStatus: job.CallbackStatus,
		})
	}
	s.mu.Unlock()
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	latest := make(map[string]webhookJobSummary)
	for _, item := range items {
		if _, exists := latest[item.RouteID]; !exists {
			latest[item.RouteID] = item
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, latest
}

func (s *Server) adminWebhookJobEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	jobID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/api/webhook-jobs"), "/")
	if jobID == "" {
		if err := authorizeFacadeCall(r, webhookJobGetTool, nil); err != nil {
			s.auditToolDecision(r, "hub", webhookJobGetTool, nil, "deny", err.Error(), nil, http.StatusForbidden)
			writeJSON(w, http.StatusForbidden, map[string]any{"detail": err.Error()})
			return
		}
		jobs, latest := s.webhookJobOverview(50)
		writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "latest_by_route": latest})
		return
	}
	if strings.Contains(jobID, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid job_id"})
		return
	}
	if err := authorizeFacadeCall(r, webhookJobGetTool, map[string]any{"id": jobID}); err != nil {
		s.auditToolDecision(r, "hub", webhookJobGetTool, map[string]any{"id": jobID}, "deny", err.Error(), nil, http.StatusForbidden)
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": err.Error()})
		return
	}
	job, operationErr := s.getWebhookJob(jobID)
	if operationErr != nil {
		writeJSON(w, operationErr.Status, map[string]any{"detail": operationErr.Detail})
		return
	}
	writeJSON(w, http.StatusOK, job)
}
