package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdminServerResourcesDoesNotProbeStaleNode(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := New(Config{CtlToken: "fixture-owner", PublicOrigin: "https://hub.example", Now: func() time.Time { return now }})
	defer s.Close()
	lastSeen := float64(now.Add(-time.Hour).Unix())
	s.agents["shell:stale-node"] = &Agent{AgentID: "shell:stale-node", Kind: "virtual_shell", Status: "stale", LastSeen: lastSeen}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/servers/resources", strings.NewReader(`{"server_id":"shell:stale-node"}`))
	req.Header.Set("Authorization", "Bearer fixture-owner")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "unavailable" || result["sampled_at"] != nil || result["node_status"] != "stale" || result["error"] != "node_not_online" || result["last_seen"] != lastSeen {
		t.Fatalf("stale node result must preserve liveness without claiming a sample: %#v", result)
	}
	for _, key := range []string{"cpu", "memory", "root_filesystem"} {
		component := mapValue(result[key])
		if component["status"] != "unavailable" || component["error"] != "node_not_online" || component["total_bytes"] != nil {
			t.Errorf("%s must be explicitly unavailable without metric values: %#v", key, component)
		}
	}
	if len(s.shellJobs) != 0 {
		t.Fatalf("stale node was probed and queued a task: %#v", s.shellJobs)
	}
}

func TestAdminJobsServerFilterIsExactBeforeLimit(t *testing.T) {
	s := New(Config{CtlToken: "fixture-owner", ConfigDir: t.TempDir()})
	defer s.Close()
	now := nowFloat()
	s.shellJobs["node-a-old"] = &shellJob{ID: "node-a-old", Server: "node-a", ToolName: "system_info", CreatedAt: now - 20, DoneAt: now - 19, Status: "completed", Result: map[string]any{"host": "node-a", "old": true}}
	s.shellJobs["node-a-new"] = &shellJob{ID: "node-a-new", Server: "node-a", ToolName: "system_resource_summary", CreatedAt: now - 10, DoneAt: now - 9, Status: "failed", Error: "sample failed"}
	s.shellJobs["node-ab-newer"] = &shellJob{ID: "node-ab-newer", Server: "node-ab", ToolName: "shell_exec", CreatedAt: now - 2, DoneAt: now - 1, Status: "completed", Result: "must not match node-a"}
	if err := s.saveTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/api/jobs?server_id=shell%3Anode-a&limit=1", nil)
	req.Header.Set("Authorization", "Bearer fixture-owner")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	recent := sliceValue(result["recent"])
	if result["count"] != float64(2) || len(recent) != 1 {
		t.Fatalf("exact filter/count/limit mismatch: %#v", result)
	}
	job := mapValue(recent[0])
	if job["job_id"] != "node-a-new" || job["server_id"] != "shell:node-a" || job["status"] != "failed" || job["error_preview"] != "sample failed" {
		t.Fatalf("expected newest exact-node task with persisted error preview: %#v", job)
	}
}

func TestAdminServerResourceMutationRequiresSameOriginForAdminCookie(t *testing.T) {
	expected := "https://hub.example"
	for _, test := range []struct {
		name   string
		origin string
		ref    string
		want   bool
	}{
		{name: "same origin", origin: expected, want: true},
		{name: "referer fallback", ref: expected + "/admin/", want: true},
		{name: "foreign origin", origin: "https://attacker.example", want: false},
		{name: "missing origin", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/admin/api/servers/resources", nil)
			req.Header.Set("Origin", test.origin)
			req.Header.Set("Referer", test.ref)
			if got := sameOriginAdminMutation(req, expected); got != test.want {
				t.Fatalf("same-origin=%v want %v", got, test.want)
			}
		})
	}
}

func TestAdminServerResourcesRejectsCrossOriginCookiePost(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := New(Config{AdminPassword: "fixture-password", CtlToken: "fixture-owner", PublicOrigin: "https://hub.example", Now: func() time.Time { return now }})
	defer s.Close()
	req := httptest.NewRequest(http.MethodPost, "https://hub.example/admin/api/servers/resources", strings.NewReader(`{"server_id":"shell:node"}`))
	req.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: s.signAdminSession(now.Add(time.Hour))})
	req.Header.Set("Authorization", "Bearer fixture-owner")
	req.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "same-origin") {
		t.Fatalf("cross-origin cookie POST status=%d body=%s", response.Code, response.Body.String())
	}
	if len(s.shellJobs) != 0 {
		t.Fatalf("cross-origin cookie POST submitted a metrics task: %#v", s.shellJobs)
	}
}

