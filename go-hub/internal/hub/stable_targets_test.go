package hub

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShortTargetDiscoveryAndCanonicalResolution(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	id := "shell:mac-long-original-name"
	s.agents[id] = &Agent{AgentID: id, Name: "Mac", Kind: "shell", Status: "online", Meta: map[string]any{"server_id": "persisted-node", "fingerprint": "key-A"}}
	short := s.shortTarget(id)
	out := mapValue(s.appsSDKCallForRequest(nil, "discover", map[string]any{}))
	found := false
	for _, v := range outputRows(out["servers"]) {
		if firstString(mapValue(v), "server_id") == short {
			found = true
		}
	}
	if !found {
		t.Fatalf("short target absent: %v", out)
	}
	canonical, status, detail := s.selectMCPRelayTarget(short)
	if canonical != id || status != 200 {
		t.Fatalf("resolve: %s %d %s", canonical, status, detail)
	}
	out = mapValue(s.appsSDKCallForRequest(nil, "schema", map[string]any{"target": short, "tool": "shell_exec"}))
	if firstString(out, "status") != "completed" {
		t.Fatalf("schema: %v", out)
	}
}

func TestShortTargetAmbiguousAliasFailsClosed(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	id := "shell:real-host"
	s.agents[id] = &Agent{AgentID: id, Kind: "shell", Status: "online", Meta: map[string]any{"server_id": "persisted-node", "fingerprint": "key-A"}}
	alias := s.shortTarget(id)
	s.agents[alias] = &Agent{AgentID: alias, Kind: "mcp", Status: "online"}
	if _, status, _ := s.selectMCPRelayTarget(alias); status == 200 {
		t.Fatal("ambiguous alias accepted")
	}
}

func TestShortTargetUsesCanonicalAccessPolicy(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	id := "shell:policy-host"
	s.agents[id] = &Agent{AgentID: id, Kind: "shell", Status: "online", Meta: map[string]any{"server_id": "persisted-node", "fingerprint": "key-A"}}
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), AccessProfile{ID: "canonical", AccessMode: "full", AllowedTargets: []string{id}, AllowedTools: []string{"schema", "shell_exec"}})
	args := map[string]any{"target": s.shortTarget(id), "tool": "shell_exec"}
	if err := s.authorizePublicFacade(r, "schema", args); err != nil {
		t.Fatalf("canonical policy rejected: %v", err)
	}
	if firstString(args, "target") != id {
		t.Fatal("ACL saw opaque alias")
	}
	denied := requestWithAccessProfile(r, AccessProfile{ID: "denied", AccessMode: "full", AllowedTargets: []string{"shell:other"}, AllowedTools: []string{"schema", "shell_exec"}})
	if err := s.authorizePublicFacade(denied, "schema", map[string]any{"target": s.shortTarget(id)}); err == nil {
		t.Fatal("alias bypassed target ACL")
	}
}

func TestShortTargetCodeModeSchemaAndSearch(t *testing.T) {
	s, r := codeModeFixture(t)
	id := "shell:short-code-mode"
	s.agents[id] = &Agent{AgentID: id, Kind: "shell", Status: "online", Name: "Mac", Meta: map[string]any{"server_id": "persisted-node", "fingerprint": "key-A"}}
	code := fmt.Sprintf(`const s = await mcp.schema(%q, 'shell_exec'); return {tool:s.tool};`, s.shortTarget(id))
	response, status := codeModeRun(t, s, r, map[string]any{"code": code})
	if status != 200 || firstString(mapValue(response["result"]), "tool") != "shell_exec" {
		t.Fatalf("short schema failed: %v", response)
	}
	result := s.appsSDKCallForRequest(r, "search", map[string]any{"query": "shell_exec", "target": s.shortTarget(id)})
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), s.shortTarget(id)) || strings.Contains(string(raw), `await call(\"shell:`) {
		t.Fatalf("search lacks short call: %s", raw)
	}
}

func TestShortTargetBoundToPersistentIdentity(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	alias := func(id string) string {
		return firstString(mapValue(s.presentTokenOutput(nil, "schema", map[string]any{}, map[string]any{"server_id": id})), "server_id")
	}
	identity := map[string]any{"server_id": "persistent-node-id", "fingerprint": "key-fingerprint-A"}
	old := "shell:mac-old"
	renamed := "shell:mac-new"
	s.agents[old] = &Agent{AgentID: old, Kind: "shell", Status: "online", Meta: identity}
	before := alias(old)
	delete(s.agents, old)
	s.agents[renamed] = &Agent{AgentID: renamed, Kind: "shell", Status: "online", Meta: cloneMap(identity)}
	if got := alias(renamed); got != before {
		t.Fatalf("same persisted identity renamed: %s -> %s", before, got)
	}
	s.agents[renamed].Meta["fingerprint"] = "key-fingerprint-B"
	if alias(renamed) == before {
		t.Fatal("replacement key inherited previous short ID")
	}
	if _, status, _ := s.selectMCPRelayTarget(before); status == 200 {
		t.Fatal("retired identity alias still resolves")
	}
}

func TestShortChildTargetBoundToParentIdentityAndRef(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	alias := func(id string) string {
		return firstString(mapValue(s.presentTokenOutput(nil, "schema", map[string]any{}, map[string]any{"server_id": id})), "server_id")
	}
	setup := func(host, key, ref string) string {
		parent := "shell:" + host
		child := "mcp:" + parent + ":" + ref
		s.agents = map[string]*Agent{
			parent: {AgentID: parent, Kind: "shell", Status: "online", Meta: map[string]any{"server_id": "node-id", "fingerprint": key}},
			child:  {AgentID: child, Kind: "child_mcp", Status: "online", Meta: map[string]any{"parent_server_id": parent, "child_ref": ref}},
		}
		return child
	}
	before := alias(setup("mac-before", "key-A", "GrepMesh"))
	if got := alias(setup("mac-after", "key-A", "GrepMesh")); got != before {
		t.Fatalf("child alias follows hostname: %s -> %s", before, got)
	}
	if alias(setup("mac-after", "key-B", "GrepMesh")) == before {
		t.Fatal("child alias inherited by replacement key")
	}
	if alias(setup("mac-after", "key-A", "Other")) == before {
		t.Fatal("distinct child ref shares alias")
	}
}

func TestShortTargetRequiresPersistentIdentity(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	id := "shell:legacy"
	s.agents[id] = &Agent{AgentID: id, Kind: "shell", Status: "online"}
	if s.shortTarget(id) != id {
		t.Fatal("anonymous route received a name-derived identity")
	}
	s.agents[id].Meta = map[string]any{"server_id": "one", "fingerprint": "same-key"}
	first := s.shortTarget(id)
	s.agents[id].Meta["server_id"] = "two"
	if first == s.shortTarget(id) {
		t.Fatal("server_id excluded from public identity")
	}
}
