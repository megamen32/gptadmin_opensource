package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Fast unit: exclude diagnostics from discovery. Expected <1s; max 10s.
func TestTokenCompactDiscoveryExcludesMesh(t *testing.T) {
	s := New(Config{})
	out := mapValue(s.appsSDKCallForRequest(nil, "discover", map[string]any{}))
	if out["mesh"] != nil {
		t.Fatal("compact discovery still includes mesh diagnostics")
	}
}

// Fast unit: exact constraints, disclosure integrity/ownership, UTF-8 pagination,
// bounded storage and no silent hiding of failures/media. Expected 1s; max 15s.
func TestTokenSchemaExactAndDisclosure(t *testing.T) {
	s := New(Config{})
	s.agents["shell:test"] = &Agent{AgentID: "shell:test", Kind: "shell", Status: "online"}
	args := map[string]any{"target": "shell:test", "tool": "shell_exec"}
	full := mapValue(s.appsSDKCallForRequest(nil, "schema", map[string]any{"target": "shell:test", "detail": "full"}))
	selected := mapValue(s.appsSDKCallForRequest(nil, "schema", args))
	defs := outputRows(mapValue(selected["response"])["tools"])
	if len(defs) != 1 {
		t.Fatalf("selected %d tools", len(defs))
	}
	var expected any
	for _, d := range outputRows(mapValue(full["response"])["tools"]) {
		if firstString(mapValue(d), "name") == "shell_exec" {
			expected = d
		}
	}
	if !reflect.DeepEqual(defs[0], expected) {
		t.Fatal("exact schema changed validation constraints")
	}
	id := firstString(selected, "call_ref")
	if id == "" {
		t.Fatal("missing disclosure handle")
	}
	read := s.expandOperation(nil, map[string]any{"id": id, "path": "/response/tools/0", "limit": 16000})
	a, _ := json.Marshal(read["data"])
	b, _ := json.Marshal(expected)
	if string(a) != string(b) {
		t.Fatalf("disclosure mismatch: %s != %s", a, b)
	}
	catalog := mapValue(s.appsSDKCallForRequest(nil, "schema", map[string]any{"target": "shell:test"}))
	for _, d := range outputRows(mapValue(catalog["response"])["tools"]) {
		if mapValue(d)["inputSchema"] != nil {
			t.Fatal("catalog sends every input schema")
		}
	}
}
func TestTokenDisclosureCallerAndLifetime(t *testing.T) {
	s := New(Config{})
	request := func(subject string) *http.Request {
		return requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil), map[string]any{"sub": subject, "scope": "gptadmin.read"})
	}
	owner, other := request("one"), request("two")
	raw := map[string]any{"value": "до", "number": json.Number("9007199254740993")}
	id := s.rememberOperation(owner, "demo", "hub", raw)
	raw["value"] = "после"
	read := s.expandOperation(owner, map[string]any{"id": id})
	if mapValue(read["data"])["value"] != "до" {
		t.Fatal("result was not immutable")
	}
	if s.expandOperation(other, map[string]any{"id": id})["status"] != "failed" {
		t.Fatal("cross-caller read accepted")
	}
	changed := requestWithAccessProfile(owner, AccessProfile{ID: "new", AllowedTargets: []string{"*"}, AllowedTools: []string{"*"}})
	if s.expandOperation(changed, map[string]any{"id": id})["status"] != "failed" {
		t.Fatal("profile change accepted")
	}
	item := s.operations[id]
	item.Created = time.Now().Add(-operationTTL - time.Second)
	s.operations[id] = item
	if s.expandOperation(owner, map[string]any{"id": id})["status"] != "failed" {
		t.Fatal("expired result accepted")
	}
}
func TestTokenDisclosureUTF8AndBounds(t *testing.T) {
	s := New(Config{})
	id := s.rememberOperation(nil, "demo", "", map[string]any{"текст": "Привет 🌍", "a/b": []any{json.Number("9007199254740993")}})
	out := s.expandOperation(nil, map[string]any{"id": id, "path": "/a~1b/0"})
	if fmt.Sprint(out["data"]) != "9007199254740993" {
		t.Fatalf("lost integer precision: %#v", out)
	}
	var joined string
	offset := 0
	for i := 0; i < 100; i++ {
		page := s.expandOperation(nil, map[string]any{"id": id, "limit": 7, "offset": offset})
		if page["status"] == "failed" {
			t.Fatal(page)
		}
		part, _ := page["text"].(string)
		if !utf8.ValidString(part) {
			t.Fatal("broken UTF8")
		}
		joined += part
		n, ok := page["next_offset"].(int)
		if !ok {
			break
		}
		if n <= offset {
			t.Fatal("pagination stalled")
		}
		offset = n
	}
	if joined != string(s.operations[id].Data) {
		t.Fatal("pages do not reconstruct original")
	}
	for _, args := range []map[string]any{{"id": id, "limit": 0}, {"id": id, "offset": -1}, {"id": id, "offset": 1.5}, {"id": id, "path": "/bad~2"}} {
		if s.expandOperation(nil, args)["status"] != "failed" {
			t.Fatal("invalid page accepted", args)
		}
	}
	for i := 0; i < operationMaxCount+2; i++ {
		s.rememberOperation(nil, "demo", "", map[string]any{"i": i})
	}
	if len(s.operations) != operationMaxCount {
		t.Fatal("unbounded operation count")
	}
	if s.rememberOperation(nil, "demo", "", map[string]any{"large": strings.Repeat("a", operationMaxItem)}) != "" {
		t.Fatal("oversized result stored")
	}
}
func TestTokenDiscoveryFiltersAndFailureMedia(t *testing.T) {
	raw := map[string]any{"mesh": "diagnostics", "servers": []any{map[string]any{"server_id": "shell:100", "name": "Shell: 100", "status": "online"}, map[string]any{"server_id": "mcp:100:Fast", "name": "fast-agent", "status": "stale"}}}
	out := compactDiscovery(raw, map[string]any{"query": "fast", "status": "stale"})
	if len(outputRows(out["servers"])) != 1 {
		t.Fatal("discovery filter failed")
	}
	for _, data := range []any{map[string]any{"isError": true}, map[string]any{"error": "oops"}, map[string]any{"response": map[string]any{"content": []map[string]any{{"type": "image", "data": "abc"}}}}} {
		if !hasMediaOrFailure(data) {
			t.Fatal("failure/media would be hidden")
		}
	}
}

