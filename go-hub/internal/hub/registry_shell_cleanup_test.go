package hub

import (
	"net/http"
	"testing"
	"time"
)

func TestManualCleanupIncludesIdleStaleShellAndKeepsWork(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	old := float64(time.Now().Add(-100 * 24 * time.Hour).Unix())
	for _, name := range []string{"obsolete", "busy", "protected"} {
		id := "shell:" + name
		s.agents[id] = &Agent{AgentID: id, Name: name, Kind: "virtual_shell", Status: "stale", LastSeen: old}
	}
	s.agents["shell:live"] = &Agent{AgentID: "shell:live", Kind: "virtual_shell", Status: "online", LastSeen: nowFloat()}
	s.agentPolicies["shell:protected"] = agentCleanupPolicy{Protected: true}
	s.shellJobs["pending-cleanup-test"] = &shellJob{ID: "pending-cleanup-test", Server: "busy", Status: "queued"}
	result, status := s.callHubTool("stale_cleanup_run", map[string]any{"agent_ids": []any{"shell:obsolete", "shell:busy", "shell:protected", "shell:live"}})
	if status != http.StatusOK || intFromAny(result["count"]) != 1 {
		t.Fatalf("cleanup=%v status=%d", result, status)
	}
	if s.agents["shell:obsolete"] != nil {
		t.Fatal("obsolete shell retained")
	}
	for _, id := range []string{"shell:busy", "shell:protected", "shell:live"} {
		if s.agents[id] == nil {
			t.Fatalf("deleted protected target %s", id)
		}
	}
	if s.shellJobs["pending-cleanup-test"] == nil {
		t.Fatal("task history lost")
	}
	restarted := New(Config{ConfigDir: s.cfg.ConfigDir})
	if restarted.agents["shell:obsolete"] != nil {
		t.Fatal("deleted server resurrected")
	}
	if len(restarted.tombstones) != 1 {
		t.Fatal("tombstone not persisted")
	}
}

func TestShellCleanupProtectsPersistedUncachedTask(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	id := "shell:old-queued"
	s.agents[id] = &Agent{AgentID: id, Kind: "virtual_shell", Status: "stale", LastSeen: float64(time.Now().Add(-100 * 24 * time.Hour).Unix())}
	s.shellJobs["durable-pending"] = &shellJob{ID: "durable-pending", Server: "old-queued", Status: "queued", CreatedAt: nowFloat()}
	if err := s.saveTaskStateLocked("durable-pending"); err != nil {
		t.Fatal(err)
	}
	delete(s.shellJobs, "durable-pending")
	result, status := s.callHubTool("stale_cleanup_run", map[string]any{"agent_ids": []any{id}})
	if status != http.StatusOK || intFromAny(result["count"]) != 0 || s.agents[id] == nil {
		t.Fatalf("lost durable executor: %v", result)
	}
}
