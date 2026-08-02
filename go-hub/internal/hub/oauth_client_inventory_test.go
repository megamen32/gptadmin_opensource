package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDynamicOAuthClientInventorySurvivesRestartAndBindsIssuedToken(t *testing.T) {
	cfg := Config{
		CtlToken:                 "ctl",
		AdminPassword:            "admin-password",
		OAuthClientSecret:        "oauth-signing-secret",
		ConfigDir:                t.TempDir(),
		PublicOrigin:             "https://hub.example",
		MCPResource:              "https://hub.example",
		OAuthPermissiveRedirects: true,
		OAuthPermissiveResources: true,
	}

	first := New(cfg)
	registered := oauthInventoryRequest(t, first, http.MethodPost, "/register", "", map[string]any{
		"redirect_uris": []string{"https://client.example/callback"},
	})
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body.String())
	}
	var registration struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &registration); err != nil {
		t.Fatal(err)
	}
	if registration.ClientID == "" || registration.ClientSecret == "" {
		t.Fatalf("registration missing client credentials: %s", registered.Body.String())
	}

	// Restart before any profile mutation: dynamic registration itself must be
	// durable and visible through the existing admin client inventory endpoint.
	restarted := New(cfg)
	clients := oauthInventoryRequest(t, restarted, http.MethodGet, "/admin/api/clients", cfg.CtlToken, nil)
	if clients.Code != http.StatusOK {
		t.Fatalf("client inventory status=%d body=%s", clients.Code, clients.Body.String())
	}
	if strings.Contains(clients.Body.String(), registration.ClientSecret) {
		t.Fatalf("client inventory leaked the raw OAuth client secret: %s", clients.Body.String())
	}
	var inventory struct {
		Clients []map[string]any `json:"clients"`
	}
	if err := json.Unmarshal(clients.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	client := findOAuthInventoryClient(inventory.Clients, registration.ClientID)
	if client == nil {
		t.Fatalf("registered OAuth client %q is missing from inventory: %s", registration.ClientID, clients.Body.String())
	}
	if _, exposed := client["client_secret"]; exposed {
		t.Fatalf("client inventory exposed client_secret: %v", client)
	}
	if _, present := client["access_mode"]; present {
		t.Fatalf("unscoped OAuth inventory must omit access_mode: %v", client)
	}

	profilePath := "/admin/api/access-profiles/oauth-profile"
	profile := oauthInventoryRequest(t, restarted, http.MethodPut, profilePath, cfg.CtlToken, map[string]any{
		"id":              "oauth-profile",
		"access_mode":     "readonly",
		"allowed_targets": []string{"hub"},
		"allowed_tools":   []string{"discover"},
	})
	if profile.Code != http.StatusOK {
		t.Fatalf("profile status=%d body=%s", profile.Code, profile.Body.String())
	}

	bindingPath := "/admin/api/client-bindings/" + url.PathEscape(registration.ClientID)
	binding := oauthInventoryRequest(t, restarted, http.MethodPut, bindingPath, cfg.CtlToken, map[string]any{
		"profile_id": "oauth-profile",
	})
	if binding.Code != http.StatusOK {
		t.Fatalf("client binding status=%d body=%s", binding.Code, binding.Body.String())
	}

	// The binding is keyed by OAuth client_id, not an in-memory token ID.
	restarted = New(cfg)
	clients = oauthInventoryRequest(t, restarted, http.MethodGet, "/admin/api/clients", cfg.CtlToken, nil)
	if clients.Code != http.StatusOK {
		t.Fatalf("restarted client inventory status=%d body=%s", clients.Code, clients.Body.String())
	}
	if strings.Contains(clients.Body.String(), registration.ClientSecret) {
		t.Fatalf("restarted client inventory leaked the raw OAuth client secret: %s", clients.Body.String())
	}
	if err := json.Unmarshal(clients.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	client = findOAuthInventoryClient(inventory.Clients, registration.ClientID)
	if client == nil || client["profile_id"] != "oauth-profile" {
		t.Fatalf("OAuth client profile binding was not persisted: %v", client)
	}

	verifier := "oauth-inventory-pkce-verifier"
	redirectURI := "https://client.example/callback"
	authorizeForm := url.Values{
		"client_id":             {registration.ClientID},
		"redirect_uri":          {redirectURI},
		"resource":              {cfg.MCPResource},
		"scope":                 {"gptadmin.read"},
		"password":              {cfg.AdminPassword},
		"code_challenge":        {oauthInventoryPKCE(verifier)},
		"code_challenge_method": {"S256"},
		"state":                 {"inventory-test"},
	}
	authorized := oauthInventoryRequestBody(t, restarted, http.MethodPost, "/authorize", "", authorizeForm.Encode(), "application/x-www-form-urlencoded")
	if authorized.Code != http.StatusFound {
		t.Fatalf("authorize status=%d body=%s", authorized.Code, authorized.Body.String())
	}
	code := oauthInventoryRedirectCode(t, authorized.Header().Get("Location"))

	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {registration.ClientID},
		"redirect_uri":  {redirectURI},
		"resource":      {cfg.MCPResource},
		"code_verifier": {verifier},
	}
	tokenResponse := oauthInventoryRequestBody(t, restarted, http.MethodPost, "/token", "", tokenForm.Encode(), "application/x-www-form-urlencoded")
	if tokenResponse.Code != http.StatusOK {
		t.Fatalf("token status=%d body=%s", tokenResponse.Code, tokenResponse.Body.String())
	}
	var tokenBody struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(tokenResponse.Body.Bytes(), &tokenBody); err != nil {
		t.Fatal(err)
	}
	claims, err := restarted.verifyJWT(tokenBody.AccessToken)
	if err != nil {
		t.Fatalf("issued OAuth access token is invalid: %v", err)
	}
	if claims["client_id"] != registration.ClientID || claims["profile_id"] != "oauth-profile" {
		t.Fatalf("issued OAuth access token lost client/profile context: %v", claims)
	}
}