func TestTokenNativeCatalogStillRoutesAllCapabilities(t *testing.T) {
	compact := compactFacadeTools(nil, false)
	full := compactFacadeTools(nil, true)
	if len(compact) != 6 || len(full) != 16 {
		t.Fatalf("catalog sizes %d/%d", len(compact), len(full))
	}
	hasExpand := false
	for _, tool := range hubTools() {
		if tool["name"] == "expand" {
			hasExpand = true
		}
	}
	if !hasExpand {
		t.Fatal("existing execute clients cannot disclose operations")
	}
}

func TestTokenOwnedHubDuplicateJSON(t *testing.T) {
	value := map[string]any{"needed": "Все данные", "count": json.Number("9007199254740993")}
	raw := map[string]any{"server_id": "hub", "status": "completed", "response": mcpToolResult(value)}
	out := compactHubOutput(raw)
	if !reflect.DeepEqual(out["result"], value) || out["response"] != nil {
		t.Fatal("duplicate Hub JSON not collapsed")
	}
	response := mapValue(raw["response"])
	response["content"] = []map[string]any{{"type": "text", "text": "different business content"}}
	if !reflect.DeepEqual(compactHubOutput(raw), raw) {
		t.Fatal("meaningful business text discarded")
	}
}

func TestTokenCallReferenceResolvesBeforeExecution(t *testing.T) {
	s := New(Config{})
	id := s.rememberOperation(nil, "schema", "mcp:shell:100:long-name", map[string]any{"response": map[string]any{"tools": []any{}}}, "search")
	args := map[string]any{"ref": id, "args": map[string]any{"query": "value"}, "idempotency_key": "stable"}
	got, err := s.resolveExecutionReference(nil, args)
	if err != nil || got["target"] != "mcp:shell:100:long-name" || got["tool"] != "search" || got["ref"] != nil || got["idempotency_key"] != "stable" {
		t.Fatalf("bad reference expansion: %v %#v", err, got)
	}
	if args["ref"] != id || args["target"] != nil {
		t.Fatal("input request mutated")
	}
	for _, bad := range []map[string]any{{"ref": id, "target": "hub"}, {"ref": "missing"}, {"ref": id, "tool": "write"}} {
		if _, err := s.resolveExecutionReference(nil, bad); err == nil {
			t.Fatal("invalid reference accepted")
		}
	}
}

