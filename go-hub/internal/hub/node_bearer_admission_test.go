package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Focused real HTTP/private-store regression. No fleet execution or runtime
// acceptance is claimed. Expected <5 seconds; maximum 30 seconds in cloud CI.
func bearerAdmissionNodes(t *testing.T) (*Server, *Server, string, string) {
	t.Helper()
	writer := membershipWriterNode(t, "writer-node")
	server := httptest.NewServer(writer.Handler())
	t.Cleanup(server.Close)
	writer.cfg.NodePublicURL = server.URL
	writer.ensureWriterSelfEntry()
	_, err := writer.updateAccessProfiles(func(profiles map[string]AccessProfile) error {
		profiles["fixture"] = AccessProfile{ID: "fixture", AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted,
			AllowedTargets: []string{"shell:member-a"}, AllowedTools: []string{"shell_exec"}, Version: 1, UpdatedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	writer.mu.Lock()
	writer.oauthClients["fixture-oauth"] = oauthClientMetadata{CreatedAt: time.Now().Unix(), ProfileID: "fixture"}
	err = writer.saveOAuthClientsStateLocked()
	writer.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := membershipHTTP(writer, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil); got.Code != http.StatusOK {
		t.Fatal("profile generation export", got.Code)
	}
	member, nodeID, nodeKey := membershipMemberNode(t, "member-a", server.URL, writer.authLocalID)
	member.cfg.OAuthClientSecret = "" // The reader never receives the writer signer.
	grant := membershipIssueGrant(t, writer, "shell:member-a")
	if got := membershipJoin(t, writer, grant, nodeID, nodeKey, "shell:member-a", "http://127.0.0.1:1"); got["__status"] != http.StatusOK {
		t.Fatal("join", got)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	member.membershipMemberTick(client, true)
	member.membershipMemberTick(client, false)
	token, err := writer.issueOAuthAccessToken("fixture-oauth", writer.cfg.MCPResource, "gptadmin.read gptadmin.exec")
	if err != nil {
		t.Fatal(err)
	}
	return writer, member, token, nodeKey
}

func TestNodeBearerAdmissionPreservesOriginalCallerAndLocalPolicy(t *testing.T) {
	writer, member, token, _ := bearerAdmissionNodes(t)
	if _, err := member.verifyJWT(token); err == nil {
		t.Fatal("fixture reader unexpectedly verifies writer signature locally")
	}
	request := httptest.NewRequest(http.MethodPost, "https://member.example/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	if !member.mcpAuth(response, request) {
		t.Fatalf("writer bearer rejected: %d %s", response.Code, response.Body.String())
	}
	claims, _ := request.Context().Value(authClaimsContextKey{}).(map[string]any)
	if claims["client_id"] != "fixture-oauth" || claims["sub"] != "admin" || claims["iss"] != writer.cfg.PublicOrigin {
		t.Fatal("original caller changed")
	}
	if AccessProfileIDFromRequest(request) != "fixture" {
		t.Fatal("actual replicated profile selection lost")
	}
	if authorizeToolCall(request, "shell:member-a", "shell_exec") != nil {
		t.Fatal("allowed call denied")
	}
	if authorizeToolCall(request, "shell:other", "shell_exec") == nil || authorizeToolCall(request, "shell:member-a", "mcp_manage") == nil {
		t.Fatal("local policy bypass")
	}
}

func TestNodeBearerAdmissionRejectsInvalidAndDisabledAuthority(t *testing.T) {
	writer, member, token, nodeKey := bearerAdmissionNodes(t)
	body, _ := json.Marshal(map[string]string{"token": token})
	for _, outer := range []string{"", "fixture-owner", token} {
		if got := membershipHTTP(writer, http.MethodPost, nodeBearerAdmissionPath, outer, body); got.Code != http.StatusUnauthorized {
			t.Fatalf("non-node outer accepted: %d", got.Code)
		}
	}
	// Different signer, expiry and audience must be checked by the writer.
	for _, claims := range []map[string]any{
		{"exp": time.Now().Add(-time.Hour).Unix()},
		{"exp": time.Now().Add(time.Hour).Unix(), "iss": "https://foreign.example", "aud": "https://foreign.example/mcp", "kid": writer.jwtKeyID()},
	} {
		bad, err := writer.signJWT(claims)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.verifyJWTForRequest(httptest.NewRequest("POST", "https://member.example/mcp", nil), bad); err == nil {
			t.Fatal("invalid writer bearer admitted")
		}
	}
	if _, err := member.verifyJWTForRequest(httptest.NewRequest("POST", "https://member.example/mcp", nil), token+"x"); err == nil {
		t.Fatal("tampered token admitted")
	}
	writer.membershipMu.Lock()
	writer.membership.Nodes[member.nodeIdentity.NodeID].State = nodeStateDisabled
	writer.membershipMu.Unlock()
	if got := admissionHTTP(writer, nodeKey, body); got.Code != http.StatusForbidden {
		t.Fatal("disabled node admitted", got.Code)
	}
}

func TestNodeBearerAdmissionBindsProofAndNeverRedirects(t *testing.T) {
	_, member, token, _ := bearerAdmissionNodes(t)
	state := member.authState
	good := nodeBearerAdmission{WriterID: state.WriterID, Generation: state.Generation,
		AuthorityDigest: state.Digest, TokenDigest: hashNodeSecret(token), Claims: map[string]any{"client_id": "fixture-oauth"}}
	for _, kind := range []string{"writer", "generation", "authority", "token", "oversized", "trailing", "redirect"} {
		t.Run(kind, func(t *testing.T) {
			proof := good
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "writer":
					proof.WriterID = "foreign"
				case "generation":
					proof.Generation++
				case "authority":
					proof.AuthorityDigest = "foreign"
				case "token":
					proof.TokenDigest = "foreign"
				case "oversized":
					w.Write(bytes.Repeat([]byte("x"), nodeBearerAdmissionLimit+1))
					return
				case "redirect":
					http.Redirect(w, r, "http://127.0.0.1:1", http.StatusTemporaryRedirect)
					return
				}
				json.NewEncoder(w).Encode(proof)
				if kind == "trailing" {
					w.Write([]byte(`{}`))
				}
			}))
			defer server.Close()
			member.nodeIdentity.WriterURL = server.URL
			if _, err := member.verifyJWTForRequest(httptest.NewRequest("POST", "https://member.example/mcp", nil), token); err == nil || !nodeBearerUnavailable(err) {
				t.Fatal("unbound proof admitted or misleading reauthentication", err)
			}
		})
	}
}