func TestAdminServerResourcesAcceptsSameOriginCookiePostThroughIngressAlias(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := New(Config{AdminPassword: "fixture-password", CtlToken: "fixture-owner", PublicOrigin: "https://hub.example", Now: func() time.Time { return now }})
	defer s.Close()
	s.agents["shell:node"] = &Agent{AgentID: "shell:node", Kind: "virtual_shell", Status: "online", LastSeen: float64(now.Unix())}

	const ingressOrigin = "https://your-subdomain.t.became.bezrabotnyi.com"
	req := httptest.NewRequest(http.MethodPost, ingressOrigin+"/admin/api/servers/resources", strings.NewReader(`{"server_id":"shell:node"}`))
	req.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: s.signAdminSession(now.Add(time.Hour))})
	// The canonical VUSA ingress preserves the external Host and sets the
	// forwarded scheme. Browser fetch omits Origin here, so Referer is the
	// same-origin fallback used by the handler.
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Referer", ingressOrigin+"/admin/")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, req)
	if response.Code == http.StatusForbidden && strings.Contains(response.Body.String(), "same-origin") {
		t.Fatalf("same-origin ingress alias rejected: status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Code != http.StatusOK {
		t.Fatalf("same-origin ingress alias did not pass origin guard: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAdminServerResourcesRejectsCrossOriginCookiePostThroughIngressAlias(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := New(Config{AdminPassword: "fixture-password", CtlToken: "fixture-owner", PublicOrigin: "https://hub.example", Now: func() time.Time { return now }})
	defer s.Close()
	s.agents["shell:node"] = &Agent{AgentID: "shell:node", Kind: "virtual_shell", Status: "online", LastSeen: float64(now.Unix())}

	const ingressOrigin = "https://your-subdomain.t.became.bezrabotnyi.com"
	req := httptest.NewRequest(http.MethodPost, ingressOrigin+"/admin/api/servers/resources", strings.NewReader(`{"server_id":"shell:node"}`))
	req.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: s.signAdminSession(now.Add(time.Hour))})
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "same-origin") {
		t.Fatalf("foreign-origin ingress alias status=%d body=%s", response.Code, response.Body.String())
	}
	if len(s.shellJobs) != 0 {
		t.Fatalf("foreign-origin cookie POST submitted a metrics task: %#v", s.shellJobs)
	}
}

func TestAdminRequestOriginUsesAdmittedHostAndRejectsUntrustedForwardedHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://ingress.example/admin/api/servers/resources", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "attacker.example")
	if got := adminRequestOrigin(req); got != "https://ingress.example" {
		t.Fatalf("request origin=%q; wanted admitted Host, ignoring X-Forwarded-Host", got)
	}
	req.Header.Add("X-Forwarded-Proto", "http")
	if got := adminRequestOrigin(req); got != "" {
		t.Fatalf("conflicting repeated forwarded proto accepted: %q", got)
	}
	req.Header.Del("X-Forwarded-Proto")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Add("X-Forwarded-Proto", "https")
	if got := adminRequestOrigin(req); got != "" {
		t.Fatalf("duplicate identical forwarded proto accepted: %q", got)
	}
	req.Header.Del("X-Forwarded-Proto")
	req.Header.Set("X-Forwarded-Proto", "https")

	req.Header.Set("Origin", "https://attacker.example")
	if sameOriginAdminMutation(req, adminRequestOrigin(req)) {
		t.Fatal("foreign origin matched when X-Forwarded-Host was supplied")
	}

	req.Header.Del("Origin")
	req.Header.Del("Referer")
	if sameOriginAdminMutation(req, adminRequestOrigin(req)) {
		t.Fatal("request without Origin or Referer did not fail closed")
	}
	req.Header.Set("Referer", "https://ingress.example/admin/")
	req.Header.Set("X-Forwarded-Proto", "https,http")
	if got := adminRequestOrigin(req); got != "" {
		t.Fatalf("malformed forwarded proto accepted: %q", got)
	}
}

func TestValidResourceSummaryRejectsMalformedAvailableComponents(t *testing.T) {
	valid := map[string]any{
		"sampled_at": "2026-10-23T00:00:00Z", "status": "available",
		"cpu":             map[string]any{"status": "available", "cores": float64(4), "utilization_percent": float64(25), "sample_interval_ms": float64(150)},
		"memory":          map[string]any{"status": "available", "total_bytes": float64(1000), "available_bytes": float64(600), "used_bytes": float64(400)},
		"root_filesystem": map[string]any{"status": "available", "mount": "/", "total_bytes": float64(5000), "used_bytes": float64(3000), "free_bytes": float64(1500)},
	}
	if !validResourceSummary(valid) {
		t.Fatal("valid typed resource sample was rejected")
	}
	malformed := cloneMap(valid)
	malformed["cpu"] = map[string]any{"status": "available"}
	if validResourceSummary(malformed) {
		t.Fatal("malformed available CPU sample was accepted")
	}
}

func TestSystemResourceSummaryIsAllowedByReadOnlyExecutionCeiling(t *testing.T) {
	r := requestWithReadOnlyExec(httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if !readOnlyExecToolAllowed(r, "shell:node", systemResourceSummaryTool, map[string]any{}) {
		t.Fatal("typed pathless resource summary was denied to the read-only executor")
	}
	if readOnlyExecToolAllowed(r, "shell:node", "shell_exec", map[string]any{"cmd": "true"}) {
		t.Fatal("read-only executor gained shell command access")
	}
}