func TestCanonicalOAuthEndpointsRequirePKCEAndBindClient(t *testing.T) {
	cfg := Config{
		AdminPassword:            "admin-password",
		OAuthClientSecret:        "oauth-signing-secret",
		PublicOrigin:             "https://hub.example",
		MCPResource:              "https://hub.example",
		OAuthPermissiveRedirects: true,
		OAuthPermissiveResources: true,
	}
	s := New(cfg)

	metadata := oauthInventoryRequestBody(t, s, http.MethodGet, "/.well-known/oauth-authorization-server", "", "", "")
	if metadata.Code != http.StatusOK || !strings.Contains(metadata.Body.String(), `"authorization_endpoint":"https://hub.example/oauth/authorize"`) || !strings.Contains(metadata.Body.String(), `"token_endpoint":"https://hub.example/oauth/token"`) {
		t.Fatalf("OAuth metadata did not advertise canonical endpoints: status=%d body=%s", metadata.Code, metadata.Body.String())
	}

	redirectURI := "https://client.example/callback"
	base := url.Values{
		"client_id":    {"client-a"},
		"redirect_uri": {redirectURI},
		"resource":     {cfg.MCPResource},
		"scope":        {"gptadmin.read"},
		"password":     {cfg.AdminPassword},
	}
	missingPKCE := oauthInventoryRequestBody(t, s, http.MethodPost, "/oauth/authorize", "", base.Encode(), "application/x-www-form-urlencoded")
	if missingPKCE.Code != http.StatusBadRequest || !strings.Contains(missingPKCE.Body.String(), "PKCE") {
		t.Fatalf("authorization without PKCE was accepted: status=%d body=%s", missingPKCE.Code, missingPKCE.Body.String())
	}

	verifier := "canonical-oauth-pkce-verifier"
	base.Set("code_challenge", oauthInventoryPKCE(verifier))
	base.Set("code_challenge_method", "S256")
	authorized := oauthInventoryRequestBody(t, s, http.MethodPost, "/oauth/authorize", "", base.Encode(), "application/x-www-form-urlencoded")
	if authorized.Code != http.StatusFound {
		t.Fatalf("canonical authorization status=%d body=%s", authorized.Code, authorized.Body.String())
	}
	code := oauthInventoryRedirectCode(t, authorized.Header().Get("Location"))
	wrongClient := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {"client-b"},
		"redirect_uri":  {"https://other.example/callback"},
		"resource":      {cfg.MCPResource},
		"code_verifier": {verifier},
	}
	token := oauthInventoryRequestBody(t, s, http.MethodPost, "/oauth/token", "", wrongClient.Encode(), "application/x-www-form-urlencoded")
	if token.Code != http.StatusBadRequest || !strings.Contains(token.Body.String(), "client or redirect") {
		t.Fatalf("authorization code was not bound to client and redirect: status=%d body=%s", token.Code, token.Body.String())
	}
}