func TestNodeBearerAdmissionReleasesGateBeforeResponseAndBoundsAdmission(t *testing.T) {
	writer, member, token, nodeKey := bearerAdmissionNodes(t)
	raw, _ := json.Marshal(map[string]string{"token": token})
	w := &blockedAuthResponse{ResponseWriter: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	request := httptest.NewRequest("POST", nodeBearerAdmissionPath, bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+nodeKey)
	done := make(chan struct{})
	deadlineWriter := &admissionDeadlineWriter{ResponseWriter: w}
	go func() { writer.Handler().ServeHTTP(deadlineWriter, request); close(done) }()
	defer func() { close(w.release); <-done }()
	select {
	case <-w.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("response not reached")
	}
	if !writer.authMu.TryLock() {
		t.Fatal("admission holds generation lock across response write")
	}
	writer.authMu.Unlock()
	if !writer.mu.TryLock() {
		t.Fatal("admission holds hub lock across response write")
	}
	writer.mu.Unlock()
	if deadlineWriter.deadline.Sub(time.Now()) > 5*time.Second || deadlineWriter.deadline.IsZero() {
		t.Fatal("body read deadline absent")
	}
	for i := 0; i < cap(member.nodeBearerAdmissionSlots); i++ {
		member.nodeBearerAdmissionSlots <- struct{}{}
	}
	response := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "https://member.example/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if member.mcpAuth(response, r) || response.Code != http.StatusServiceUnavailable || response.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("busy writer falsely requests OAuth reconnect", response.Code)
	}
	for i := 0; i < cap(member.nodeBearerAdmissionSlots); i++ {
		<-member.nodeBearerAdmissionSlots
	}
	if got := admissionHTTP(writer, nodeKey, []byte(`{"token":"`+strings.Repeat("x", 17000)+`"}`)); got.Code != http.StatusBadRequest {
		t.Fatal("oversized token admitted", got.Code)
	}
}

// Local deadline spy only. Positive delegated authentication above uses a real
// httptest TCP server and the actual ingress wrapper, not this test boundary.
type admissionDeadlineWriter struct {
	http.ResponseWriter
	deadline time.Time
}

func (w *admissionDeadlineWriter) SetReadDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}
func admissionHTTP(writer *Server, nodeKey string, body []byte) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	w := &admissionDeadlineWriter{ResponseWriter: response}
	r := httptest.NewRequest("POST", nodeBearerAdmissionPath, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+nodeKey)
	writer.Handler().ServeHTTP(w, r)
	return response
}

func TestNodeBearerAdmissionMatchesSanitizedControlSnapshot(t *testing.T) {
	writer, member, token, _ := bearerAdmissionNodes(t)
	writer.mu.Lock()
	writer.managedMCP["legacy-owner"] = managedMCPToken{ID: "legacy-owner", TokenKind: "legacy_ctl", TokenValue: writer.cfg.CtlToken}
	err := writer.saveManagedMCPStateLocked()
	writer.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Control-only metadata is not replicated and must not break ordinary OAuth.
	r := httptest.NewRequest("POST", "https://member.example/mcp", nil)
	if _, err := member.verifyJWTForRequest(r, token); err != nil {
		t.Fatal("writer-private control record broke scoped admission", err)
	}
}
