package hub

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func relayRequest(t *testing.T, s *Server, method, path, credential, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(payload))
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func enrollRelay(t *testing.T, s *Server, id string) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	digest := sha256.Sum256(der)
	encoded := base64.RawURLEncoding.EncodeToString(der)
	fp := hex.EncodeToString(digest[:])
	initial := relayRequest(t, s, http.MethodPost, "/mcp-relay/register", "admin-password", `{"agent_id":"`+id+`","public_key":"`+encoded+`","fingerprint":"`+fp+`"}`)
	if initial.Code != http.StatusOK {
		t.Fatalf("initial enrollment status=%d body=%s", initial.Code, initial.Body.String())
	}
	var pending map[string]any
	_ = json.Unmarshal(initial.Body.Bytes(), &pending)
	challenge := pending["challenge"].(string)
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, relayEnrollmentMessage(id, challenge)))
	proof := relayRequest(t, s, http.MethodPost, "/mcp-relay/register", "", `{"agent_id":"`+id+`","public_key":"`+encoded+`","fingerprint":"`+fp+`","signature":"`+sig+`"}`)
	if proof.Code != http.StatusOK || !bytes.Contains(proof.Body.Bytes(), []byte("awaiting_approval")) {
		t.Fatalf("proof status=%d body=%s", proof.Code, proof.Body.String())
	}
	return priv, `{"agent_id":"` + id + `","public_key":"` + encoded + `","fingerprint":"` + fp + `","signature":"` + sig + `"}`
}

func TestRelayEnrollmentRequiresIdentityApprovalAndIssuesAgentCredential(t *testing.T) {
	s := New(Config{AdminPassword: "admin-password", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	_, proof := enrollRelay(t, s, "first")
	if got := relayRequest(t, s, http.MethodGet, "/mcp-relay/poll/first?timeout=0", "admin-password", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("admin password accepted for poll: %d", got.Code)
	}
	result, status := s.callHubTool("approve_pending_server", map[string]any{"server_id": "first"})
	if status != http.StatusOK || result["status"] != "approved" {
		t.Fatalf("approve=%d %#v", status, result)
	}
	issued := relayRequest(t, s, http.MethodPost, "/mcp-relay/register", "", proof)
	var body map[string]any
	_ = json.Unmarshal(issued.Body.Bytes(), &body)
	credential, _ := body["relay_token"].(string)
	if issued.Code != http.StatusOK || credential == "" {
		t.Fatalf("issued=%d %s", issued.Code, issued.Body.String())
	}
	if got := relayRequest(t, s, http.MethodGet, "/mcp-relay/poll/first?timeout=0", credential, ""); got.Code != http.StatusOK {
		t.Fatalf("credential poll=%d", got.Code)
	}
	if got := relayRequest(t, s, http.MethodGet, "/mcp-relay/poll/other?timeout=0", credential, ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("cross-agent credential=%d", got.Code)
	}
}

func TestDebugVerifyWorkLowSecurityModeAutoApprovesSignedRelayEnrollment(t *testing.T) {
	s := New(Config{
		Addr:             "0.0.0.0:9001",
		AdminPassword:    "admin-password",
		DebugLowSecurity: true,
		DefaultTimeout:   time.Second,
		PollMaxTimeout:   time.Second,
	})
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	digest := sha256.Sum256(der)
	encoded := base64.RawURLEncoding.EncodeToString(der)
	fingerprint := hex.EncodeToString(digest[:])
	initial := relayRequest(t, s, http.MethodPost, "/mcp-relay/register", "admin-password", `{"agent_id":"debug","public_key":"`+encoded+`","fingerprint":"`+fingerprint+`"}`)
	if initial.Code != http.StatusOK {
		t.Fatalf("initial enrollment status=%d body=%s", initial.Code, initial.Body.String())
	}
	var pending map[string]any
	if err := json.Unmarshal(initial.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	challenge, _ := pending["challenge"].(string)
	if challenge == "" {
		t.Fatalf("missing enrollment challenge: %s", initial.Body.String())
	}
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, relayEnrollmentMessage("debug", challenge)))
	registered := relayRequest(t, s, http.MethodPost, "/mcp-relay/register", "", `{"agent_id":"debug","public_key":"`+encoded+`","fingerprint":"`+fingerprint+`","signature":"`+signature+`"}`)
	if registered.Code != http.StatusOK || !bytes.Contains(registered.Body.Bytes(), []byte(`"status":"registered"`)) || !bytes.Contains(registered.Body.Bytes(), []byte(`"relay_token":"gptr_`)) {
		t.Fatalf("debug enrollment status=%d body=%s", registered.Code, registered.Body.String())
	}
}

func TestRelayEnrollmentCannotReplaceExistingIdentityAndPersistsDigestOnly(t *testing.T) {
	state := t.TempDir() + "/registry.json"
	cfg := Config{AdminPassword: "admin-password", RegistryStateFile: state, DefaultTimeout: time.Second, PollMaxTimeout: time.Second}
	s := New(cfg)
	_, proof := enrollRelay(t, s, "durable")
	if got := relayRequest(t, s, http.MethodPost, "/mcp-relay/register", "admin-password", `{"agent_id":"durable","public_key":"bad","fingerprint":"bad"}`); got.Code != http.StatusConflict {
		t.Fatalf("takeover=%d", got.Code)
	}
	_, _ = s.callHubTool("approve_pending_server", map[string]any{"server_id": "durable"})
	issued := relayRequest(t, s, http.MethodPost, "/mcp-relay/register", "", proof)
	var body map[string]any
	_ = json.Unmarshal(issued.Body.Bytes(), &body)
	credential := body["relay_token"].(string)
	persisted, _ := os.ReadFile(state)
	if bytes.Contains(persisted, []byte(credential)) {
		t.Fatal("raw credential persisted")
	}
	restarted := New(cfg)
	if got := relayRequest(t, restarted, http.MethodGet, "/mcp-relay/poll/durable?timeout=0", credential, ""); got.Code != http.StatusOK {
		t.Fatalf("restart=%d", got.Code)
	}
}
