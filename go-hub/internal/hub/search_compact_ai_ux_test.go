package hub

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchCompactDefaultHasExecutablePathsAndBoundedCoverage(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	agent := Agent{AgentID: "shell:fixture", Name: "Fixture", Status: "online", Kind: "shell", Meta: map[string]any{"approved": true}}
	s.mu.Lock()
	s.agents[agent.AgentID] = &agent
	s.mu.Unlock()

	output := s.searchToolsForRequest(r, map[string]any{"query": "shell_exec", "target": "shell:fixture", "limit": 3})
	if firstString(output, "status") != "completed" {
		t.Fatalf("search unavailable: %#v", output)
	}
	matches := outputRows(output["results"])
	if len(matches) == 0 {
		t.Fatalf("expected shell tool in search: %#v", output)
	}
	first := mapValue(matches[0])
	if firstString(first, "path") != `tools["shell:fixture"]["shell_exec"]` {
		t.Fatalf("path must identify exact target: %#v", first)
	}
	if _, has := first["inputSchema"]; has {
		t.Fatalf("compact default embedded schema: %#v", first)
	}
	if _, has := first["schema_ref"]; !has {
		t.Fatal("compact result has no schema lookup")
	}
	coverage := mapValue(output["coverage"])
	if _, has := coverage["failure_reasons"]; has {
		t.Fatal("compact coverage leaked unrelated failures")
	}
	if _, has := coverage["unreachable"]; has {
		t.Fatal("compact coverage listed unrelated servers")
	}
	if _, has := coverage["complete"]; !has {
		t.Fatal("compact coverage concealed incompleteness")
	}
	bytes, _ := json.Marshal(output)
	if len(bytes) > 8192 {
		t.Fatalf("compact search response exceeds budget: %d bytes", len(bytes))
	}
	full := s.searchToolsForRequest(r, map[string]any{"query": "shell_exec", "target": "shell:fixture", "detail": "full", "limit": 3})
	detailed := mapValue(outputRows(full["results"])[0])
	if _, has := detailed["inputSchema"]; !has {
		t.Fatal("detail=full lost original schema")
	}
	if _, has := detailed["call"]; !has {
		t.Fatal("detail=full lost legacy call example")
	}
}

func TestSearchRejectsUnsupportedDetail(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	got := s.searchToolsForRequest(r, map[string]any{"query": "files", "detail": "verbose-secret"})
	if firstString(got, "status") != "failed" || !strings.Contains(firstString(got, "error"), "detail") {
		t.Fatalf("invalid detail should be refused: %#v", got)
	}
}
