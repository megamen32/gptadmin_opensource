package hub

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Fast unit: preserve GrepMesh business data before preview. Expected 1s,
// emergency 10s. Fixtures prove presentation logic, not child integration.
func grepTextResult(value any) map[string]any {
	encoded, _ := json.Marshal(value)
	return map[string]any{"status": "completed", "job_id": "unit-job", "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(encoded)}}}}
}

func TestTokenGrepMeshReadableLinesAndFullDisclosure(t *testing.T) {
	s := New(Config{})
	lines := []any{}
	for i := 1; i <= 8; i++ {
		lines = append(lines, map[string]any{"line_number": i, "text": fmt.Sprintf("строка %d", i)})
	}
	data := map[string]any{"host_id": "node-100", "path": "/example", "partial": false, "truncated": false, "origin_host": "node-44", "host_status": "degraded", "artifact_id": "read-artifact", "chunks": []any{map[string]any{"start_line": 1, "end_line": 8, "lines": lines}}}
	raw := grepTextResult(data)
	args := map[string]any{"target": "mcp:shell:100:GrepMesh", "tool": "read_text"}
	out := mapValue(s.presentTokenOutput(nil, "execute", args, raw))
	result := mapValue(out["result"])
	if result["path"] != "/example" || result["host"] != "node-100" || !strings.Contains(firstString(result, "text"), "8: строка 8") || out["preview"] != nil {
		t.Fatalf("readable data lost: %#v", out)
	}
	for _, key := range []string{"host_status", "origin_host", "artifact_id"} {
		if !reflect.DeepEqual(result[key], data[key]) {
			t.Fatalf("source status or retrieval handle lost: %s", key)
		}
	}
	read := s.expandOperation(nil, map[string]any{"id": out["operation_id"], "limit": 16000})
	a, _ := json.Marshal(read["data"])
	b, _ := json.Marshal(raw)
	if string(a) != string(b) {
		t.Fatal("disclosure did not retain the original child receipt")
	}
	lines = append(lines, map[string]any{"line_number": 9, "text": strings.Repeat("Привет", 300)})
	data["chunks"] = []any{map[string]any{"start_line": 1, "end_line": 9, "lines": lines}}
	out = mapValue(s.presentTokenOutput(nil, "execute", args, grepTextResult(data)))
	result = mapValue(out["result"])
	if result["truncated"] != true || out["operation_id"] == nil || out["preview"] != nil || len(firstString(result, "text")) > compactResultBytes {
		t.Fatalf("unbounded or unreadable preview: %#v", out)
	}
}

func TestTokenGrepMeshDecodePreservesUnknownFieldsAndFailures(t *testing.T) {
	for _, value := range []map[string]any{
		{"number": json.Number("9007199254740993"), "partial": true, "truncated": true},
		{"status": "failed", "error": "upstream failure"},
		{"type": "resource", "uri": "unit-only"},
	} {
		out, _ := compactGrepMeshOutput(grepTextResult(value), "mcp:shell:100:GrepMesh", "read_text")
		a, _ := json.Marshal(out["result"])
		b, _ := json.Marshal(value)
		if string(a) != string(b) {
			t.Fatalf("semantic fields lost: %s != %s", a, b)
		}
	}
	raw := grepTextResult(map[string]any{"value": 1})
	block := mapValue(outputRows(mapValue(raw["result"])["content"])[0])
	for _, extra := range []string{" trailing information", " {\"second\":true}"} {
		block["text"] = `{"value":1}` + extra
		out, _ := compactGrepMeshOutput(raw, "mcp:shell:100:GrepMesh", "read_text")
		if !reflect.DeepEqual(out, raw) {
			t.Fatal("trailing business text discarded")
		}
	}
	block["text"] = `{"value":1}`
	block["annotations"] = map[string]any{"audience": []string{"user"}}
	out, _ := compactGrepMeshOutput(raw, "mcp:shell:100:GrepMesh", "read_text")
	if !reflect.DeepEqual(out, raw) {
		t.Fatal("content annotations discarded")
	}
}

func TestTokenNestedExpandIsNotPreviewedAgain(t *testing.T) {
	s := New(Config{})
	raw := map[string]any{"server_id": "hub", "status": "completed", "response": map[string]any{"data": strings.Repeat("x", 1800)}}
	out := mapValue(s.presentTokenOutput(nil, "execute", map[string]any{"target": "hub", "tool": "expand"}, raw))
	if !reflect.DeepEqual(out, raw) {
		t.Fatal("bounded disclosure was previewed a second time")
	}
}

func TestTokenHTTPDiscoveryFiltersAndCompactContract(t *testing.T) {
	s := New(Config{})
	s.agents["shell:unit-test"] = &Agent{AgentID: "shell:unit-test", Kind: "shell", Status: "online"}
	s.agents["shell:other"] = &Agent{AgentID: "shell:other", Kind: "shell", Status: "stale"}
	w := httptest.NewRecorder()
	s.mcpRelayServers(w, httptest.NewRequest("GET", "/mcp-relay/servers?query=unit-test&status=online", nil))
	var result map[string]any
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(outputRows(result["servers"])) != 1 {
		t.Fatalf("HTTP filters ignored: %s", w.Body.String())
	}
	if firstString(mapValue(outputRows(result["servers"])[0]), "server_id") != "shell:unit-test" {
		t.Fatal("wrong filtered target")
	}
}

func TestTokenQueuedChildJobUsesSameReadablePresentation(t *testing.T) {
	s := New(Config{})
	data := map[string]any{"host_id": "node-100", "path": "/example", "partial": false, "chunks": []any{map[string]any{"start_line": 1, "end_line": 1, "lines": []any{map[string]any{"line_number": 1, "text": "содержательный результат"}}}}}
	upstream := grepTextResult(data)["result"]
	wrapped := map[string]any{"resultType": "complete", "content": []any{map[string]any{"type": "text", "text": "Child MCP tool completed"}}, "structuredContent": map[string]any{"ref": "GrepMesh", "name": "read_text", "result": upstream}}
	j := &shellJob{ID: "unit-child", Server: "100", ToolName: "mcp_call", Arguments: map[string]any{"ref": "GrepMesh", "name": "read_text"}, Status: "completed", Result: wrapped}
	s.shellJobs[j.ID] = j
	raw := shellJobResponse(j)
	out := mapValue(s.presentTokenOutput(nil, "job", map[string]any{"id": j.ID}, raw))
	if firstString(mapValue(out["result"]), "text") != "1: содержательный результат" || out["preview"] != nil || out["operation_id"] == nil {
		t.Fatalf("job readability lost: %#v", out)
	}
	read := s.expandOperation(nil, map[string]any{"id": out["operation_id"]})
	if !reflect.DeepEqual(mapValue(read["data"])["response"], jsonRoundTrip(raw["response"])) {
		t.Fatal("job disclosure lost its original receipt")
	}
}

func jsonRoundTrip(value any) any {
	encoded, _ := json.Marshal(value)
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var result any
	_ = decoder.Decode(&result)
	return result
}

func TestTokenCachedExecuteCanReadExactSchemaAndFilterDiscovery(t *testing.T) {
	s := New(Config{})
	s.agents["shell:unit"] = &Agent{AgentID: "shell:unit", Kind: "shell", Status: "online"}
	raw, status := s.callHubToolForRequest(nil, "schema", map[string]any{"target": "shell:unit", "tool": "shell_exec"})
	if status != 200 || len(outputRows(mapValue(raw["response"])["tools"])) != 1 || raw["call_ref"] == nil {
		t.Fatalf("cached execute has no exact schema: %#v", raw)
	}
	raw, status = s.callHubToolForRequest(nil, "discover", map[string]any{"query": "unit", "status": "online"})
	if status != 200 || len(outputRows(raw["servers"])) != 1 {
		t.Fatalf("cached execute discovery unfiltered: %#v", raw)
	}
}
