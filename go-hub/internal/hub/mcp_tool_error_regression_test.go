package hub

import "testing"

func TestMCPToolResultMarksUnexecutedStructuredErrors(t *testing.T) {
	payload := map[string]any{"executed": false, "error": map[string]any{"code": "fixture_denied", "message": "fixture access denied"}}
	result := mcpToolResult(payload)
	if result["isError"] != true {
		t.Fatalf("unexecuted refusal must be a tool error: %#v", result)
	}
	detail := mapValue(mapValue(result["structuredContent"])["error"])
	if firstString(detail, "code") != "fixture_denied" || firstString(detail, "message") != "fixture access denied" {
		t.Fatalf("refusal details lost: %#v", detail)
	}
}
