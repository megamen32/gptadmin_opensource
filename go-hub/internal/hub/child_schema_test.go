package hub

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func childSchemaFixture(t *testing.T, dir string) (*Server, Agent) {
	t.Helper()
	s := New(Config{ConfigDir: dir, CtlToken: "fixture-owner", DefaultTimeout: time.Millisecond})
	t.Cleanup(func() { _ = s.Close() })
	parent := Agent{AgentID: "shell:fixture", Kind: "virtual_shell", Status: "online", Meta: map[string]any{
		"approved":   true,
		"mcp_agents": []any{map[string]any{"ref": "fixture-child", "enabled": true, "transport": "streamable-http"}},
	}}
	s.mu.Lock()
	s.agents[parent.AgentID] = &parent
	if err := s.saveRegistryStateLocked(); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	return s, childMCPAgents(parent)[0]
}

func finishChildSchema(t *testing.T, s *Server, id string, result any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.shellJobs[id]
	if j == nil {
		t.Fatal("schema was not queued to parent shell")
	}
	j.Status, j.StartedAt, j.DoneAt, j.Result = "completed", nowFloat(), nowFloat(), result
	if err := s.saveTaskStateLocked(id); err != nil {
		t.Fatal(err)
	}
}

func TestChildSchemaNativeDispatchUsesParentAndOneID(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	r := httptest.NewRequest("POST", "/mcp", nil)
	response := mapValue(s.appsSDKSchemaForRequest(r, map[string]any{"target": child.AgentID}))
	id := firstString(response, "job_id")
	if id == "" || response["owner_target"] != "shell:fixture" {
		t.Fatalf("missing explicit owner receipt: %v", response)
	}
	s.mu.Lock()
	job := s.shellJobs[id]
	valid := len(s.shellJobs) == 1 && len(s.relayJobs) == 1 && len(s.relayQueues[child.AgentID]) == 0 &&
		len(s.shellQueues["fixture"]) == 1 && s.shellQueues["fixture"][0] == id && job.ToolName == "mcp_tools" && job.Arguments["ref"] == "fixture-child"
	s.mu.Unlock()
	if !valid {
		t.Fatal("native schema must queue only its same-ID parent adapter")
	}
	finishChildSchema(t, s, id, map[string]any{"ref": "fixture-child", "tools": []map[string]any{{"name": "upstream_real_name", "inputSchema": map[string]any{"type": "object"}}}})
	first, found := s.childSchemaJobForRequest(r, id)
	if !found || first["status"] != "completed" || firstString(mapValue(first["response"]), "error") != "" {
		t.Fatalf("schema did not complete: %v", first)
	}
	second, _ := s.childSchemaJobForRequest(r, id)
	if !reflect.DeepEqual(first, second) || len(s.shellJobs) != 1 {
		t.Fatal("receipt read resubmitted or changed schema")
	}
}

func TestChildSchemaRecoversExistingUndeliveredID(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	id := s.enqueueRelay(child.AgentID, "tools/list", map[string]any{})
	r := httptest.NewRequest("POST", "/mcp", nil)
	response, found := s.childSchemaJobForRequest(r, id)
	if !found || response["job_id"] != id || s.shellJobs[id] == nil || len(s.relayJobs) != 1 || len(s.shellJobs) != 1 {
		t.Fatalf("existing ID not recovered: %v", response)
	}
	if len(s.shellQueues["fixture"]) != 1 {
		t.Fatal("missing parent dispatch")
	}
	_, _ = s.childSchemaJobForRequest(r, id)
	if len(s.shellQueues["fixture"]) != 1 {
		t.Fatal("repeated read duplicated dispatch")
	}
}

func TestChildSchemaNeverRecoversBusinessCallOrStartedRead(t *testing.T) {
	for _, method := range []string{"tools/call", "tools/list"} {
		t.Run(method, func(t *testing.T) {
			s, child := childSchemaFixture(t, "")
			id := s.enqueueRelay(child.AgentID, method, map[string]any{"name": "business_action"})
			if method == "tools/list" {
				s.relayJobs[id].StartedAt = nowFloat()
			}
			_, _ = s.childSchemaJobForRequest(httptest.NewRequest("POST", "/mcp", nil), id)
			if len(s.shellJobs) != 0 {
				t.Fatal("already delivered or business request replayed")
			}
		})
	}
}

