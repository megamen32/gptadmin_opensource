package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func directSchemaFixture(t *testing.T, remote http.HandlerFunc) (*Server, Agent, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		remote(w, r)
	}))
	t.Cleanup(peer.Close)
	s := peerTestHub(t, "schema-test-token", map[string]string{"shell:peer": peer.URL})
	parent := Agent{AgentID: "shell:peer", Kind: "virtual_shell", Status: "online", Meta: map[string]any{
		"approved": true, "mcp_agents": []any{map[string]any{
			"ref": "sample", "enabled": true, "name": "sample", "transport": "streamable-http",
		}},
	}}
	s.mu.Lock()
	s.agents[parent.AgentID] = &parent
	s.mu.Unlock()
	return s, childMCPAgents(parent)[0], calls
}

func TestDirectChildSchemaAndExplicitSchemaNoDurableJobs(t *testing.T) {
	s, child, calls := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/mcp-relay/tools" || r.Header.Get("Authorization") != "Bearer schema-test-token" ||
			firstString(body, "target") != childTargetFixture() {
			t.Errorf("schema request did not preserve owner/authorization: %v %v", r.URL.Path, body)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "completed", "response": map[string]any{"tools": []any{
				map[string]any{"name": "hello", "description": "original MCP tool", "inputSchema": map[string]any{"type": "object"}},
			}},
		})
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer schema-test-token")
	direct := s.directChildSchemaForRequest(req, child)
	if firstString(direct, "status") != "completed" ||
		len(searchChildToolMaps(mapValue(direct["response"])["tools"])) != 1 {
		t.Fatalf("direct schema failed: %#v", direct)
	}
	explicit := mapValue(s.appsSDKSchemaForRequest(req, map[string]any{"target": child.AgentID}))
	if firstString(explicit, "status") != "completed" || calls.Load() != 2 {
		t.Fatalf("explicit tools/list was not direct: %#v calls=%d", explicit, calls.Load())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 || len(s.shellQueues) != 0 {
		t.Fatal("read-only child tools/list allocated durable Jobs")
	}
}

func childTargetFixture() string { return "mcp:shell:peer:sample" }

func TestDirectChildSearchUsesBoundedCacheWithoutQueue(t *testing.T) {
	s, child, calls := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "completed", "response": map[string]any{"tools": []any{
				map[string]any{"name": "read_sapphire", "description": "sapphire source", "inputSchema": map[string]any{"type": "object"}},
			}},
		})
	})
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer schema-test-token")
	rows, coverage := s.childSearchCatalogForRequest(r, child.AgentID)
	if len(rows) != 1 || !truthy(coverage["complete"]) || calls.Load() != 1 {
		t.Fatalf("cold search incorrect: %#v %#v calls=%d", rows, coverage, calls.Load())
	}
	rows, coverage = s.childSearchCatalogForRequest(r, child.AgentID)
	if len(rows) != 1 || !truthy(coverage["complete"]) || calls.Load() != 1 {
		t.Fatalf("warm cache did not eliminate read: %#v %#v calls=%d", rows, coverage, calls.Load())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
		t.Fatal("cold search allocated durable Jobs")
	}
}

func TestDirectChildSchemaUnavailableNeverQueues(t *testing.T) {
	s, child, calls := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "offline"})
	})
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer schema-test-token")
	got := s.directChildSchemaForRequest(r, child)
	if firstString(got, "status") != "failed" || calls.Load() != 1 {
		t.Fatalf("offline schema was not reported: %#v", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
		t.Fatal("offline schema enqueued an unfulfillable job")
	}
}

type localSchemaReaderStub struct{ calls int }

func (*localSchemaReaderStub) ActiveCallbacks() int { return 0 }
func (*localSchemaReaderStub) OutboxPending() int   { return 0 }
func (l *localSchemaReaderStub) DirectChildTools(_ context.Context, args map[string]any) (map[string]any, error) {
	l.calls++
	if firstString(args, "ref") != "sample" {
		panic("wrong MCP ref")
	}
	return map[string]any{"structuredContent": map[string]any{
		"tools": []map[string]any{{"name": "local_tool", "inputSchema": map[string]any{"type": "object"}}},
	}}, nil
}

func TestLocalDirectChildSchemaUsesEmbeddedExecutorWithoutJobs(t *testing.T) {
	s, child, peerCalls := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("local schema unexpectedly used the remote peer")
	})
	reader := &localSchemaReaderStub{}
	s.mu.Lock()
	if s.localExecutors == nil {
		s.localExecutors = make(map[string]map[string]string)
	}
	s.localExecutors["shell:peer"] = map[string]string{"server_id": "fixture"}
	s.nodeRuntime = reader
	s.mu.Unlock()
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer schema-test-token")
	result := s.directChildSchemaForRequest(r, child)
	if firstString(result, "status") != "completed" || reader.calls != 1 || peerCalls.Load() != 0 {
		t.Fatalf("local reader failed: %#v", result)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
		t.Fatal("local schema allocated durable Jobs")
	}
}

func TestDirectChildSchemaDoesNotExposeDeniedTarget(t *testing.T) {
	s, child, calls := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("denied principal contacted upstream")
	})
	profile := AccessProfile{ID: "denied-schema", Version: 1, AccessMode: "full", AllowedTargets: []string{"shell:unrelated"}}
	r := requestWithAccessProfile(httptest.NewRequest(http.MethodPost, "/mcp", nil), profile)
	r.Header.Set("Authorization", "Bearer schema-test-token")
	got := s.directChildSchemaForRequest(r, child)
	if firstString(got, "status") != "failed" || calls.Load() != 0 ||
		!strings.Contains(firstString(mapValue(got["error"]), "message"), "denied") {
		t.Fatalf("denied schema was contacted or revealed: %#v", got)
	}
}
