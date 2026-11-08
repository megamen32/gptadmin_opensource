package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These regressions exercise the real resource handlers and access policy in
// process. They do not establish deployment or client-picker acceptance.
func newResourceACLServer(t *testing.T) *Server {
	t.Helper()
	s := New(Config{
		ConfigDir: t.TempDir(), CtlToken: "resource-test-ctl",
		OAuthClientSecret: "resource-test-signing-secret",
		PublicOrigin:      "https://hub.example", MCPResource: "https://hub.example",
		DefaultTimeout: time.Second,
	})
	t.Cleanup(func() { _ = s.Close() })
	s.mu.Lock()
	for _, id := range []string{"shell:visible", "shell:hidden"} {
		s.agents[id] = &Agent{
			AgentID: id, Name: id + " private name", Kind: "virtual_shell",
			Status: "online", LastSeen: nowFloat(),
			Meta: map[string]any{"approved": true},
		}
	}
	s.mu.Unlock()
	return s
}

func resourceACLRequest(principal, profileID string, targets ...string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer test-"+principal)
	r = requestWithAuthClaims(r, map[string]any{
		"sub": principal, "client_id": principal, "jti": "token-" + principal,
		"scope": "gptadmin.read gptadmin.exec",
	})
	return requestWithAccessProfile(r, AccessProfile{
		ID: profileID, Version: 1, AccessMode: accessModeFull,
		ApprovalMode:   approvalModeUnrestricted,
		AllowedTargets: targets, AllowedTools: []string{"*"},
	})
}

func resourceServerIDs(t *testing.T, resource map[string]any) map[string]bool {
	t.Helper()
	contents := outputRows(resource["contents"])
	if len(contents) != 1 {
		t.Fatalf("expected one resource content, got %v", resource)
	}
	var body struct {
		Servers []struct {
			ID string `json:"server_id"`
		} `json:"servers"`
	}
	if err := json.Unmarshal([]byte(firstString(mapValue(contents[0]), "text")), &body); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool, len(body.Servers))
	for _, server := range body.Servers {
		ids[server.ID] = true
	}
	return ids
}

func TestResourceInventoryFiltersTargetACLAndCaller(t *testing.T) {
	s := newResourceACLServer(t)
	for _, uri := range []string{"gptadmin://servers", "gptadmin://agents"} {
		t.Run(uri, func(t *testing.T) {
			for _, test := range []struct {
				principal string
				allowed   string
				denied    string
			}{
				{"alice", "shell:visible", "shell:hidden"},
				{"bob", "shell:hidden", "shell:visible"},
			} {
				r := resourceACLRequest(test.principal, test.principal, test.allowed)
				ids := resourceServerIDs(t, s.appsSDKResourceRead(r, uri))
				if len(ids) != 1 || !ids[test.allowed] || ids[test.denied] {
					t.Errorf("%s resource inventory ignored target policy: %v", test.principal, ids)
				}
			}
		})
	}
}

func TestResourceInventoryRequiresPersonalConnectionOwner(t *testing.T) {
	s := newResourceACLServer(t)
	childID := "mcp:shell:visible:private"
	s.mu.Lock()
	s.agents["shell:visible"].Meta["mcp_agents"] = []any{map[string]any{
		"ref": "private", "name": "private MCP", "enabled": true,
		"owner_profile_id": "owner", "transport": "streamable-http",
	}}
	s.mu.Unlock()
	owner := resourceACLRequest("alice", "owner", "*")
	connected := owner.WithContext(context.WithValue(owner.Context(), connectionContextKey{}, connectionScope{
		Target: childID, Key: "owner-connection", Instance: "generation",
	}))
	for _, test := range []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"owner with exact connection", connected, true},
		{"same owner without connection", owner, false},
		{"another principal without connection", resourceACLRequest("bob", "owner", "*"), false},
		{"foreign owner", resourceACLRequest("charlie", "foreign", "*"), false},
		{"unbound internal inventory", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ids := resourceServerIDs(t, s.appsSDKResourceRead(test.r, "gptadmin://servers"))
			if ids[childID] != test.want {
				t.Fatalf("personal resource inventory visibility=%v, want %v", ids[childID], test.want)
			}
			if !ids["shell:visible"] {
				t.Fatal("shared authorized parent disappeared")
			}
		})
	}
}

