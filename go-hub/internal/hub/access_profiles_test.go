package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedClientAccessProfilePersistsBindsAndEnforcesPolicy(t *testing.T) {
	cfg := Config{
		CtlToken:          "ctl",
		ConfigDir:         t.TempDir(),
		OAuthClientSecret: "oauth-secret",
		PublicOrigin:      "https://hub.example",
		MCPResource:       "https://hub.example",
		RegistryStateFile: filepath.Join(t.TempDir(), "registry.json"),
	}
	s := New(cfg)

	request := func(server *Server, method, path, token string, body any, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		var reader *strings.Reader
		if body == nil {
			reader = strings.NewReader("")
		} else {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = strings.NewReader(string(encoded))
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, req)
		return w
	}

	issued := request(s, http.MethodPost, "/admin/api/mcp/issue-token", "ctl", map[string]any{
		"client_id": "ops-client",
		"ttl_days":  7,
	}, nil)
	if issued.Code != http.StatusOK {
		t.Fatalf("issue token status=%d body=%s", issued.Code, issued.Body.String())
	}
	var issuedBody struct {
		TokenID     string `json:"token_id"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &issuedBody); err != nil {
		t.Fatal(err)
	}
	if issuedBody.TokenID == "" || issuedBody.AccessToken == "" {
		t.Fatalf("managed token response missing id or token: %s", issued.Body.String())
	}

	profile := map[string]any{
		"id":              "ops",
		"access_mode":     "full",
		"approval_mode":   "ask_before_write",
		"allowed_targets": []string{"hub"},
		"allowed_tools":   []string{"discover"},
	}
	profilePath := "/admin/api/access-profiles/ops"
	profilePut := request(s, http.MethodPut, profilePath, "ctl", profile, map[string]string{"If-Match": "*"})
	if profilePut.Code != http.StatusOK {
		t.Fatalf("PUT %s status=%d body=%s", profilePath, profilePut.Code, profilePut.Body.String())
	}
	if !strings.Contains(profilePut.Body.String(), `"approval_mode":"ask_before_write"`) {
		t.Fatalf("profile response omitted approval mode: %s", profilePut.Body.String())
	}

	bindingPath := "/admin/api/client-bindings/" + url.PathEscape(issuedBody.TokenID)
	bindingPut := request(s, http.MethodPut, bindingPath, "ctl", map[string]string{"profile_id": "ops"}, nil)
	if bindingPut.Code != http.StatusOK {
		t.Fatalf("PUT %s status=%d body=%s", bindingPath, bindingPut.Code, bindingPut.Body.String())
	}

	// A fresh Server must load both records from the shared config directory.
	restarted := New(cfg)
	profileGet := request(restarted, http.MethodGet, profilePath, "ctl", nil, nil)
	if profileGet.Code != http.StatusOK || !strings.Contains(profileGet.Body.String(), `"approval_mode":"ask_before_write"`) {
		t.Fatalf("persisted approval mode missing after restart: status=%d body=%s", profileGet.Code, profileGet.Body.String())
	}
	mcp := func(server *Server, token, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, req)
		return w
	}
	list := mcp(restarted, issuedBody.AccessToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if list.Code != http.StatusOK {
		t.Fatalf("managed tools/list status=%d body=%s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), `"name":"execute"`) {
		t.Fatalf("managed tools/list exposes forbidden execute: %s", list.Body.String())
	}
	if !strings.Contains(list.Body.String(), `"name":"discover"`) {
		t.Fatalf("managed tools/list hides allowed discover: %s", list.Body.String())
	}

	discover := mcp(restarted, issuedBody.AccessToken, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"discover","arguments":{}}}`)
	if discover.Code != http.StatusOK || !strings.Contains(discover.Body.String(), `"servers"`) || strings.Contains(discover.Body.String(), "error") {
		t.Fatalf("allowed discover failed status=%d body=%s", discover.Code, discover.Body.String())
	}

	forbidden := mcp(restarted, issuedBody.AccessToken, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"execute","arguments":{"target":"hub","tool":"discover"}}}`)
	if forbidden.Code != http.StatusOK || !strings.Contains(forbidden.Body.String(), "error") {
		t.Fatalf("forbidden execute status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}

	unbound := request(restarted, http.MethodPost, "/admin/api/mcp/issue-token", "ctl", map[string]any{
		"client_id": "unbound-client",
		"ttl_days":  7,
	}, nil)
	if unbound.Code != http.StatusOK {
		t.Fatalf("unbound token status=%d body=%s", unbound.Code, unbound.Body.String())
	}
	var unboundBody struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(unbound.Body.Bytes(), &unboundBody); err != nil {
		t.Fatal(err)
	}
	full := mcp(restarted, unboundBody.AccessToken, `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`)
	if full.Code != http.StatusOK || !strings.Contains(full.Body.String(), `"name":"execute"`) {
		t.Fatalf("unbound tools/list lost full behavior: status=%d body=%s", full.Code, full.Body.String())
	}
}

func TestAccessProfileCASInventoryAndAuthContext(t *testing.T) {
	cfg := Config{CtlToken: "ctl", ConfigDir: t.TempDir(), OAuthClientSecret: "oauth-secret", PublicOrigin: "https://hub.example", MCPResource: "https://hub.example"}
	s := New(cfg)

	request := func(method, path, token string, body any, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		var payload *strings.Reader
		if body == nil {
			payload = strings.NewReader("")
		} else {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			payload = strings.NewReader(string(encoded))
		}
		req := httptest.NewRequest(method, path, payload)
		req.Header.Set("Authorization", "Bearer "+token)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}

	profile := map[string]any{"id": "ops", "access_mode": "readonly", "allowed_targets": []string{"hub"}, "allowed_tools": []string{"discover"}}
	created := request(http.MethodPut, "/admin/api/access-profiles/ops", "ctl", profile, map[string]string{"If-Match": "*"})
	if created.Code != http.StatusOK || created.Header().Get("ETag") != `"1"` {
		t.Fatalf("create status=%d etag=%q body=%s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}
	got := request(http.MethodGet, "/admin/api/access-profiles/ops", "ctl", nil, nil)
	if got.Code != http.StatusOK || got.Header().Get("Cache-Control") != "no-store" || got.Header().Get("ETag") != `"1"` {
		t.Fatalf("GET status=%d cache=%q etag=%q body=%s", got.Code, got.Header().Get("Cache-Control"), got.Header().Get("ETag"), got.Body.String())
	}
	stale := request(http.MethodPut, "/admin/api/access-profiles/ops", "ctl", profile, map[string]string{"If-Match": `"0"`})
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale update status=%d body=%s", stale.Code, stale.Body.String())
	}

	issued := request(http.MethodPost, "/admin/api/mcp/issue-token", "ctl", map[string]any{"client_id": "ops-client", "ttl_days": 1}, nil)
	if issued.Code != http.StatusOK {
		t.Fatalf("issue status=%d body=%s", issued.Code, issued.Body.String())
	}
	var issuedBody struct {
		TokenID     string `json:"token_id"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &issuedBody); err != nil {
		t.Fatal(err)
	}
	binding := request(http.MethodPut, "/admin/api/client-bindings/"+url.PathEscape(issuedBody.TokenID), "ctl", map[string]string{"profile_id": "ops"}, nil)
	if binding.Code != http.StatusOK {
		t.Fatalf("binding status=%d body=%s", binding.Code, binding.Body.String())
	}

	clients := request(http.MethodGet, "/admin/api/clients", "ctl", nil, nil)
	if clients.Code != http.StatusOK || !strings.Contains(clients.Body.String(), `"profile_id":"ops"`) {
		t.Fatalf("inventory status=%d body=%s", clients.Code, clients.Body.String())
	}
	authRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	authRequest.Header.Set("Authorization", "Bearer "+issuedBody.AccessToken)
	authResponse := httptest.NewRecorder()
	if !s.mcpAuth(authResponse, authRequest) {
		t.Fatalf("managed JWT authentication failed: %s", authResponse.Body.String())
	}
	attached, ok := AccessProfileFromRequest(authRequest)
	if !ok || attached.ID != "ops" || attached.AccessMode != accessModeReadonly {
		t.Fatalf("auth profile=%+v attached=%v", attached, ok)
	}
}

