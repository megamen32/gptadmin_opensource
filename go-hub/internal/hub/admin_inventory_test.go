package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAdminOverviewContainsFullMetadataAndUsefulSortedJobs(t *testing.T) {
	s := New(Config{CtlToken: "fixture-owner"})
	defer s.Close()
	s.agents["shell:fixture"] = &Agent{AgentID: "shell:fixture", Name: "fixture", Kind: "virtual_shell", Status: "online", Capabilities: []string{"shell", "tasks"}, Meta: map[string]any{"build_version": "193"}}
	s.relayJobs["old"] = &relayJob{ID: "old", AgentID: "fixture", CreatedAt: 1, DoneAt: 2, Status: "completed", Method: "tools/list", Result: map[string]any{"tools": []string{"inspect"}}}
	s.relayJobs["new"] = &relayJob{ID: "new", AgentID: "fixture", CreatedAt: 10, DoneAt: 12, Status: "failed", Method: "tools/call", Params: map[string]any{"name": "inspect", "arguments": map[string]any{"path": "/work"}}, Error: map[string]any{"message": "fixture failure"}}
	w := sharedAccessCall(t, s, "fixture-owner", "GET", "/admin/api/overview", nil)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, raw := range body["servers"].([]any) {
		row := mapValue(raw)
		if row["server_id"] == "shell:fixture" {
			found = true
			if len(sliceValue(row["capabilities"])) != 2 || mapValue(row["meta"])["build_version"] != "193" {
				t.Errorf("admin receives compact server metadata: %v", row)
			}
		}
	}
	if !found {
		t.Fatal("server missing")
	}
	recent := mapValue(body["jobs"])["recent"].([]any)
	if len(recent) != 2 {
		t.Fatal("jobs missing")
	}
	first := mapValue(recent[0])
	if first["job_id"] != "new" || first["tool_name"] != "inspect" || !strings.Contains(firstString(first, "error_preview"), "fixture failure") {
		t.Errorf("recent job lacks sorted detail: %v", first)
	}
	if !strings.Contains(firstString(mapValue(recent[1]), "result_preview"), "inspect") {
		t.Error("completed result preview missing")
	}
}

func TestAdminJobInventoryPagesWithoutDroppingHistory(t *testing.T) {
	s := New(Config{CtlToken: "fixture-owner"})
	defer s.Close()
	for i := 0; i < 240; i++ {
		id := fmt.Sprintf("task-%03d", i)
		s.shellJobs[id] = &shellJob{ID: id, CreatedAt: float64(i), Status: "completed", Result: "retained"}
	}
	first := s.adminJobsDataLocked()
	if len(first["recent"].([]map[string]any)) != 200 || first["count"] != 240 {
		t.Fatal("incorrect bounded overview")
	}
	w := sharedAccessCall(t, s, "fixture-owner", "GET", "/admin/api/jobs?offset=200&limit=100", nil)
	var page map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page["recent"].([]any)) != 40 || page["recent_truncated"] != false {
		t.Fatalf("older history unavailable: %d %v", w.Code, page)
	}
}
