package hub

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const systemResourceSummaryTool = "system_resource_summary"

func (s *Server) adminServerResources(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if cookieAdminSessionPresent(r, s) && !sameOriginAdminMutation(r, adminRequestOrigin(r)) {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "same-origin admin request required"})
		return
	}
	var request struct {
		ServerID string `json:"server_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "server_id is required"})
		return
	}
	serverID := strings.TrimSpace(request.ServerID)
	if !strings.HasPrefix(serverID, "shell:") || strings.TrimSpace(strings.TrimPrefix(serverID, "shell:")) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "server_id must be an exact shell:<name> target"})
		return
	}
	s.mu.Lock()
	agent := s.agents[serverID]
	if agent == nil || agent.Kind != "virtual_shell" {
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, unavailableResourceSummary(serverID, "unknown", 0, "node_not_registered"))
		return
	}
	lastSeen := agent.LastSeen
	nodeStatus := s.agentLifecycleStatusLocked(lastSeen)
	if nodeStatus != "online" {
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, unavailableResourceSummary(serverID, nodeStatus, lastSeen, "node_not_online"))
		return
	}
	s.mu.Unlock()

	response, status := s.executeMCPTool(nonAgentWorkflow(r), serverID, systemResourceSummaryTool, map[string]any{}, false, s.cfg.ActionSyncWait, "")
	responseStatus := firstString(response, "status")
	if status != http.StatusOK || responseStatus == "failed" {
		writeJSON(w, http.StatusOK, unavailableResourceSummary(serverID, nodeStatus, lastSeen, "resource_sample_failed"))
		return
	}
	if responseStatus == "running" || truthy(response["background"]) {
		writeJSON(w, http.StatusOK, map[string]any{
			"server_id": serverID, "node_status": nodeStatus, "last_seen": lastSeen,
			"status": "pending", "sampled_at": nil, "job_id": firstString(response, "job_id", "task_id"),
		})
		return
	}
	sample := resourceSummaryFromResponse(response)
	if !validResourceSummary(sample) {
		writeJSON(w, http.StatusOK, unavailableResourceSummary(serverID, nodeStatus, lastSeen, "invalid_resource_sample"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server_id": serverID, "node_status": nodeStatus, "last_seen": lastSeen,
		"status": sample["status"], "sampled_at": sample["sampled_at"],
		"cpu": sample["cpu"], "memory": sample["memory"], "root_filesystem": sample["root_filesystem"],
	})
}

func cookieAdminSessionPresent(r *http.Request, s *Server) bool {
	if cookie, err := r.Cookie(adminSessionCookieName); err == nil && cookie.Value != "" && s.adminSessionValid(r) {
		return true
	}
	return r.Header.Get("X-GPTAdmin-Airlock-Identity") != "" && s.adminSessionValid(r)
}

func sameOriginAdminMutation(r *http.Request, expected string) bool {
	expectedURL, err := url.Parse(strings.TrimRight(expected, "/"))
	if err != nil || expectedURL.Scheme == "" || expectedURL.Host == "" {
		return false
	}
	actual := strings.TrimSpace(r.Header.Get("Origin"))
	if actual == "" {
		if referer, parseErr := url.Parse(r.Referer()); parseErr == nil && referer.Scheme != "" && referer.Host != "" {
			actual = referer.Scheme + "://" + referer.Host
		}
	}
	actualURL, err := url.Parse(strings.TrimRight(actual, "/"))
	return err == nil && strings.EqualFold(expectedURL.Scheme, actualURL.Scheme) && strings.EqualFold(expectedURL.Host, actualURL.Host)
}

// adminRequestOrigin derives the browser-facing origin from the admitted
// request authority. The supported ingress preserves Host and sets a single
// X-Forwarded-Proto value; X-Forwarded-Host is intentionally not trusted.
func adminRequestOrigin(r *http.Request) string {
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else {
		forwardedProtos := r.Header.Values("X-Forwarded-Proto")
		if len(forwardedProtos) > 1 {
			return ""
		}
		if len(forwardedProtos) == 1 {
			switch strings.ToLower(strings.TrimSpace(forwardedProtos[0])) {
			case "https":
				scheme = "https"
			case "http":
			default:
				return ""
			}
		}
	}
	host := strings.TrimSpace(r.Host)
	if host == "" || strings.ContainsAny(host, "/?#@") {
		return ""
	}
	parsed, err := url.Parse(scheme + "://" + host)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	return scheme + "://" + parsed.Host
}

func unavailableResourceSummary(serverID, nodeStatus string, lastSeen float64, reason string) map[string]any {
	component := func() map[string]any { return map[string]any{"status": "unavailable", "error": reason} }
	return map[string]any{
		"server_id": serverID, "node_status": nodeStatus, "last_seen": lastSeen,
		"status": "unavailable", "sampled_at": nil, "error": reason,
		"cpu": component(), "memory": component(), "root_filesystem": component(),
	}
}

func resourceSummaryFromResponse(response map[string]any) map[string]any {
	queue := []map[string]any{response}
	for depth := 0; len(queue) > 0 && depth < 16; depth++ {
		current := queue[0]
		queue = queue[1:]
		if firstString(current, "sampled_at") != "" && current["cpu"] != nil && current["memory"] != nil && current["root_filesystem"] != nil {
			return current
		}
		for _, key := range []string{"response", "structuredContent", "result"} {
			if nested, ok := current[key].(map[string]any); ok {
				queue = append(queue, nested)
			}
		}
	}
	return nil
}

func validResourceSummary(sample map[string]any) bool {
	if sample == nil || !oneOf(firstString(sample, "status"), "available", "partial", "unavailable") {
		return false
	}
	sampledAt, err := time.Parse(time.RFC3339Nano, firstString(sample, "sampled_at"))
	if err != nil || sampledAt.IsZero() {
		return false
	}
	available := 0
	for _, key := range []string{"cpu", "memory", "root_filesystem"} {
		component := mapValue(sample[key])
		status := firstString(component, "status")
		if !oneOf(status, "available", "unavailable") {
			return false
		}
		if status == "available" {
			available++
			if !validResourceComponent(key, component) {
				return false
			}
		} else if firstString(component, "error") == "" {
			return false
		}
	}
	if available == 3 {
		return firstString(sample, "status") == "available"
	}
	if available == 0 {
		return firstString(sample, "status") == "unavailable"
	}
	return firstString(sample, "status") == "partial"
}

func validResourceComponent(name string, component map[string]any) bool {
	switch name {
	case "cpu":
		cores, coresOK := positiveMetric(component["cores"])
		utilization, utilizationOK := numericMetric(component["utilization_percent"])
		interval, intervalOK := positiveMetric(component["sample_interval_ms"])
		return coresOK && cores >= 1 && utilizationOK && utilization >= 0 && utilization <= 100 && intervalOK && interval >= 1
	case "memory":
		total, totalOK := positiveMetric(component["total_bytes"])
		available, availableOK := numericMetric(component["available_bytes"])
		used, usedOK := numericMetric(component["used_bytes"])
		return totalOK && availableOK && usedOK && available >= 0 && used >= 0 && available <= total && used <= total
	case "root_filesystem":
		total, totalOK := positiveMetric(component["total_bytes"])
		used, usedOK := numericMetric(component["used_bytes"])
		free, freeOK := numericMetric(component["free_bytes"])
		return firstString(component, "mount") == "/" && totalOK && usedOK && freeOK && used >= 0 && free >= 0 && used <= total && free <= total
	default:
		return false
	}
}

func numericMetric(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case uint64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func positiveMetric(value any) (float64, bool) {
	number, ok := numericMetric(value)
	return number, ok && number > 0
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