func TestAccessProfileSharedConfigRefreshAndWorkspacePersistence(t *testing.T) {
	cfg := Config{
		CtlToken:     "ctl",
		ConfigDir:    t.TempDir(),
		PublicOrigin: "https://hub.example",
		MCPResource:  "https://hub.example",
	}
	first := New(cfg)
	second := New(cfg)

	workspace := map[string]any{
		"machine_id":       "machine-a",
		"workspace_path":   "/srv/workspace",
		"startup_document": "",
		"shell_target":     "shell:machine-a",
	}
	create := map[string]any{
		"id":              "z-profile",
		"name":            "Z profile",
		"access_mode":     "readonly",
		"allowed_targets": []string{"hub"},
		"allowed_tools":   []string{"discover"},
		"workspace_refs":  []any{workspace},
	}
	created := accessProfileTestRequest(t, first, http.MethodPut, "/admin/api/access-profiles/z-profile", create, map[string]string{"If-Match": "*"})
	if created.Code != http.StatusOK || created.Header().Get("ETag") != `"1"` {
		t.Fatalf("create status=%d etag=%q body=%s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}
	var createdProfile AccessProfile
	if err := json.Unmarshal(created.Body.Bytes(), &createdProfile); err != nil {
		t.Fatal(err)
	}
	if createdProfile.InstructionSetID != defaultInstructionSetID || len(createdProfile.WorkspaceRefs) != 1 || createdProfile.WorkspaceRefs[0].StartupDocument != "AGENTS.md" {
		t.Fatalf("profile defaults were not applied: %+v", createdProfile)
	}

	other := map[string]any{
		"id":              "a-profile",
		"name":            "A profile",
		"access_mode":     "full",
		"allowed_targets": []string{"hub"},
		"allowed_tools":   []string{"discover"},
	}
	if response := accessProfileTestRequest(t, first, http.MethodPut, "/admin/api/access-profiles/a-profile", other, map[string]string{"If-Match": "*"}); response.Code != http.StatusOK {
		t.Fatalf("second create status=%d body=%s", response.Code, response.Body.String())
	}

	collection := accessProfileTestRequest(t, second, http.MethodGet, "/admin/api/access-profiles", nil, nil)
	if collection.Code != http.StatusOK || collection.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("collection status=%d cache=%q body=%s", collection.Code, collection.Header().Get("Cache-Control"), collection.Body.String())
	}
	var listed struct {
		Profiles []AccessProfile `json:"profiles"`
	}
	if err := json.Unmarshal(collection.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Profiles) != 2 || listed.Profiles[0].ID != "a-profile" || listed.Profiles[1].ID != "z-profile" {
		t.Fatalf("profiles are not sorted or refreshed: %+v", listed.Profiles)
	}

	update := map[string]any{
		"id":              "z-profile",
		"name":            "Updated Z profile",
		"access_mode":     "readonly",
		"allowed_targets": []string{"hub"},
		"allowed_tools":   []string{"discover"},
		"workspace_refs":  []any{workspace},
	}
	updated := accessProfileTestRequest(t, first, http.MethodPut, "/admin/api/access-profiles/z-profile", update, map[string]string{"If-Match": `"1"`})
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"2"` {
		t.Fatalf("update status=%d etag=%q body=%s", updated.Code, updated.Header().Get("ETag"), updated.Body.String())
	}
	refreshed := accessProfileTestRequest(t, second, http.MethodGet, "/admin/api/access-profiles", nil, nil)
	if refreshed.Code != http.StatusOK || !strings.Contains(refreshed.Body.String(), "Updated Z profile") {
		t.Fatalf("second server did not refresh updated profile: status=%d body=%s", refreshed.Code, refreshed.Body.String())
	}
	stale := accessProfileTestRequest(t, second, http.MethodPut, "/admin/api/access-profiles/z-profile", update, map[string]string{"If-Match": `"1"`})
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale update status=%d body=%s", stale.Code, stale.Body.String())
	}

	restarted := New(cfg)
	persisted := accessProfileTestRequest(t, restarted, http.MethodGet, "/admin/api/access-profiles/z-profile", nil, nil)
	if persisted.Code != http.StatusOK || !strings.Contains(persisted.Body.String(), `"workspace_path":"/srv/workspace"`) {
		t.Fatalf("workspace reference did not survive New(cfg): status=%d body=%s", persisted.Code, persisted.Body.String())
	}

	tooLong := map[string]any{
		"id":              "invalid",
		"name":            "invalid",
		"access_mode":     "full",
		"allowed_targets": []string{"hub"},
		"allowed_tools":   []string{"discover"},
		"workspace_refs":  []any{map[string]any{"machine_id": strings.Repeat("x", accessProfileMaxStringBytes+1)}},
	}
	invalid := accessProfileTestRequest(t, first, http.MethodPut, "/admin/api/access-profiles/invalid", tooLong, map[string]string{"If-Match": "*"})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("oversized workspace reference status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestClientBindingDeleteUnbindsManagedTokenAndOAuthClient(t *testing.T) {
	cfg := Config{
		CtlToken:                 "ctl",
		ConfigDir:                t.TempDir(),
		OAuthClientSecret:        "oauth-secret",
		PublicOrigin:             "https://hub.example",
		MCPResource:              "https://hub.example",
		OAuthPermissiveRedirects: true,
		OAuthPermissiveResources: true,
	}
	s := New(cfg)

	profile := map[string]any{"id": "ops", "access_mode": "readonly", "allowed_targets": []string{"hub"}, "allowed_tools": []string{"discover"}}
	if response := accessProfileTestRequest(t, s, http.MethodPut, "/admin/api/access-profiles/ops", profile, map[string]string{"If-Match": "*"}); response.Code != http.StatusOK {
		t.Fatalf("profile status=%d body=%s", response.Code, response.Body.String())
	}
	issued := accessProfileTestRequest(t, s, http.MethodPost, "/admin/api/mcp/issue-token", map[string]any{"client_id": "managed", "ttl_days": 1}, nil)
	if issued.Code != http.StatusOK {
		t.Fatalf("issue status=%d body=%s", issued.Code, issued.Body.String())
	}
	var token struct {
		TokenID     string `json:"token_id"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	registered := accessProfileTestRequest(t, s, http.MethodPost, "/register", map[string]any{"redirect_uris": []string{"https://client.example/callback"}}, nil)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body.String())
	}
	var oauth struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &oauth); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{token.TokenID, oauth.ClientID} {
		binding := accessProfileTestRequest(t, s, http.MethodPut, "/admin/api/client-bindings/"+url.PathEscape(id), map[string]string{"profile_id": "ops"}, nil)
		if binding.Code != http.StatusOK {
			t.Fatalf("bind %q status=%d body=%s", id, binding.Code, binding.Body.String())
		}
	}

	deletePath := "/admin/api/client-bindings/" + url.PathEscape(token.TokenID)
	deleted := accessProfileTestRequest(t, s, http.MethodDelete, deletePath, nil, nil)
	if deleted.Code != http.StatusOK || deleted.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("delete status=%d cache=%q body=%s", deleted.Code, deleted.Header().Get("Cache-Control"), deleted.Body.String())
	}
	secondDelete := accessProfileTestRequest(t, s, http.MethodDelete, deletePath, nil, nil)
	if secondDelete.Code != http.StatusNotFound {
		t.Fatalf("second delete status=%d body=%s", secondDelete.Code, secondDelete.Body.String())
	}
	unknown := accessProfileTestRequest(t, s, http.MethodDelete, "/admin/api/client-bindings/unknown", nil, nil)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown delete status=%d body=%s", unknown.Code, unknown.Body.String())
	}

	// DELETE removes only the profile association; the managed credential remains usable.
	mcp := httptest.NewRecorder()
	mcpRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	mcpRequest.Header.Set("Authorization", "Bearer "+token.AccessToken)
	s.Handler().ServeHTTP(mcp, mcpRequest)
	if mcp.Code != http.StatusOK {
		t.Fatalf("unbound managed token status=%d body=%s", mcp.Code, mcp.Body.String())
	}

	restarted := New(cfg)
	clients := accessProfileTestRequest(t, restarted, http.MethodGet, "/admin/api/clients", nil, nil)
	if clients.Code != http.StatusOK {
		t.Fatalf("restarted inventory status=%d body=%s", clients.Code, clients.Body.String())
	}
	if !strings.Contains(clients.Body.String(), `"id":"`+token.TokenID+`"`) || !strings.Contains(clients.Body.String(), `"client_id":"`+oauth.ClientID+`"`) {
		t.Fatalf("credentials were not retained after unbind: %s", clients.Body.String())
	}
	if !strings.Contains(clients.Body.String(), `"profile_id":"ops"`) {
		t.Fatalf("OAuth profile binding was unexpectedly removed: %s", clients.Body.String())
	}

	oauthDelete := accessProfileTestRequest(t, restarted, http.MethodDelete, "/admin/api/client-bindings/"+url.PathEscape(oauth.ClientID), nil, nil)
	if oauthDelete.Code != http.StatusOK || oauthDelete.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("OAuth delete status=%d cache=%q body=%s", oauthDelete.Code, oauthDelete.Header().Get("Cache-Control"), oauthDelete.Body.String())
	}
	if second := accessProfileTestRequest(t, restarted, http.MethodDelete, "/admin/api/client-bindings/"+url.PathEscape(oauth.ClientID), nil, nil); second.Code != http.StatusNotFound {
		t.Fatalf("second OAuth delete status=%d body=%s", second.Code, second.Body.String())
	}
	finalInventory := accessProfileTestRequest(t, restarted, http.MethodGet, "/admin/api/clients", nil, nil)
	if finalInventory.Code != http.StatusOK || strings.Contains(finalInventory.Body.String(), `"profile_id":"ops"`) {
		t.Fatalf("OAuth profile binding survived delete: status=%d body=%s", finalInventory.Code, finalInventory.Body.String())
	}
}

