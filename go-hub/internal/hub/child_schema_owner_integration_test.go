package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type ownerIntegrationReader struct{ calls int }

func (*ownerIntegrationReader) ActiveCallbacks() int { return 0 }
func (*ownerIntegrationReader) OutboxPending() int   { return 0 }
func (r *ownerIntegrationReader) DirectChildTools(_ context.Context, _ map[string]any) (map[string]any, error) {
	r.calls++
	return map[string]any{"structuredContent": map[string]any{"tools": []map[string]any{{"name": "sample_tool", "inputSchema": ownerIntegrationSchema()}}}}, nil
}

func ownerIntegrationSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": "exact schema marker"}}, "required": []any{"query"}, "additionalProperties": false}
}

func TestChildSchemaOwnerRealHandlersNoQueue(t *testing.T) {
	const token = "schema-owner-test"
	owner := New(Config{ConfigDir: t.TempDir(), CtlToken: token, DefaultTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { owner.Close() })
	reader := &ownerIntegrationReader{}
	parent := Agent{AgentID: "shell:peer", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true, "mcp_agents": []any{map[string]any{"ref": "sample", "enabled": true, "name": "sample", "transport": "streamable-http"}}}}
	owner.mu.Lock()
	owner.agents[parent.AgentID] = &parent
	if owner.localExecutors == nil {
		owner.localExecutors = make(map[string]map[string]string)
	}
	owner.localExecutors[parent.AgentID] = map[string]string{"server_id": "fixture"}
	owner.nodeRuntime = reader
	owner.mu.Unlock()
	peer := httptest.NewServer(owner.Handler())
	t.Cleanup(peer.Close)
	ingress := peerTestHub(t, token, map[string]string{parent.AgentID: peer.URL})
	ingress.mu.Lock()
	ingress.agents[parent.AgentID] = &parent
	ingress.mu.Unlock()
	child := childMCPAgents(parent)[0]
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "schema", "arguments": map[string]any{"target": child.AgentID, "detail": "full"}}})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	ingress.Handler().ServeHTTP(rec, req)
	var reply map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("invalid response %d: %s", rec.Code, rec.Body.String())
	}
	result := mapValue(mapValue(reply["result"])["structuredContent"])
	tools := searchChildToolMaps(mapValue(result["response"])["tools"])
	if rec.Code != http.StatusOK || firstString(result, "status") != "completed" || len(tools) != 1 {
		t.Errorf("real handlers did not return schema: HTTP %d %#v", rec.Code, reply)
	} else if !reflect.DeepEqual(tools[0]["inputSchema"], ownerIntegrationSchema()) {
		t.Errorf("inputSchema changed: got %#v want %#v", tools[0]["inputSchema"], ownerIntegrationSchema())
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if reader.calls != 1 || len(owner.shellJobs) != 0 || len(owner.relayJobs) != 0 || len(owner.shellQueues) != 0 {
		t.Errorf("owner used durable queue: reader calls=%d shellJobs=%d relayJobs=%d shellQueues=%d", reader.calls, len(owner.shellJobs), len(owner.relayJobs), len(owner.shellQueues))
	}
}

func TestChildSchemaForwardRequestsFullDetail(t *testing.T) {
	for _, method := range []string{"direct", "agent-tools"} {
		t.Run(method, func(t *testing.T) {
			s, child, _ := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if firstString(body, "detail") != "full" {
					t.Errorf("forwarded payload missing detail=full: %#v", body)
				}
				var schema any
				if firstString(body, "detail") == "full" {
					schema = ownerIntegrationSchema()
				}
				writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "response": map[string]any{"tools": []any{map[string]any{"name": "sample_tool", "inputSchema": schema}}}})
			})
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			r.Header.Set("Authorization", "Bearer schema-test-token")
			var response map[string]any
			if method == "direct" {
				response = mapValue(s.directChildSchemaForRequest(r, child)["response"])
			} else {
				result, err := s.agentToolsListForRequest(r, child)
				if err != nil {
					t.Fatalf("list failed: %v", err)
				}
				response = mapValue(result)
			}
			tools := searchChildToolMaps(response["tools"])
			if len(tools) != 1 || !reflect.DeepEqual(tools[0]["inputSchema"], ownerIntegrationSchema()) {
				t.Errorf("exact inputSchema lost: %#v", tools)
			}
		})
	}
}
