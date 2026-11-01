package hub

import "testing"

func TestSearchAnyWordFallbackAndServerNames(t *testing.T) {
	i, err := newToolSearchIndex()
	if err != nil {
		t.Fatal(err)
	}
	defer i.db.Close()
	catalog := []map[string]any{
		{"target": "mcp:test:vibe", "name": "browser_tabs", "description": "List browser tabs", "labels": "VibeMCP child MCP"},
		{"target": "mcp:test:omni", "name": "list_combos", "description": "List routing combinations", "labels": "OmniRoute child MCP"},
	}
	for _, q := range []string{"Vibe MCP", "Vibe unknownterm", "Omni Route", "OmniRoute unknownterm"} {
		hits, err := i.search(q, "", 10, catalog)
		if err != nil || len(hits) == 0 {
			t.Errorf("query %q: hits=%v err=%v", q, hits, err)
		}
	}
	hits, err := i.search("Vibe unknownterm", "mcp:test:omni", 10, catalog)
	if err != nil || len(hits) != 0 {
		t.Fatalf("target leak: %v %v", hits, err)
	}
}
