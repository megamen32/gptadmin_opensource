package hub

import "testing"

func discoveryFixture() map[string]any {
	return map[string]any{"servers": []any{
		map[string]any{"server_id": "shell:a", "status": "online", "name": "Shell: a"},
		map[string]any{"server_id": "shell:b", "status": "stale"},
		map[string]any{"server_id": "shell:c", "status": "awaiting_approval"},
		map[string]any{"server_id": "shell:d", "status": "failed"},
	}}
}

func TestCompactDiscoveryListsOnlineAndCountsTheRest(t *testing.T) {
	out := compactDiscovery(discoveryFixture(), map[string]any{})
	if rows := outputRows(out["servers"]); len(rows) != 1 {
		t.Fatalf("expected only the online target, got %v", out["servers"])
	}
	counts, _ := out["not_listed"].(map[string]int)
	if counts["stale"] != 1 || counts["failed"] != 1 || counts["awaiting_approval"] != 1 {
		t.Fatalf("collapsed counts are wrong: %v", out["not_listed"])
	}
	pending, _ := out["pending_approvals"].([]string)
	if len(pending) != 1 || pending[0] != "shell:c" || out["approve_with"] == nil {
		t.Fatalf("devices waiting for approval must be named with the tool to use: %v", out)
	}
}

func TestCompactDiscoveryFullAndFilteredStayComplete(t *testing.T) {
	full := compactDiscovery(discoveryFixture(), map[string]any{"detail": "full"})
	if rows := outputRows(full["servers"]); len(rows) != 4 || full["not_listed"] != nil {
		t.Fatalf("detail=full must list every target: %v", full)
	}
	stale := compactDiscovery(discoveryFixture(), map[string]any{"status": "stale"})
	if rows := outputRows(stale["servers"]); len(rows) != 1 || stale["not_listed"] != nil {
		t.Fatalf("a status filter must list matching targets: %v", stale)
	}
}

func TestChildSearchUnreachableStatuses(t *testing.T) {
	for _, status := range []string{"failed", "stale", "offline", "awaiting_approval", " Failed "} {
		if !childSearchUnreachable(Agent{Status: status}) {
			t.Fatalf("%q must be treated as unreachable", status)
		}
	}
	if childSearchUnreachable(Agent{Status: "online"}) {
		t.Fatal("an online child must be refreshed")
	}
}