func TestOAuthRefreshTokenSurvivesRestartForFiveYearsAndAuthenticatesMCPPaths(t *testing.T) {
	cfg := Config{
		AdminPassword:            "admin-password",
		OAuthClientSecret:        "oauth-signing-secret",
		ConfigDir:                t.TempDir(),
		PublicOrigin:             "https://hub.example",
		MCPResource:              "https://hub.example",
		OAuthPermissiveRedirects: true,
		OAuthPermissiveResources: true,
	}
	first := New(cfg)

	registered := oauthInventoryRequest(t, first, http.MethodPost, "/register", "", map[string]any{
		"redirect_uris": []string{"https://client.example/callback"},
	})
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body.String())
	}
	var registration struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &registration); err != nil {
		t.Fatal(err)
	}

	verifier := "five-year-refresh-verifier"
	authorize := url.Values{
		"client_id":             {registration.ClientID},
		"redirect_uri":          {"https://client.example/callback"},
		"resource":              {cfg.MCPResource},
		"scope":                 {"gptadmin.read gptadmin.exec"},
		"password":              {cfg.AdminPassword},
		"code_challenge":        {oauthInventoryPKCE(verifier)},
		"code_challenge_method": {"S256"},
	}
	authorized := oauthInventoryRequestBody(t, first, http.MethodPost, "/oauth/authorize", "", authorize.Encode(), "application/x-www-form-urlencoded")
	if authorized.Code != http.StatusFound {
		t.Fatalf("authorize status=%d body=%s", authorized.Code, authorized.Body.String())
	}

	code := oauthInventoryRedirectCode(t, authorized.Header().Get("Location"))
	initialForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {registration.ClientID},
		"redirect_uri":  {"https://client.example/callback"},
		"resource":      {cfg.MCPResource},
		"code_verifier": {verifier},
	}
	initial := oauthInventoryRequestBody(t, first, http.MethodPost, "/oauth/token", "", initialForm.Encode(), "application/x-www-form-urlencoded")
	if initial.Code != http.StatusOK {
		t.Fatalf("initial token status=%d body=%s", initial.Code, initial.Body.String())
	}
	var initialBody struct {
		RefreshToken          string `json:"refresh_token"`
		RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	}
	if err := json.Unmarshal(initial.Body.Bytes(), &initialBody); err != nil {
		t.Fatal(err)
	}
	if initialBody.RefreshToken == "" {
		t.Fatal("authorization-code response omitted refresh_token")
	}
	parts := strings.SplitN(initialBody.RefreshToken, "_", 3)
	if len(parts) != 3 {
		t.Fatalf("refresh token format=%q", initialBody.RefreshToken)
	}
	first.mu.Lock()
	record := first.managedMCP[parts[1]]
	first.mu.Unlock()
	if want := time.Unix(record.IssuedAt, 0).AddDate(5, 0, 0).Unix(); record.ExpiresAt != want {
		t.Fatalf("refresh expiry=%d, want %d", record.ExpiresAt, want)
	}
	if initialBody.RefreshTokenExpiresIn <= 0 {
		t.Fatalf("refresh_token_expires_in=%d, want positive", initialBody.RefreshTokenExpiresIn)
	}

	restarted := New(cfg)
	refreshForm := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {initialBody.RefreshToken},
		"client_id":     {registration.ClientID},
		"resource":      {cfg.MCPResource},
	}
	refreshed := oauthInventoryRequestBody(t, restarted, http.MethodPost, "/oauth/token", "", refreshForm.Encode(), "application/x-www-form-urlencoded")
	if refreshed.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", refreshed.Code, refreshed.Body.String())
	}
	var refreshedBody struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(refreshed.Body.Bytes(), &refreshedBody); err != nil {
		t.Fatal(err)
	}
	if refreshedBody.AccessToken == "" || refreshedBody.RefreshToken == "" || refreshedBody.RefreshToken == initialBody.RefreshToken {
		t.Fatalf("refresh response did not rotate credentials: %s", refreshed.Body.String())
	}
	reused := oauthInventoryRequestBody(t, restarted, http.MethodPost, "/oauth/token", "", refreshForm.Encode(), "application/x-www-form-urlencoded")
	if reused.Code != http.StatusBadRequest || !strings.Contains(reused.Body.String(), "invalid_grant") {
		t.Fatalf("rotated refresh token remained usable: status=%d body=%s", reused.Code, reused.Body.String())
	}

	for _, path := range []string{"/mcp", "/server/hub/mcp", "/mcp-relay/servers"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
		if path == "/mcp-relay/servers" {
			req = httptest.NewRequest(http.MethodGet, path, nil)
		}
		req.Header.Set("Authorization", "Bearer "+refreshedBody.AccessToken)
		response := httptest.NewRecorder()
		restarted.Handler().ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestOAuthMetadataAdvertisesRefreshAndOfflineAccess(t *testing.T) {
	s := New(Config{PublicOrigin: "https://hub.example", MCPResource: "https://hub.example"})
	rec := oauthInventoryRequest(t, s, http.MethodGet, "/.well-known/oauth-authorization-server", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata status=%d body=%s", rec.Code, rec.Body.String())
	}
	var metadata struct {
		GrantTypesSupported []string `json:"grant_types_supported"`
		ScopesSupported     []string `json:"scopes_supported"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(metadata.GrantTypesSupported, "refresh_token") {
		t.Fatalf("refresh_token grant missing: %v", metadata.GrantTypesSupported)
	}
	if !slices.Contains(metadata.ScopesSupported, "offline_access") {
		t.Fatalf("offline_access scope missing: %v", metadata.ScopesSupported)
	}
}

func oauthInventoryRequest(t *testing.T, server *Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	if body == nil {
		return oauthInventoryRequestBody(t, server, method, path, token, "", "")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return oauthInventoryRequestBody(t, server, method, path, token, string(encoded), "application/json")
}

func oauthInventoryRequestBody(t *testing.T, server *Server, method, path, token, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if method == http.MethodPut && strings.HasPrefix(path, "/admin/api/access-profiles/") {
		req.Header.Set("If-Match", "*")
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	return recorder
}

func findOAuthInventoryClient(clients []map[string]any, clientID string) map[string]any {
	for _, client := range clients {
		if client["client_id"] == clientID {
			return client
		}
	}
	return nil
}

func oauthInventoryPKCE(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func oauthInventoryRedirectCode(t *testing.T, location string) string {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse authorize redirect: %v", err)
	}
	code := parsed.Query().Get("code")
	if code == "" {
		t.Fatalf("authorize redirect missing code: %s", location)
	}
	return code
}
