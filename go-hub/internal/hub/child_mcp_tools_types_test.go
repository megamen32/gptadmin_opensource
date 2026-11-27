package hub

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestChildMCPToolsNativeAndWireShapes(t *testing.T) {
	one := map[string]any{"name": "read_fixture", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}, "allowed_actions": []string{"status"}}
	native := []map[string]any{one}
	many := make([]map[string]any, 45)
	for i := range many {
		many[i] = map[string]any{"name": fmt.Sprintf("fixture_%02d", i), "inputSchema": map[string]any{"type": "object"}}
	}
	rawFor := func(tools any) map[string]any {
		return map[string]any{"response": map[string]any{"structuredContent": map[string]any{"server": "node-44", "result": map[string]any{"structuredContent": map[string]any{"ref": "AgentHerder", "tools": tools}}}}}
	}
	b, e := json.Marshal(rawFor(native))
	if e != nil {
		t.Fatal(e)
	}
	var wire map[string]any
	if e = json.Unmarshal(b, &wire); e != nil {
		t.Fatal(e)
	}
	wireTools := mapValue(mapValue(mapValue(mapValue(wire["response"])["structuredContent"])["result"])["structuredContent"])["tools"].([]any)
	cases := []struct {
		name string
		raw  map[string]any
		want []map[string]any
	}{
		{"native_typed_single", rawFor(native), native}, {"native_typed_45", rawFor(many), many},
		{"decoded_json", wire, []map[string]any{mapValue(wireTools[0])}},
		{"empty_native", rawFor([]map[string]any{}), []map[string]any{}}, {"empty_json", rawFor([]any{}), []map[string]any{}},
		{"null", rawFor(nil), []map[string]any{}}, {"scalar_is_not_tools", rawFor("bad"), []map[string]any{}},
		{"map_is_not_tools", rawFor(one), []map[string]any{}}, {"mixed_rows_skip_invalid", rawFor([]any{nil, "bad", map[string]any{}, one}), native},
		{"missing_response", map[string]any{}, []map[string]any{}}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.raw)
			got := childMCPTools(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %d tools; want %d", len(got), len(tc.want))
			}
			after, _ := json.Marshal(tc.raw)
			if string(before) != string(after) {
				t.Fatal("input mutated")
			}
		})
	}
}
