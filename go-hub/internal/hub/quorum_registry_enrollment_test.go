package hub

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestControlRegistryReplicatesEnrollmentWithoutObservedPayload(t *testing.T) {
	const id, raw, observation = "shell:phone", "synthetic-device-credential", "private-tool-schema-marker"
	old := persistentRegistryState{Agents: map[string]Agent{
		id: {AgentID: id, Kind: "virtual_shell", Status: "online", LastSeen: 123,
			Meta: map[string]any{"server_id": "phone", "approved": true, "self_enrolled": true, "mcp_agents": observation}},
	}, LocalExecutorTokens: map[string]string{id: raw}}
	enrollment := registryEnrollments(old)
	first, err := json.Marshal(enrollment)
	if err != nil || bytes.Contains(first, []byte(raw)) || bytes.Contains(first, []byte(observation)) {
		t.Fatal("registry authority contained a credential or an observed catalog")
	}
	if firstString(enrollment.Agents[id].Meta, shellTokenHashMetaKey) != shellTokenHash(raw) {
		t.Fatal("legacy local credential could not be verified after an election")
	}
	agent := old.Agents[id]
	agent.LastSeen++
	agent.Status = "stale"
	agent.Meta["queue_poll_count"] = 1000
	old.Agents[id] = agent
	second, err := json.Marshal(registryEnrollments(old))
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("liveness changed the control record")
	}
	// A reader's disk has an old credential and a previously approved device.
	// The elected writer must respect a later revocation and removed devices.
	agent.Meta[shellTokenHashMetaKey] = shellTokenHash(raw)
	old.Agents["shell:removed"] = agent
	path := filepath.Join(t.TempDir(), "registry.json")
	if err = writeAccessStateJSON(path, old, 16<<20); err != nil {
		t.Fatal(err)
	}
	for _, enrolled := range enrollment.Agents {
		enrolled.Meta["approved"] = false
	}
	restored, err := restoreRegistryEnrollments(path, enrollment)
	if err != nil {
		t.Fatal(err)
	}
	got := restored.Agents[id]
	if len(restored.Agents) != 1 || truthyAny(got.Meta["approved"]) || got.Meta["mcp_agents"] != observation || len(restored.LocalExecutorTokens) != 0 {
		t.Fatal("restored enrollment leaked stale permissions, lost local observations, or copied a raw credential")
	}
}