func TestAgentResourcesRejectDeniedTargetBeforeReturningContent(t *testing.T) {
	s := newResourceACLServer(t)
	s.mu.Lock()
	hidden := *s.agents["shell:hidden"]
	s.mu.Unlock()
	r := resourceACLRequest("alice", "limited", "shell:visible")
	for _, method := range []string{"resources/list", "resources/read"} {
		t.Run(method, func(t *testing.T) {
			result, rpcErr, _ := s.agentMCPJSONRPC(r, hidden, map[string]any{
				"method": method, "params": map[string]any{"uri": "gptadmin://server/shell-hidden"},
			})
			if result != nil || rpcErr == nil {
				t.Fatalf("denied target returned resource data: result=%v error=%v", result, rpcErr)
			}
		})
	}
	shell, relay := policyJobCounts(s)
	if shell != 0 || relay != 0 {
		t.Fatalf("denied resource request dispatched work: shell=%d relay=%d", shell, relay)
	}
	// The Hub remains a facade: a target-scoped profile can read its filtered
	// inventory through either Hub entry point without an unrelated Hub grant.
	result, rpcErr := s.agentResourceRead(r, Agent{AgentID: "hub"}, "gptadmin://servers")
	if rpcErr != nil || !resourceServerIDs(t, mapValue(result))["shell:visible"] {
		t.Fatalf("Hub resource facade denied authorized inventory: result=%v error=%v", result, rpcErr)
	}
}

func TestResourceInventoryRechecksRevokedProfileThroughMCP(t *testing.T) {
	s := newResourceACLServer(t)
	profile := AccessProfile{
		ID: "resource-reader", Version: 1, AccessMode: accessModeFull,
		ApprovalMode:   approvalModeUnrestricted,
		AllowedTargets: []string{"shell:visible"}, AllowedTools: []string{"*"},
	}
	if err := saveAccessProfilesState(s.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
		t.Fatal(err)
	}
	token, err := s.signJWT(map[string]any{
		"sub": "resource-reader", "client_id": "resource-reader",
		"jti": "resource-reader-token", "profile_id": profile.ID,
		"scope": "gptadmin.read gptadmin.exec", "aud": "https://hub.example",
		"resource": "https://hub.example", "exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(), "kid": defaultJWTKeyID,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	call := func(endpoint, method, uri string) (map[string]any, map[string]any) {
		t.Helper()
		body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": map[string]any{"uri": uri}}
		r := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(mustJSON(t, body))))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s HTTP status %d", endpoint, method, w.Code)
		}
		var reply map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		return mapValue(reply["result"]), mapValue(reply["error"])
	}
	for _, endpoint := range []string{"/mcp", "/server/hub/mcp"} {
		result, rpcErr := call(endpoint, "resources/read", "gptadmin://servers")
		if len(rpcErr) != 0 {
			t.Fatalf("initial authorized read failed: %v", rpcErr)
		}
		ids := resourceServerIDs(t, result)
		if len(ids) != 1 || !ids["shell:visible"] {
			t.Errorf("initial resource inventory ignored profile at %s: %v", endpoint, ids)
		}
	}
	profile.Version++
	profile.AllowedTargets = []string{"shell:hidden"}
	if err := saveAccessProfilesState(s.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/mcp", "/server/hub/mcp"} {
		result, rpcErr := call(endpoint, "resources/read", "gptadmin://servers")
		if len(rpcErr) != 0 {
			t.Fatalf("updated profile could not read current inventory: %v", rpcErr)
		}
		ids := resourceServerIDs(t, result)
		if len(ids) != 1 || ids["shell:visible"] || !ids["shell:hidden"] {
			t.Errorf("revoked target remained visible at %s: %v", endpoint, ids)
		}
	}
	for _, method := range []string{"resources/list", "resources/read"} {
		result, rpcErr := call("/server/shell-visible/mcp", method, "gptadmin://server/shell-visible")
		if len(result) != 0 || len(rpcErr) == 0 {
			t.Errorf("%s still accessed the revoked target: result=%v error=%v", method, result, rpcErr)
		}
	}
}
