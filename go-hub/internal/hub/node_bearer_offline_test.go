package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A separately provisioned shared signer is an explicit HAOS failover option.
// Only writer transport failure or the reader's 409 self-route can use it;
// wrong proofs, disabled members, and invalid snapshots remain fail closed.
func TestReaderOfflineSharedSignerAdmission(t *testing.T) {
	writer, member, token, _ := bearerAdmissionNodes(t)
	member.cfg.OAuthClientSecret = writer.cfg.OAuthClientSecret
	member.cfg.PublicOrigin = writer.cfg.PublicOrigin
	member.cfg.MCPResource = writer.cfg.MCPResource
	member.nodeIdentity.WriterURL = "http://127.0.0.1:1"
	request := func(token string) (*http.Request, *httptest.ResponseRecorder) {
		r := httptest.NewRequest("POST", writer.cfg.PublicOrigin+"/mcp", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r, httptest.NewRecorder()
	}
	r, w := request(token)
	if member.mcpAuth(w, r) || w.Code != 503 {
		t.Fatalf("disabled fallback: %d", w.Code)
	}
	member.cfg.ReaderOfflineSharedSigner = true
	r, w = request(token)
	if !member.mcpAuth(w, r) {
		t.Fatalf("valid offline token: %d %s", w.Code, w.Body.String())
	}
	if AccessProfileIDFromRequest(r) != "fixture" || authorizeToolCall(r, "shell:other", "shell_exec") == nil {
		t.Fatal("replicated profile not enforced")
	}
	for _, bad := range []string{token + "x", "gptk_missing_value"} {
		r, w = request(bad)
		if member.mcpAuth(w, r) {
			t.Fatal("invalid credential accepted")
		}
	}
	expired, err := writer.signJWT(map[string]any{"sub": "admin", "scope": "gptadmin.read", "iss": writer.cfg.PublicOrigin, "aud": writer.cfg.MCPResource, "resource": writer.cfg.MCPResource, "iat": time.Now().Add(-time.Hour).Unix(), "exp": time.Now().Add(-time.Minute).Unix(), "kid": writer.jwtKeyID()})
	if err != nil {
		t.Fatal(err)
	}
	r, w = request(expired)
	if member.mcpAuth(w, r) {
		t.Fatal("expired credential accepted")
	}
	foreign, err := writer.signJWT(map[string]any{"sub": "admin", "scope": "gptadmin.read", "iss": "https://foreign.example", "aud": writer.cfg.MCPResource, "resource": writer.cfg.MCPResource, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "kid": writer.jwtKeyID()})
	if err != nil {
		t.Fatal(err)
	}
	r, w = request(foreign)
	if member.mcpAuth(w, r) {
		t.Fatal("foreign issuer accepted")
	}
	member.authState.Pending = true
	r, w = request(token)
	if member.mcpAuth(w, r) {
		t.Fatal("pending snapshot accepted")
	}
	member.authState.Pending = false
	original := member.authState.Digest
	member.authState.Digest = strings.Repeat("0", 64)
	r, w = request(token)
	if member.mcpAuth(w, r) {
		t.Fatal("invalid snapshot accepted")
	}
	member.authState.Digest = original
	member.cfg.OAuthClientSecret = "different-signer"
	r, w = request(token)
	if member.mcpAuth(w, r) {
		t.Fatal("different signer accepted")
	}
	member.cfg.OAuthClientSecret = writer.cfg.OAuthClientSecret
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "disabled", http.StatusForbidden) }))
	defer denied.Close()
	// TLS or protocol errors must not be mistaken for an offline writer.
	originalClient := member.nodeBearerAdmissionClient
	member.nodeIdentity.WriterURL = "https://writer.example"
	member.nodeBearerAdmissionClient = &http.Client{Transport: offlineErrorTransport{}}
	r, w = request(token)
	if member.mcpAuth(w, r) {
		t.Fatal("TLS/protocol failure bypassed")
	}
	member.nodeBearerAdmissionClient = originalClient
	member.nodeIdentity.WriterURL = denied.URL
	r, w = request(token)
	if member.mcpAuth(w, r) {
		t.Fatal("explicit denial bypassed")
	}
	conflict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "writer required", http.StatusConflict) }))
	defer conflict.Close()
	member.nodeIdentity.WriterURL = conflict.URL
	r, w = request(token)
	if !member.mcpAuth(w, r) {
		t.Fatalf("self-route 409 rejected: %d %s", w.Code, w.Body.String())
	}
	invalidProof := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"writer_id": "foreign"})
	}))
	defer invalidProof.Close()
	member.nodeIdentity.WriterURL = invalidProof.URL
	r, w = request(token)
	if member.mcpAuth(w, r) {
		t.Fatal("invalid writer proof bypassed")
	}
}

type offlineErrorTransport struct{}

func (offlineErrorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("TLS certificate verification failed")
}