func TestManagedMCPTokenRotationPreservesAccessProfileBinding(t *testing.T) {
	s := New(Config{
		CtlToken:          "ctl",
		ConfigDir:         t.TempDir(),
		OAuthClientSecret: "oauth-secret",
		PublicOrigin:      "https://hub.example",
		MCPResource:       "https://hub.example",
	})

	profile := map[string]any{
		"id":              "profile-a",
		"access_mode":     "full",
		"allowed_targets": []string{"hub"},
		"allowed_tools":   []string{"discover"},
	}
	created := accessProfileTestRequest(t, s, http.MethodPut, "/admin/api/access-profiles/profile-a", profile, map[string]string{"If-Match": "*"})
	if created.Code != http.StatusOK {
		t.Fatalf("create profile status=%d", created.Code)
	}

	issued := accessProfileTestRequest(t, s, http.MethodPost, "/admin/api/mcp/issue-token", map[string]any{"client_id": "managed", "ttl_days": 7}, nil)
	if issued.Code != http.StatusOK {
		t.Fatalf("issue token status=%d", issued.Code)
	}
	var issuedBody struct {
		TokenID string `json:"token_id"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &issuedBody); err != nil {
		t.Fatal(err)
	}
	if issuedBody.TokenID == "" {
		t.Fatal("issued token ID is empty")
	}

	bound := accessProfileTestRequest(t, s, http.MethodPut, "/admin/api/client-bindings/"+url.PathEscape(issuedBody.TokenID), map[string]string{"profile_id": "profile-a"}, nil)
	if bound.Code != http.StatusOK {
		t.Fatalf("bind token status=%d", bound.Code)
	}

	rotated := accessProfileTestRequest(t, s, http.MethodPost, "/admin/api/mcp/tokens/"+url.PathEscape(issuedBody.TokenID)+"/rotate", nil, nil)
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate token status=%d", rotated.Code)
	}
	var rotatedBody struct {
		TokenID     string `json:"token_id"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rotated.Body.Bytes(), &rotatedBody); err != nil {
		t.Fatal(err)
	}
	if rotatedBody.TokenID == "" || rotatedBody.AccessToken == "" {
		t.Fatal("replacement token response is incomplete")
	}

	s.mu.Lock()
	replacement := s.managedMCP[rotatedBody.TokenID]
	s.mu.Unlock()
	if replacement.ProfileID != "profile-a" {
		t.Fatalf("replacement profile ID = %q, want profile-a", replacement.ProfileID)
	}
	claims, err := s.verifyJWT(rotatedBody.AccessToken)
	if err != nil {
		t.Fatalf("verify replacement token: %v", err)
	}
	if claims["profile_id"] != "profile-a" {
		t.Fatalf("replacement profile claim = %v, want profile-a", claims["profile_id"])
	}
}

func accessProfileTestRequest(t *testing.T, server *Server, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var payload *strings.Reader
	if body == nil {
		payload = strings.NewReader("")
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = strings.NewReader(string(encoded))
	}
	req := httptest.NewRequest(method, path, payload)
	req.Header.Set("Authorization", "Bearer ctl")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	return w
}

func TestAccessProfileNormalizesNamedInstructionSetReference(t *testing.T) {
	normalized, err := normalizeAccessProfile(AccessProfile{
		ID:               "ops",
		InstructionSetID: "custom-operator-v2",
		AccessMode:       accessModeFull,
		Version:          1,
	})
	if err != nil || normalized.InstructionSetID != "custom-operator-v2" {
		t.Fatalf("normalizeAccessProfile=(%+v, %v), want named instruction set reference", normalized, err)
	}
	_, err = normalizeAccessProfile(AccessProfile{
		ID: "ops", AccessMode: accessModeFull, ApprovalMode: "unsafe", Version: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "approval_mode") {
		t.Fatalf("normalizeAccessProfile error = %v, want unsupported approval_mode", err)
	}
}
