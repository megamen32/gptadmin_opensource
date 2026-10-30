package hub

import (
	"strings"
	"testing"
)

func TestQuorumRegistryKeepsPinnedLocalExecutor(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	identity := map[string]string{"server_id": "local-id", "public_key": "local-key", "fingerprint": "local-fp"}
	if err := s.RegisterLocalExecutor("local", identity, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := s.materializeQuorumRegistry(persistentRegistryState{Agents: map[string]Agent{}}); err != nil {
		t.Fatal(err)
	}
	if _, status, message := s.selectMCPRelayTarget("shell:local"); status != 200 {
		t.Fatalf("pinned local executor unavailable: %d %s", status, message)
	}
	if s.localExecutorTokens["shell:local"] != strings.Repeat("x", 32) {
		t.Fatal("local executor credential changed")
	}
}

func TestQuorumRegistryRejectsConflictingPinnedIdentity(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	identity := map[string]string{"server_id": "local-id", "public_key": "local-key", "fingerprint": "local-fp"}
	if err := s.RegisterLocalExecutor("local", identity, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	conflict := Agent{AgentID: "shell:local", Meta: map[string]any{"server_id": "other-id"}}
	if err := s.materializeQuorumRegistry(persistentRegistryState{Agents: map[string]Agent{"shell:local": conflict}}); err != nil || s.agents["shell:local"].Meta["server_id"] != "other-id" {
		t.Fatal("conflicting committed identity was overwritten or blocked authority")
	}
	conflict.Meta["server_id"] = 123
	if err := s.materializeQuorumRegistry(persistentRegistryState{Agents: map[string]Agent{"shell:local": conflict}}); err != nil || s.agents["shell:local"].Meta["server_id"] != float64(123) {
		t.Fatal("malformed committed identity was overwritten or blocked authority")
	}
}

func TestQuorumRegistryDoesNotOverrideUnapprovedExecutor(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	identity := map[string]string{"server_id": "local-id", "public_key": "local-key", "fingerprint": "local-fp"}
	if err := s.RegisterLocalExecutor("local", identity, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	unapproved := Agent{AgentID: "shell:local", Meta: map[string]any{"approved": false, "server_id": "local-id"}}
	if err := s.materializeQuorumRegistry(persistentRegistryState{Agents: map[string]Agent{"shell:local": unapproved}}); err != nil || truthy(s.agents["shell:local"].Meta["approved"]) {
		t.Fatal("unapproved executor was approved or blocked authority")
	}
}

func TestQuorumRegistryKeepsSelfEnrollmentPending(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	s.localExecutors = map[string]map[string]string{"shell:pending": {}}
	s.localExecutorTokens = map[string]string{"shell:pending": strings.Repeat("x", 32)}
	pending := Agent{AgentID: "shell:pending", Meta: map[string]any{"approved": false, "self_enrolled": true}}
	if err := s.materializeQuorumRegistry(persistentRegistryState{Agents: map[string]Agent{"shell:pending": pending}}); err != nil {
		t.Fatal(err)
	}
	if agent := s.agents["shell:pending"]; agent == nil || truthy(agent.Meta["approved"]) {
		t.Fatal("pending self-enrollment was approved")
	}
}

// Promotion must not erase existing operator metadata while restoring the
// node's physically pinned in-process executor.
func TestQuorumRegistryPreservesPinnedExecutorMetadata(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	identity := map[string]string{"server_id": "local-id", "public_key": "local-key", "fingerprint": "local-fp"}
	if err := s.RegisterLocalExecutor("local", identity, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	existing := Agent{AgentID: "shell:local", Name: "Operator label", Kind: "virtual_shell", Transport: "long_poll", RegisteredAt: 123, Capabilities: []string{"shell", "custom-capability"}, Meta: map[string]any{"approved": true, "server_id": "local-id", "public_key": "local-key", "fingerprint": "local-fp", "operator_note": "keep"}}
	if err := s.materializeQuorumRegistry(persistentRegistryState{Agents: map[string]Agent{"shell:local": existing}}); err != nil {
		t.Fatal(err)
	}
	got := s.agents["shell:local"]
	if got == nil || got.RegisteredAt != 123 || got.Name != "Operator label" || got.Meta["operator_note"] != "keep" || !strings.Contains(strings.Join(got.Capabilities, ","), "custom-capability") {
		t.Fatal("pinned executor metadata was lost")
	}
}
