package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These tests exercise the local browser admission adapter. The SSO response
// and task completion are fixtures; deployed Node/SSO acceptance is separate.
type browserGrepMeshFixture struct {
	server                  *Server
	sso                     *httptest.Server
	authorized              atomic.Bool
	owner, reader           managedMCPToken
	ownerToken, readerToken string
	profile                 AccessProfile
	target                  string
}

func newBrowserGrepMeshFixture(t *testing.T) *browserGrepMeshFixture {
	t.Helper()
	f := &browserGrepMeshFixture{target: "mcp:shell:fixture:GrepMesh"}
	f.authorized.Store(true)
	f.sso = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/check" || !f.authorized.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Forwarded-Host") != "grepmesh.example" {
			t.Error("SSO request did not use the configured origin host")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Header.Get("Cookie") {
		case "sso=owner-session":
			w.Header().Set("X-GPTAdmin-User", "owner")
		case "sso=reader-session":
			w.Header().Set("X-GPTAdmin-User", "reader")
		case "sso=unmapped-session":
			w.Header().Set("X-GPTAdmin-User", "unmapped")
		default:
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(f.sso.Close)
	t.Setenv("GREPMESH_BROWSER_SSO_URL", f.sso.URL+"/check")
	t.Setenv("GREPMESH_BROWSER_ORIGIN", "https://grepmesh.example")
	f.server = New(Config{OAuthClientSecret: "fixture-key", PublicOrigin: "https://hub.example", MCPResource: "https://hub.example", AdminPassword: "fixture-password", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	t.Cleanup(func() { _ = f.server.Close() })
	roots := []string{"/tmp/browser-allowed"}
	f.profile = AccessProfile{ID: "browser-reader", Version: 7, AccessMode: accessModeReadonly, ApprovalMode: approvalModeReadOnly, AllowedTargets: []string{f.target}, AllowedTools: []string{"search", "search_text", "read_text"}, ReadRoots: &roots}
	f.server.accessProfiles[f.profile.ID] = f.profile
	var err error
	f.ownerToken, f.owner, err = newManagedMCPToken("browser-owner", 1, "https://hub.example", "https://hub.example", accessModeReadonly, f.profile.ID, "client", time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	f.readerToken, f.reader, err = newManagedMCPToken("browser-reader", 1, "https://hub.example", "https://hub.example", accessModeReadonly, f.profile.ID, "client", time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	f.server.managedMCP[f.owner.ID] = f.owner
	f.server.managedMCP[f.reader.ID] = f.reader
	f.server.agents["shell:fixture"] = &Agent{AgentID: "shell:fixture", Kind: "virtual_shell", Status: "online", LastSeen: nowFloat(), Meta: map[string]any{
		"approved": true, "filesystem_acl_version": 1,
		"mcp_agents": []any{map[string]any{"ref": "GrepMesh", "name": "GrepMesh", "enabled": true, "transport": "stdio"}},
	}}
	bindings, _ := json.Marshal(map[string]string{"owner": f.owner.ID, "reader": f.reader.ID})
	t.Setenv("GREPMESH_BROWSER_TOKEN_BINDINGS", string(bindings))
	return f
}

func (f *browserGrepMeshFixture) request(t *testing.T, method, path, cookie string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		data = mustJSON(t, body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Cookie", cookie)
	r.Header.Set("Origin", "https://grepmesh.example")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("MCP-Protocol-Version", "2026-07-28")
	if message, ok := body.(map[string]any); ok {
		r.Header.Set("Mcp-Method", firstString(message, "method"))
		if name := mcpRoutingName(firstString(message, "method"), mapValue(message["params"])); name != "" {
			r.Header.Set("Mcp-Name", name)
		}
	}
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, r)
	return w
}

func browserGrepMeshBody(method string, params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "grepmesh-browser-unit", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}},
	}
	return map[string]any{"jsonrpc": "2.0", "id": "browser-unit", "method": method, "params": params}
}

func (f *browserGrepMeshFixture) seedTask(id, status string) {
	request := httptest.NewRequest(http.MethodPost, "/api/browser/grepmesh/mcp", nil)
	request.Header.Set("Cookie", "sso=owner-session")
	delegated, _, err := f.server.authenticateBrowserGrepMesh(request)
	if err != nil {
		panic(err)
	}
	caller := taskCallerFromRequest(delegated)
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	f.server.shellJobs[id] = &shellJob{ID: id, Server: "fixture", ToolName: "mcp_call", Arguments: map[string]any{"ref": "GrepMesh", "name": "read_text", "arguments": map[string]any{"path": "/tmp/browser-allowed/file"}},
		Status: status, CreatedAt: nowFloat(), Caller: caller,
		FilesystemACL: &filesystemACLPolicy{Version: 1, ProfileID: f.profile.ID, ProfileVersion: f.profile.Version},
		Result:        map[string]any{"text": "browser-private-result"}}
}

func TestBrowserGrepMeshReadOnlyPolicyKeepsRootAndTargetBoundary(t *testing.T) {
	roots := []string{"/tmp/browser-allowed"}
	profile := AccessProfile{ID: "bounded", Version: 1, AccessMode: accessModeReadonly, AllowedTargets: []string{"*"}, AllowedTools: []string{"*"}, ReadRoots: &roots}
	for _, name := range []string{"search_text", "read_text"} {
		r := requestWithAccessProfile(httptest.NewRequest(http.MethodPost, "/mcp", nil), profile)
		if err := authorizeToolAction(r, "mcp:shell:fixture:GrepMesh", name, map[string]any{}); err != nil {
			t.Fatalf("bounded GrepMesh read denied: %v", err)
		}
		if err := authorizeToolAction(r, "mcp:shell:fixture:Other", name, map[string]any{}); err == nil {
			t.Fatal("different child was admitted")
		}
		unbound := requestWithAuthClaims(httptest.NewRequest(http.MethodPost, "/mcp", nil), map[string]any{"access_mode": accessModeReadonly})
		if err := authorizeToolAction(unbound, "mcp:shell:fixture:GrepMesh", name, nil); err == nil {
			t.Fatal("unbound read was admitted")
		}
		for _, readRoots := range []*[]string{nil, {}} {
			noRoots := profile
			noRoots.ReadRoots = readRoots
			r = requestWithAccessProfile(httptest.NewRequest(http.MethodPost, "/mcp", nil), noRoots)
			if err := authorizeToolAction(r, "mcp:shell:fixture:GrepMesh", name, nil); err == nil {
				t.Fatal("absent or empty read roots were admitted")
			}
		}
		deniedTool := profile
		deniedTool.AllowedTools = []string{"search"}
		r = requestWithAccessProfile(httptest.NewRequest(http.MethodPost, "/mcp", nil), deniedTool)
		if err := authorizeToolAction(r, "mcp:shell:fixture:GrepMesh", name, nil); err == nil {
			t.Fatal("explicit tool restriction was bypassed")
		}
	}
}

func TestBrowserGrepMeshSessionUsesSSOIdentityAndLiveProfile(t *testing.T) {
	f := newBrowserGrepMeshFixture(t)
	w := f.request(t, http.MethodGet, "/api/browser/grepmesh/session", "sso=owner-session", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", w.Code, w.Body.String())
	}
	var session map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session["principal"] != "owner" || session["profile_id"] != f.profile.ID || intFromAny(session["profile_version"]) != 7 || session["roots_digest"] != filesystemACLRootsDigest(f.profile) || len(firstString(session, "authz_fingerprint")) != 64 {
		t.Fatalf("session lost verified scope: %v", session)
	}
	if strings.Contains(w.Body.String(), f.ownerToken) || strings.Contains(w.Body.String(), f.readerToken) {
		t.Fatal("session leaked a backend credential")
	}
	firstFingerprint := session["authz_fingerprint"]
	f.server.mu.Lock()
	profile := f.server.accessProfiles[f.profile.ID]
	profile.Version++
	f.server.accessProfiles[f.profile.ID] = profile
	f.server.mu.Unlock()
	w = f.request(t, http.MethodGet, "/api/browser/grepmesh/session", "sso=owner-session", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("updated session status=%d", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session["authz_fingerprint"] == firstFingerprint || intFromAny(session["profile_version"]) != 8 {
		t.Fatal("profile revision did not change the authorization fingerprint")
	}
}

func TestBrowserGrepMeshDoesNotTrustIncomingIdentityOrBearer(t *testing.T) {
	f := newBrowserGrepMeshFixture(t)
	r := httptest.NewRequest(http.MethodGet, "/api/browser/grepmesh/session", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer "+f.ownerToken)
	r.Header.Set("X-GPTAdmin-User", "owner")
	r.Header.Set("X-Auth-User", "owner")
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("headers replaced SSO: %d %s", w.Code, w.Body.String())
	}
	r.Header.Set("Cookie", "sso=reader-session")
	w = httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "\"principal\":\"reader\"") {
		t.Fatalf("incoming principal overrode verified SSO: %d %s", w.Code, w.Body.String())
	}
}

func TestBrowserGrepMeshRejectsUnboundRevokedExpiredAndDuplicateBindings(t *testing.T) {
	for _, kind := range []string{"unbound", "revoked", "expired", "missing-value", "duplicate-binding", "unmapped-user"} {
		t.Run(kind, func(t *testing.T) {
			f := newBrowserGrepMeshFixture(t)
			cookie := "sso=owner-session"
			expected := http.StatusForbidden
			f.server.mu.Lock()
			record := f.server.managedMCP[f.owner.ID]
			switch kind {
			case "unbound":
				record.ProfileID = ""
			case "revoked":
				record.RevokedAt = time.Now().Unix()
			case "expired":
				record.ExpiresAt = time.Now().Add(-time.Minute).Unix()
			case "missing-value":
				record.TokenValue = ""
			case "unmapped-user":
				cookie = "sso=unmapped-session"
			case "duplicate-binding":
				t.Setenv("GREPMESH_BROWSER_TOKEN_BINDINGS", string(mustJSON(t, map[string]string{"owner": f.owner.ID, "reader": f.owner.ID})))
				expected = http.StatusServiceUnavailable
			}
			f.server.managedMCP[f.owner.ID] = record
			f.server.mu.Unlock()
			w := f.request(t, http.MethodGet, "/api/browser/grepmesh/session", cookie, nil)
			if w.Code != expected {
				t.Fatalf("status=%d want=%d body=%s", w.Code, expected, w.Body.String())
			}
		})
	}
}

func TestBrowserGrepMeshRejectsRemoteAndCrossOriginIngress(t *testing.T) {
	f := newBrowserGrepMeshFixture(t)
	for _, tc := range []struct{ remote, origin string }{
		{"192.0.2.1:1234", "https://grepmesh.example"},
		{"127.0.0.1:1234", "https://foreign.example"},
		{"127.0.0.1:1234", ""},
	} {
		body := browserGrepMeshBody("tasks/get", map[string]any{"taskId": "owned"})
		r := httptest.NewRequest(http.MethodPost, "/api/browser/grepmesh/mcp", bytes.NewReader(mustJSON(t, body)))
		r.RemoteAddr = tc.remote
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Cookie", "sso=owner-session")
		w := httptest.NewRecorder()
		f.server.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("remote=%s origin=%q status=%d", tc.remote, tc.origin, w.Code)
		}
	}
}

func TestBrowserGrepMeshRejectsGeneralExecutionSurfaces(t *testing.T) {
	f := newBrowserGrepMeshFixture(t)
	for _, tc := range []struct{ path, method, name string }{
		{"/api/browser/grepmesh/mcp", "tools/call", "exec"},
		{"/api/browser/grepmesh/mcp", "resources/read", ""},
		{"/api/browser/grepmesh/mcp", "tasks/update", ""},
		{"/api/browser/grepmesh/server/shell-fixture/mcp", "tools/call", "search"},
		{"/api/browser/grepmesh/server/mcp-shell-fixture-grepmesh/mcp", "tools/call", "delete"},
	} {
		body := browserGrepMeshBody(tc.method, map[string]any{"name": tc.name, "arguments": map[string]any{}})
		w := f.request(t, http.MethodPost, tc.path, "sso=owner-session", body)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d body=%s", tc.path, tc.method, w.Code, w.Body.String())
		}
	}
	if len(f.server.shellJobs) != 0 || len(f.server.relayJobs) != 0 {
		t.Fatal("refused browser call allocated a task")
	}
}

func TestBrowserGrepMeshRejectsForgedTransportPolicyBeforeAdmission(t *testing.T) {
	for _, key := range []string{"filesystem_acl", "_meta", "origin_host", "hop_count", "request_id"} {
		for _, value := range []any{nil, map[string]any{"read_roots": []any{"/"}}} {
			t.Run(key, func(t *testing.T) {
				f := newBrowserGrepMeshFixture(t)
				args := map[string]any{"path": "/tmp/browser-allowed/file", "host": "local", key: value}
				body := browserGrepMeshBody("tools/call", map[string]any{"name": "read_text", "arguments": args})
				w := f.request(t, http.MethodPost, "/api/browser/grepmesh/server/mcp-shell-fixture-grepmesh/mcp", "sso=owner-session", body)
				if w.Code != http.StatusForbidden || len(f.server.shellJobs) != 0 || len(f.server.relayJobs) != 0 {
					t.Fatalf("forged %s reached admission: %d %s", key, w.Code, w.Body.String())
				}
			})
		}
	}
	for _, value := range []any{nil, map[string]any{"read_roots": []any{"/"}}} {
		t.Run("params-meta", func(t *testing.T) {
			f := newBrowserGrepMeshFixture(t)
			body := browserGrepMeshBody("tools/call", map[string]any{"name": "read_text", "arguments": map[string]any{"path": "/tmp/browser-allowed/file", "host": "local"}})
			mapValue(mapValue(body["params"])["_meta"])["io.gptadmin/filesystem-acl"] = value
			w := f.request(t, http.MethodPost, "/api/browser/grepmesh/server/mcp-shell-fixture-grepmesh/mcp", "sso=owner-session", body)
			if w.Code != http.StatusForbidden || len(f.server.shellJobs) != 0 || len(f.server.relayJobs) != 0 {
				t.Fatalf("forged params policy reached admission: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestBrowserGrepMeshRejectsNonTaskSubscriptions(t *testing.T) {
	f := newBrowserGrepMeshFixture(t)
	for _, key := range []string{"resources", "tools", "prompts", "logging", "taskIds-extra"} {
		body := browserGrepMeshBody("subscriptions/listen", map[string]any{"notifications": map[string]any{"taskIds": []any{"absent-task"}, key: nil}})
		w := f.request(t, http.MethodPost, "/api/browser/grepmesh/mcp", "sso=owner-session", body)
		if w.Code != http.StatusForbidden {
			t.Fatalf("non-task subscription %s accepted: %d %s", key, w.Code, w.Body.String())
		}
	}
}

func TestBrowserGrepMeshRequiresNativeTaskProtocolBeforeAdmission(t *testing.T) {
	for _, kind := range []string{"missing-task-capability", "legacy-header", "legacy-body", "legacy-both", "missing-header", "missing-body-version"} {
		t.Run(kind, func(t *testing.T) {
			f := newBrowserGrepMeshFixture(t)
			body := browserGrepMeshBody("tools/call", map[string]any{"name": "read_text", "arguments": map[string]any{"path": "/tmp/browser-allowed/file", "host": "local"}})
			meta := mapValue(mapValue(body["params"])["_meta"])
			protocol := mcpProtocolVersion
			switch kind {
			case "missing-task-capability":
				// Change only the capability from the successful native request.
				delete(mapValue(mapValue(meta["io.modelcontextprotocol/clientCapabilities"])["extensions"]), "io.modelcontextprotocol/tasks")
			case "legacy-header":
				protocol = "2025-11-25"
			case "legacy-body":
				meta["io.modelcontextprotocol/protocolVersion"] = "2025-11-25"
			case "legacy-both":
				protocol = "2025-11-25"
				meta["io.modelcontextprotocol/protocolVersion"] = protocol
			case "missing-header":
				protocol = ""
			case "missing-body-version":
				delete(meta, "io.modelcontextprotocol/protocolVersion")
			}
			r := httptest.NewRequest(http.MethodPost, "/api/browser/grepmesh/server/mcp-shell-fixture-grepmesh/mcp", bytes.NewReader(mustJSON(t, body)))
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("Cookie", "sso=owner-session")
			r.Header.Set("Origin", "https://grepmesh.example")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("MCP-Protocol-Version", protocol)
			r.Header.Set("Mcp-Method", "tools/call")
			r.Header.Set("Mcp-Name", "read_text")
			w := httptest.NewRecorder()
			f.server.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || len(f.server.shellJobs) != 0 || len(f.server.relayJobs) != 0 {
				t.Fatalf("%s escaped native-task-only admission: status=%d shell_jobs=%d relay_jobs=%d body=%s", kind, w.Code, len(f.server.shellJobs), len(f.server.relayJobs), w.Body.String())
			}
		})
	}
}

func TestBrowserGrepMeshReadUsesNativeACLTaskAndIsolatesReceipts(t *testing.T) {
	f := newBrowserGrepMeshFixture(t)
	body := browserGrepMeshBody("tools/call", map[string]any{"name": "read_text", "arguments": map[string]any{"path": "/tmp/browser-allowed/file", "host": "local"}})
	w := f.request(t, http.MethodPost, "/api/browser/grepmesh/server/mcp-shell-fixture-grepmesh/mcp", "sso=owner-session", body)
	if w.Code != http.StatusOK {
		t.Fatalf("admission status=%d body=%s", w.Code, w.Body.String())
	}
	var reply map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	taskID := firstString(mapValue(reply["result"]), "taskId")
	job := f.server.shellJobs[taskID]
	if taskID == "" || job == nil || job.Caller == nil || job.Caller.TokenID != f.owner.ID || job.FilesystemACL == nil || job.FilesystemACL.ProfileID != f.profile.ID || job.FilesystemACL.RootsDigest != filesystemACLRootsDigest(f.profile) {
		t.Fatalf("browser did not use the native bound task: %s", w.Body.String())
	}
	f.seedTask("owned-receipt", "completed")
	for _, cookie := range []string{"sso=owner-session", "sso=reader-session"} {
		w = f.request(t, http.MethodPost, "/api/browser/grepmesh/mcp", cookie, browserGrepMeshBody("tasks/get", map[string]any{"taskId": "owned-receipt"}))
		visible := strings.Contains(w.Body.String(), "browser-private-result")
		if visible != (cookie == "sso=owner-session") {
			t.Fatalf("receipt isolation failed for %s: %s", cookie, w.Body.String())
		}
	}
	f.server.mu.Lock()
	profile := f.server.accessProfiles[f.profile.ID]
	profile.Version++
	f.server.accessProfiles[f.profile.ID] = profile
	f.server.mu.Unlock()
	w = f.request(t, http.MethodPost, "/api/browser/grepmesh/mcp", "sso=owner-session", browserGrepMeshBody("tasks/get", map[string]any{"taskId": "owned-receipt"}))
	if strings.Contains(w.Body.String(), "browser-private-result") {
		t.Fatal("stale task leaked after profile revision")
	}
}

func TestBrowserGrepMeshSubscriptionRevalidatesTokenAndSSO(t *testing.T) {
	for _, change := range []string{"token-revoked", "sso-revoked", "binding-changed"} {
		t.Run(change, func(t *testing.T) {
			f := newBrowserGrepMeshFixture(t)
			f.seedTask("stream-owned", "running")
			server := httptest.NewServer(f.server.Handler())
			t.Cleanup(server.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			body := browserGrepMeshBody("subscriptions/listen", map[string]any{"notifications": map[string]any{"taskIds": []any{"stream-owned"}}})
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/browser/grepmesh/mcp", bytes.NewReader(mustJSON(t, body)))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Cookie", "sso=owner-session")
			r.Header.Set("Origin", "https://grepmesh.example")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("MCP-Protocol-Version", "2026-07-28")
			r.Header.Set("Mcp-Method", "subscriptions/listen")
			r.Header.Set("Accept", "application/json, text/event-stream")
			response, err := server.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatalf("subscription status=%d content-type=%s", response.StatusCode, response.Header.Get("Content-Type"))
			}
			reader := bufio.NewReader(response.Body)
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(line, "notifications/tasks") {
					break
				}
			}
			f.server.mu.Lock()
			if change == "token-revoked" {
				record := f.server.managedMCP[f.owner.ID]
				record.RevokedAt = time.Now().Unix()
				f.server.managedMCP[f.owner.ID] = record
			}
			if change == "sso-revoked" {
				f.authorized.Store(false)
			}
			if change == "binding-changed" {
				t.Setenv("GREPMESH_BROWSER_TOKEN_BINDINGS", string(mustJSON(t, map[string]string{"owner": f.reader.ID})))
			}
			f.server.shellJobs["stream-owned"].Status = "completed"
			f.server.shellJobs["stream-owned"].DoneAt = nowFloat()
			f.server.mu.Unlock()
			tail, err := io.ReadAll(reader)
			if strings.Contains(string(tail), "browser-private-result") {
				t.Fatal("subscription leaked a result after revocation")
			}
			if err != nil {
				t.Fatalf("revoked subscription did not close: %v", err)
			}
		})
	}
}

// Exercise browser admission, persisted caller serialization and receipt routes.
func TestBrowserGrepMeshPersistedOwnerRejectsReboundToken(t *testing.T) {
	for _, rebind := range []bool{false, true} {
		name := "foreign-token"
		if rebind {
			name = "same-token-new-subject"
		}
		t.Run(name, func(t *testing.T) {
			f := newBrowserGrepMeshFixture(t)
			w := f.request(t, http.MethodPost, "/api/browser/grepmesh/server/mcp-shell-fixture-grepmesh/mcp", "sso=owner-session", browserGrepMeshBody("tools/call", map[string]any{"name": "read_text", "arguments": map[string]any{"path": "/tmp/browser-allowed/file", "host": "local"}}))
			var reply map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			id := firstString(mapValue(reply["result"]), "taskId")
			job := f.server.shellJobs[id]
			if id == "" || job == nil || job.Caller == nil {
				t.Fatalf("no admitted task: %s", w.Body.String())
			}
			// Reload precisely the persisted caller representation, not request context.
			raw := mustJSON(t, job.Caller)
			var restored taskCaller
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			job.Caller = &restored
			job.Status = "running"
			own := f.request(t, http.MethodPost, "/api/browser/grepmesh/mcp", "sso=owner-session", browserGrepMeshBody("tasks/get", map[string]any{"taskId": id}))
			var ownReply map[string]any
			if err := json.Unmarshal(own.Body.Bytes(), &ownReply); err != nil {
				t.Fatal(err)
			}
			if ownReply["error"] != nil || ownReply["result"] == nil {
				t.Fatalf("owner cannot read: %s", own.Body.String())
			}
			if rebind {
				t.Setenv("GREPMESH_BROWSER_TOKEN_BINDINGS", string(mustJSON(t, map[string]string{"reader": f.owner.ID})))
			}
			for _, method := range []string{"tasks/get", "tasks/cancel"} {
				denied := f.request(t, http.MethodPost, "/api/browser/grepmesh/mcp", "sso=reader-session", browserGrepMeshBody(method, map[string]any{"taskId": id}))
				var result map[string]any
				if err := json.Unmarshal(denied.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result["error"] == nil || result["result"] != nil {
					t.Errorf("foreign %s accepted: %s", method, denied.Body.String())
				}
			}
			if job.Status != "running" {
				t.Errorf("foreign cancellation mutated task: %s", job.Status)
			}
		})
	}
}

// A standalone peer owns a separate queue. Browser native tasks must not be
// acknowledged in the ingress queue, where that executor will never poll.
func TestBrowserGrepMeshRejectsStandalonePeerBeforeTaskAdmission(t *testing.T) {
	f := newBrowserGrepMeshFixture(t)
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer peer.Close()
	f.server.mu.Lock()
	f.server.nodePeers["shell:fixture"] = nodePeerRoute{URL: peer.URL}
	f.server.mu.Unlock()
	w := f.request(t, http.MethodPost, "/api/browser/grepmesh/server/mcp-shell-fixture-grepmesh/mcp", "sso=owner-session", browserGrepMeshBody("tools/call", map[string]any{"name": "read_text", "arguments": map[string]any{"path": "/tmp/browser-allowed/file", "host": "local"}}))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("remote task silently admitted: status=%d body=%s", w.Code, w.Body.String())
	}
	if len(f.server.shellJobs) != 0 || len(f.server.relayJobs) != 0 || calls.Load() != 0 {
		t.Fatal("unsupported remote browser task dispatched or allocated ingress receipt")
	}
	// An in-process pinned owner wins even if a peer route also exists.
	f.server.mu.Lock()
	f.server.localExecutors = map[string]map[string]string{"shell:fixture": {"server_id": "fixture"}}
	f.server.mu.Unlock()
	local := f.request(t, http.MethodPost, "/api/browser/grepmesh/server/mcp-shell-fixture-grepmesh/mcp", "sso=owner-session", browserGrepMeshBody("tools/call", map[string]any{"name": "read_text", "arguments": map[string]any{"path": "/tmp/browser-allowed/file", "host": "local"}}))
	if local.Code != http.StatusOK || len(f.server.shellJobs) != 1 || calls.Load() != 0 {
		t.Fatalf("local owner lost admission: %d %s", local.Code, local.Body.String())
	}
	for _, job := range f.server.shellJobs {
		if job.Caller == nil || job.Caller.BrowserSubject != "owner" || job.Caller.BrowserBinding == "" || job.Status != "queued" {
			t.Fatal("browser caller missing before dispatch")
		}
	}
}
