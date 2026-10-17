package hub

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

// Fast units, expected <1s/max10s. Detect duplicated native model content
// without claiming that an SDK or provider was exercised here.
func TestTokenNativeContentRemovesOnlyExactDuplicate(t *testing.T) {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("X-GPTAdmin-Result-Mode", "text")
	raw := map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"value":false}`}}, "structuredContent": map[string]any{"value": false}, "_meta": map[string]any{"retained": true}}
	result := nativeFacadeResult(r, raw).(map[string]any)
	if _, present := result["structuredContent"]; present {
		t.Fatal("native client would receive the same result twice")
	}
	if !reflect.DeepEqual(result["content"], raw["content"]) || !reflect.DeepEqual(result["_meta"], raw["_meta"]) || raw["structuredContent"] == nil {
		t.Fatal("content/metadata or caller-owned result changed")
	}
	for _, value := range []map[string]any{
		{"content": []any{map[string]any{"type": "text", "text": `{"value":true}`}}, "structuredContent": map[string]any{"value": false}},
		{"content": []any{map[string]any{"type": "image", "data": "unit-only"}}, "structuredContent": map[string]any{"value": false}},
		{"content": raw["content"], "structuredContent": raw["structuredContent"], "isError": true},
	} {
		if !reflect.DeepEqual(nativeFacadeResult(r, value), value) {
			t.Fatal("meaningful content, media or error changed")
		}
	}
	r.Header.Del("X-GPTAdmin-Result-Mode")
	result = nativeFacadeResult(r, raw).(map[string]any)
	if !reflect.DeepEqual(result["structuredContent"], raw["structuredContent"]) || len(outputRows(result["content"])) != 0 {
		t.Fatal("default result must keep its schema contract without duplicate text")
	}
	r.Header.Set("X-GPTAdmin-Result-Mode", "dual")
	if !reflect.DeepEqual(nativeFacadeResult(r, raw), raw) {
		t.Fatal("explicit dual result changed")
	}
}

func TestTokenNativeCatalogHasMatchingResultContract(t *testing.T) {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("X-GPTAdmin-Result-Mode", "text")
	for _, tool := range nativeFacadeTools(r, compactFacadeTools(r, false)) {
		if firstString(tool, "name") != "ui" && tool["outputSchema"] != nil {
			t.Fatal("text-only facade still requires structuredContent")
		}
	}
}
