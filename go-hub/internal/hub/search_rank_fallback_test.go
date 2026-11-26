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

func TestSearchBroadServiceQueryDiversifiesToolsWithoutLosingExactTargets(t *testing.T) {
	i, err := newToolSearchIndex()
	if err != nil {
		t.Fatal(err)
	}
	defer i.db.Close()
	catalog := []map[string]any{
		{"target": "mcp:test:a", "name": "browser_status", "description": "Status", "labels": "bezbrowser child MCP"},
		{"target": "mcp:test:b", "name": "browser_status", "description": "Status", "labels": "bezbrowser child MCP"},
		{"target": "mcp:test:c", "name": "browser_status", "description": "Status", "labels": "bezbrowser child MCP"},
		{"target": "mcp:test:a", "name": "browser_tabs", "description": "List and create tabs in this browser", "labels": "bezbrowser child MCP"},
		{"target": "mcp:test:b", "name": "browser_tabs", "description": "List and create tabs in this browser", "labels": "bezbrowser child MCP"},
	}
	hits, err := i.search("bezbrowser", "", 2, catalog)
	if err != nil || len(hits) != 2 || firstString(hits[0], "name") == firstString(hits[1], "name") {
		t.Fatalf("broad service search repeated a tool before showing another capability: %v %v", hits, err)
	}
	exact, err := i.search("browser_status", "", 3, catalog)
	if err != nil || len(exact) != 3 {
		t.Fatalf("exact name did not retain all three targets: %v %v", exact, err)
	}
	seen := map[string]bool{}
	for _, row := range exact {
		if firstString(row, "name") != "browser_status" {
			t.Fatalf("exact match was demoted: %v", exact)
		}
		seen[firstString(row, "target")] = true
	}
	if len(seen) != 3 {
		t.Fatalf("exact target identity lost: %v", exact)
	}
	targeted, err := i.search("bezbrowser", "mcp:test:b", 10, catalog)
	if err != nil || len(targeted) != 2 {
		t.Fatalf("targeted search lost tools: %v %v", targeted, err)
	}
	for _, row := range targeted {
		if firstString(row, "target") != "mcp:test:b" {
			t.Fatalf("target leak: %v", targeted)
		}
	}
}
