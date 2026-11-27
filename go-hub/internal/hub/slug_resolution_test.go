package hub

import (
	"strings"
	"testing"
	"time"
)

// Regression: bare child names are shared across hosts ("grepmesh" exists on
// every mesh member), so /server/grepmesh/mcp used to resolve to a random
// same-named child via map iteration order. The assigned slug must win
// deterministically, and name matches must prefer a live parent shell.
func TestResolveExposedAgentPrefersAssignedSlugOverSharedName(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	s.mu.Lock()
	s.agents["shell:admin-node-100"] = &Agent{
		AgentID: "shell:admin-node-100", Name: "Shell: admin-node-100",
		Kind: "virtual_shell", Status: "online",
	}
	s.agents["shell:mac-mini-2012.lan"] = &Agent{
		AgentID: "shell:mac-mini-2012.lan", Name: "Shell: mac-mini-2012.lan",
		Kind: "virtual_shell", Status: "stale",
	}
	s.agents["mcp:shell:admin-node-100:GrepMesh"] = &Agent{
		AgentID: "mcp:shell:admin-node-100:GrepMesh", Name: "grepmesh", Kind: "child_mcp", Status: "online",
		Meta: map[string]any{
			"parent_server_id": "shell:admin-node-100", "child_ref": "GrepMesh",
			"public_mcp_slug": "grepmesh",
		},
	}
	s.agents["mcp:shell:mac-mini-2012.lan:mac-mini-GrepMesh"] = &Agent{
		AgentID: "mcp:shell:mac-mini-2012.lan:mac-mini-GrepMesh", Name: "grepmesh", Kind: "child_mcp", Status: "online",
		Meta: map[string]any{
			"parent_server_id": "shell:mac-mini-2012.lan", "child_ref": "mac-mini-GrepMesh",
			"public_mcp_slug": "mac-mini-grepmesh",
		},
	}
	s.mu.Unlock()

	for i := 0; i < 64; i++ {
		got, ok := s.resolveExposedAgent("grepmesh")
		if !ok {
			t.Fatalf("iteration %d: slug unresolved", i)
		}
		if got.AgentID != "mcp:shell:admin-node-100:GrepMesh" {
			t.Fatalf("iteration %d: bare slug routed to %s; the assigned owner must win deterministically", i, got.AgentID)
		}
	}
}

// Name-only matches (no assigned slug) must prefer the child whose parent
// shell is online over one parked on a stale shell.
func TestResolveExposedAgentNameMatchPrefersLiveParent(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	s.mu.Lock()
	s.agents["shell:admin-node-100"] = &Agent{
		AgentID: "shell:admin-node-100", Kind: "virtual_shell", Status: "online",
	}
	s.agents["shell:MacBook-Pro-User.local"] = &Agent{
		AgentID: "shell:MacBook-Pro-User.local", Kind: "virtual_shell", Status: "stale",
	}
	s.agents["mcp:shell:admin-node-100:GrepMesh"] = &Agent{
		AgentID: "mcp:shell:admin-node-100:GrepMesh", Name: "grepmesh", Kind: "child_mcp", Status: "online",
		Meta: map[string]any{"parent_server_id": "shell:admin-node-100", "child_ref": "GrepMesh"},
	}
	s.agents["mcp:shell:MacBook-Pro-User.local:mac-m1-GrepMesh"] = &Agent{
		AgentID: "mcp:shell:MacBook-Pro-User.local:mac-m1-GrepMesh", Name: "grepmesh", Kind: "child_mcp", Status: "online",
		Meta: map[string]any{"parent_server_id": "shell:MacBook-Pro-User.local", "child_ref": "mac-m1-GrepMesh"},
	}
	s.mu.Unlock()

	for i := 0; i < 64; i++ {
		got, ok := s.resolveExposedAgent("grepmesh")
		if !ok || got.AgentID != "mcp:shell:admin-node-100:GrepMesh" {
			t.Fatalf("iteration %d: name match routed to %+v; a live parent must win", i, got.AgentID)
		}
	}
}

// Synchronous shell tool calls must fail fast when the target shell stopped
// polling instead of burning the whole tool timeout in a readerless queue.
func TestCallShellToolFailsFastOnStaleShell(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 20 * time.Second, PollMaxTimeout: time.Second})
	s.mu.Lock()
	s.agents["shell:mac-mini-2012.lan"] = &Agent{
		AgentID: "shell:mac-mini-2012.lan", Name: "Shell: mac-mini-2012.lan",
		Kind: "virtual_shell", Status: "stale",
	}
	s.mu.Unlock()

	started := time.Now()
	raw := s.callShellToolWithTraceParent("shell:mac-mini-2012.lan", "mcp_tools", map[string]any{"ref": "GrepMesh"}, false, s.cfg.DefaultTimeout, "", "")
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("stale-shell call took %v; it must fail fast", elapsed)
	}
	if firstString(raw, "status") != "failed" {
		t.Fatalf("expected failed status, got %v", raw)
	}
	if !strings.Contains(firstString(raw, "error"), "no active queue poller") {
		t.Fatalf("expected honest no-poller error, got %v", raw)
	}
}
