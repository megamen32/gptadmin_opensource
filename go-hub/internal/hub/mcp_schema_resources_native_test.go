package hub

import (
	"net/http"
	"testing"
)

func TestMCPSchemaResourcesNativeCataloguesAreComplete(t *testing.T) {
	s, _, r, _, calls := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("native schema attempted a child fetch")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	targets := []string{"shell:peer", "file:peer", "control:peer"}
	s.mu.Lock()
	// Native file/control targets are projected from their enrolled shell.
	s.agents["shell:peer"].Meta["file_transfer_version"] = 1
	s.mu.Unlock()
	r = requestWithAccessProfile(r, AccessProfile{ID: "native-schema-reader", AccessMode: "full",
		AllowedTargets: targets, AllowedTools: []string{"*"}})
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			s.mu.Lock()
			identity := fullTargetIdentityFromAgents(target, targetAgentMap(s.publicAgentsLocked(r)))
			s.mu.Unlock()
			if identity == "" {
				t.Fatal("native fixture has no enrolled identity")
			}
			doc := schemaResourceDocument(t, s, r, mcpSchemaServerPrefix+identity)
			if firstString(doc, "status") != "complete" || !truthy(doc["complete"]) || len(searchChildToolMaps(doc["tools"])) == 0 {
				t.Fatalf("complete native catalogue was marked partial: target=%s status=%v reason=%v", target, doc["status"], doc["reason"])
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("native schemas fetched a child catalog")
	}
}