func TestChildSchemaChildOnlyProfileAndExactHint(t *testing.T) {
	s, child := childSchemaFixture(t, "")
	id := s.enqueueRelay(child.AgentID, "tools/list", nil)
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), AccessProfile{ID: "child-only", AccessMode: "full", AllowedTargets: []string{child.AgentID}, AllowedTools: []string{"schema", "job", "allowed_tool"}})
	response, _ := s.childSchemaJobForRequest(r, id)
	if response["status"] != "queued" {
		t.Fatalf("child-only access requires unrelated parent grant: %v", response)
	}
	for _, hint := range []string{child.AgentID, "shell:fixture"} {
		if recognized, allowed := s.childSchemaHintAllowed(r, id, hint); !recognized || !allowed {
			t.Fatalf("exact receipt hint denied: %s", hint)
		}
	}
	if _, allowed := s.childSchemaHintAllowed(r, id, "shell:foreign"); allowed {
		t.Fatal("foreign owner hint accepted")
	}
	denied := requestWithAccessProfile(r, AccessProfile{AllowedTargets: []string{"shell:fixture"}})
	if _, allowed := s.childSchemaHintAllowed(denied, id, "shell:fixture"); allowed {
		t.Fatal("parent grant bypassed child profile")
	}
	finishChildSchema(t, s, id, map[string]any{"ref": "fixture-child", "tools": []any{map[string]any{"name": "allowed_tool"}, map[string]any{"name": "private_tool"}}})
	response, _ = s.childSchemaJobForRequest(r, id)
	tools := mapValue(response["response"])["tools"].([]map[string]any)
	if len(tools) != 1 || tools[0]["name"] != "allowed_tool" {
		t.Fatalf("profile tool filter lost: %v", tools)
	}
}

func TestChildSchemaInvalidUpstreamIsFailure(t *testing.T) {
	for _, result := range []any{map[string]any{"ref": "fixture-child", "error": "upstream down"}, map[string]any{"ref": "fixture-child", "tools": "invalid"}} {
		s, child := childSchemaFixture(t, "")
		id := s.enqueueRelay(child.AgentID, "tools/list", nil)
		r := httptest.NewRequest("POST", "/mcp", nil)
		_, _ = s.childSchemaJobForRequest(r, id)
		finishChildSchema(t, s, id, result)
		response, _ := s.childSchemaJobForRequest(r, id)
		if response["status"] != "failed" {
			t.Fatalf("invalid upstream became successful empty schema: %v", response)
		}
	}
}

func TestChildSchemaPreservesCompletedReceiptAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s, child := childSchemaFixture(t, dir)
	id := s.enqueueRelay(child.AgentID, "tools/list", nil)
	r := httptest.NewRequest("POST", "/mcp", nil)
	_, _ = s.childSchemaJobForRequest(r, id)
	finishChildSchema(t, s, id, map[string]any{"ref": "fixture-child", "tools": []any{map[string]any{"name": "real_name"}}})
	before, _ := s.childSchemaJobForRequest(r, id)
	_ = s.Close()
	restored, _ := childSchemaFixture(t, dir)
	after, _ := restored.childSchemaJobForRequest(r, id)
	if !reflect.DeepEqual(before, after) || len(restored.shellQueues["fixture"]) != 0 {
		t.Fatalf("completed schema replayed or lost after restart: %v / %v", before, after)
	}
}

func TestChildSchemaRecoversUndeliveredIDAfterCreatorExit(t *testing.T) {
	dir := t.TempDir()
	s, child := childSchemaFixture(t, dir)
	id := s.enqueueRelay(child.AgentID, "tools/list", nil)
	_ = s.Close()
	restored, _ := childSchemaFixture(t, dir)
	response, found := restored.childSchemaJobForRequest(httptest.NewRequest("POST", "/mcp", nil), id)
	if !found || response["job_id"] != id || restored.shellJobs[id] == nil || len(restored.shellJobs) != 1 {
		t.Fatalf("undelivered original schema ID lost after creator exit: %v", response)
	}
}

func TestChildSchemaAdmissionDeniesBeforeQueue(t *testing.T) {
	s, child := childSchemaFixture(t, "")
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), AccessProfile{AllowedTargets: []string{"shell:fixture"}})
	_ = s.childSchemaForRequest(r, child)
	if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 || len(s.shellQueues["fixture"]) != 0 {
		t.Fatal("denied child queued before authorization")
	}
	s.agents["shell:fixture"].Meta["approved"] = false
	_ = s.childSchemaForRequest(httptest.NewRequest("POST", "/mcp", nil), child)
	if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
		t.Fatal("unapproved executor received schema")
	}
}

func TestChildSchemaHTTPJobProjectsSameReceipt(t *testing.T) {
	s, child := childSchemaFixture(t, "")
	id := s.enqueueRelay(child.AgentID, "tools/list", nil)
	r := httptest.NewRequest("GET", "/mcp-relay/job/"+id+"?detail=full", nil)
	_, _ = s.childSchemaJobForRequest(r, id)
	finishChildSchema(t, s, id, map[string]any{"ref": "fixture-child", "tools": []any{map[string]any{"name": "real_name"}}})
	w := httptest.NewRecorder()
	s.mcpRelayJob(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "real_name") || !strings.Contains(w.Body.String(), `"owner_target":"shell:fixture"`) {
		t.Fatalf("HTTP schema receipt lost: %d %s", w.Code, w.Body.String())
	}
}