func TestTokenExpandedExactSchemaGetsExecutableReference(t *testing.T) {
	s := New(Config{})
	definition := map[string]any{"name": "search_status", "inputSchema": map[string]any{"type": "object"}}
	raw := map[string]any{"response": map[string]any{"tools": []any{definition}}}
	id := s.rememberOperation(nil, "schema", "mcp:shell:100:GrepMesh", raw)
	read := s.expandOperation(nil, map[string]any{"id": id, "path": "/response/tools/0"})
	ref := firstString(read, "call_ref")
	if ref == "" {
		t.Fatal("expanded schema missing callable ref")
	}
	resolved, err := s.resolveExecutionReference(nil, map[string]any{"ref": ref})
	if err != nil || resolved["target"] != "mcp:shell:100:GrepMesh" || resolved["tool"] != "search_status" {
		t.Fatal("wrong callable schema reference", err)
	}
	summary := s.presentTokenOutput(nil, "job", map[string]any{}, map[string]any{"job_id": "finished-schema", "server_id": "mcp:shell:100:GrepMesh", "status": "completed", "response": map[string]any{"tools": []any{definition}}})
	result := mapValue(summary)
	if result["truncated"] != nil || result["operation_id"] == nil {
		t.Fatal("asynchronous schema was treated as business preview", result)
	}
}

func TestTokenOwnedChildEnvelopeIsLossless(t *testing.T) {
	upstream := map[string]any{"content": []map[string]any{{"type": "text", "text": "{\"answer\":42}"}}, "structuredContent": map[string]any{"answer": 42}}
	wrapped := map[string]any{"resultType": "complete", "content": []map[string]any{{"type": "text", "text": "Child MCP tool completed"}}, "structuredContent": map[string]any{"ref": "GrepMesh", "name": "search_status", "result": upstream}}
	raw := map[string]any{"job_id": "j", "status": "completed", "result": wrapped}
	out := compactChildOutput(raw, "mcp:shell:100:GrepMesh", "search_status")
	if !reflect.DeepEqual(out["result"], upstream["structuredContent"]) {
		t.Fatal("child business data changed", out)
	}
	block := upstream["content"].([]map[string]any)[0]
	block["text"] = "{\"answer\":42} additional information"
	if !reflect.DeepEqual(compactChildOutput(raw, "mcp:shell:100:GrepMesh", "search_status")["result"], upstream) {
		t.Fatal("trailing business text discarded")
	}
	block["text"] = "{\"answer\":42}"
	block["annotations"] = map[string]any{"audience": []string{"user"}}
	if !reflect.DeepEqual(compactChildOutput(raw, "mcp:shell:100:GrepMesh", "search_status")["result"], upstream) {
		t.Fatal("content metadata discarded")
	}
	delete(block, "annotations")
	upstream["isError"] = true
	out = compactChildOutput(raw, "mcp:shell:100:GrepMesh", "search_status")
	if !reflect.DeepEqual(out["result"], upstream) {
		t.Fatal("error metadata dropped")
	}
	if !reflect.DeepEqual(compactChildOutput(raw, "mcp:shell:100:Other", "search_status"), raw) {
		t.Fatal("unrelated business wrapper stripped")
	}
}
