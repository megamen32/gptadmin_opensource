package hub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTypedSchemaInspectNeverQueuesAndPreservesSurface(t *testing.T) {
	for _, tc := range []struct{ target, tool string }{
		{"control:fixture", "mcp_manage"}, {"file:fixture", "file_editor"}, {"control:hub", "settings_get"},
	} {
		for _, allowed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/allowed=%t", tc.target, allowed), func(t *testing.T) {
				s, r := codeModeFixture(t)
				t.Cleanup(func() { _ = s.Close() })
				s.cfg.DefaultTimeout = 20 * time.Millisecond
				s.mu.Lock()
				s.agents["shell:fixture"] = &Agent{AgentID: "shell:fixture", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true, "build_version": 1110}}
				s.mu.Unlock()
				targets := []string{"hub", tc.target}
				if !allowed {
					targets = []string{"hub", "shell:fixture"}
				}
				r = requestWithAccessProfile(r, AccessProfile{ID: "typed-schema", AccessMode: "full", AllowedTargets: targets, AllowedTools: []string{"exec", "schema", tc.tool}})
				got, status := codeModeRun(t, s, r, map[string]any{"code": fmt.Sprintf("return mcp.inspect(%q,%q);", tc.target, tc.tool)})
				if allowed && (status != http.StatusOK || firstString(mapValue(got["result"]), "tool") != tc.tool || mapValue(got["result"])["inputSchema"] == nil) {
					t.Errorf("typed schema unavailable: HTTP%d %#v", status, got)
				}
				if !allowed && status == http.StatusOK {
					t.Errorf("shell-only caller saw typed schema: %#v", got)
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.relayJobs) != 0 || len(s.shellJobs) != 0 || len(s.relayQueues) != 0 || len(s.shellQueues) != 0 {
					t.Errorf("schema allocated work: relay=%d shell=%d", len(s.relayJobs), len(s.shellJobs))
				}
			})
		}
	}
}

func TestTypedSchemaHTTPNeverQueuesAndFiltersCaller(t *testing.T) {
	for _, target := range []string{"control:fixture", "file:fixture", "control:hub"} {
		t.Run(target, func(t *testing.T) {
			s, _ := codeModeFixture(t)
			t.Cleanup(func() { _ = s.Close() })
			s.mu.Lock()
			s.agents["shell:fixture"] = &Agent{AgentID: "shell:fixture", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true, "build_version": 1110}}
			s.mu.Unlock()
			tool := "mcp_manage"
			if target == "file:fixture" {
				tool = "file_editor"
			}
			if target == "control:hub" {
				tool = "settings_get"
			}
			r := httptest.NewRequest(http.MethodPost, "/mcp-relay/tools", strings.NewReader(fmt.Sprintf(`{"target":%q,"background":true,"detail":"full"}`, target)))
			r = requestWithAccessProfile(r, AccessProfile{ID: "only-one-tool", AccessMode: "full", AllowedTargets: []string{"hub", target}, AllowedTools: []string{"schema", tool}})
			w := httptest.NewRecorder()
			s.mcpRelayTools(w, r)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"completed"`) || !strings.Contains(w.Body.String(), `"name":"`+tool+`"`) || strings.Contains(w.Body.String(), `"job_id"`) || strings.Contains(w.Body.String(), `"name":"shell_exec"`) {
				t.Errorf("bad typed catalog: HTTP%d %s", w.Code, w.Body.String())
			}
			if len(s.relayJobs) != 0 || len(s.shellJobs) != 0 {
				t.Fatal("HTTP schema queued work")
			}
		})
	}
}

func TestTypedSchemaReadonlyControlUsesCallerFilteredCatalog(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	s.mu.Lock()
	s.agents["shell:fixture"] = &Agent{AgentID: "shell:fixture", Kind: "virtual_shell", Meta: map[string]any{"approved": true}}
	s.mu.Unlock()
	r = requestWithAccessProfile(r, AccessProfile{ID: "schema-reader", AccessMode: "readonly", AllowedTargets: []string{"hub", "control:fixture"}, AllowedTools: []string{"schema", "control_catalog"}})
	got := mapValue(s.appsSDKCallRawForRequest(r, "schema", map[string]any{"target": "control:fixture"}))
	rows := outputRows(mapValue(got["response"])["tools"])
	if firstString(got, "status") != "completed" || len(rows) != 1 || firstString(mapValue(rows[0]), "name") != "control_catalog" {
		t.Fatalf("readonly typed catalog lost caller filter: %#v", got)
	}
	if len(s.relayJobs) != 0 || len(s.shellJobs) != 0 {
		t.Fatal("readonly schema queued work")
	}
}
