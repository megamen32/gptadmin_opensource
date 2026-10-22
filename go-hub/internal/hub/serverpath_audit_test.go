package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCallerBanServerRequestAuditsDenialAndKeepsAllowedCall(t *testing.T) {
	banPath := filepath.Join(t.TempDir(), "codex-caller-ban.json")
	ban := `{"version":1,"enabled":true,"clients":["chatgpt-connector"],"targets":["hub"],"tools":["new_or_resume"],"denied_harness":"codex","reason":"Codex starts are temporarily restricted"}`
	if err := os.WriteFile(banPath, []byte(ban), 0o600); err != nil {
		t.Fatalf("write ban fixture: %v", err)
	}
	t.Setenv("GPTADMIN_CODEX_CALLER_BAN_FILE", banPath)
	resetCodexCallerBanCache()
	t.Cleanup(resetCodexCallerBanCache)

	s := New(Config{
		CtlToken:          "audit-readback-token",
		OAuthClientSecret: "test-oauth-secret",
		PublicOrigin:      "https://hub.example",
		MCPResource:       "https://hub.example",
	})
	token, err := s.signJWT(map[string]any{
		"sub": "chatgpt-connector", "client_id": "chatgpt-connector", "jti": "caller-ban-audit-test",
		"scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull,
		"aud": "https://hub.example", "resource": "https://hub.example",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "kid": defaultJWTKeyID,
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, req)
		return response
	}

	const privateMarker = "RAW_CALLER_BAN_ARGUMENT_MUST_NOT_BE_AUDITED"
	denied := call(`{"target":"hub","tool_name":"new_or_resume","arguments":{"harness":"codex","private_note":"` + privateMarker + `"}}`)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("banned server request status=%d body=%s, want 403", denied.Code, denied.Body.String())
	}
	if !strings.Contains(denied.Body.String(), "Codex starts are temporarily restricted") || strings.Contains(denied.Body.String(), privateMarker) {
		t.Fatalf("denial response lost its reason or exposed arguments: %s", denied.Body.String())
	}

	allowed := call(`{"target":"hub","tool_name":"hub_status","arguments":{"probe":"allowed"}}`)
	if allowed.Code != http.StatusOK {
		t.Fatalf("ordinary allowed request was blocked: status=%d body=%s", allowed.Code, allowed.Body.String())
	}

	auditRequest := httptest.NewRequest(http.MethodGet, "/admin/api/audit?name=tool_policy_decision&limit=100", nil)
	auditRequest.Header.Set("Authorization", "Bearer audit-readback-token")
	auditResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(auditResponse, auditRequest)
	if auditResponse.Code != http.StatusOK {
		t.Fatalf("audit readback status=%d body=%s", auditResponse.Code, auditResponse.Body.String())
	}
	var body struct {
		Events []auditEvent `json:"events"`
	}
	if err := json.Unmarshal(auditResponse.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode audit readback: %v", err)
	}
	var sawDenied, sawAllowed bool
	for _, event := range body.Events {
		if event.Fields["actor"] != "chatgpt-connector" {
			continue
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), privateMarker) {
			t.Fatalf("audit readback leaked raw denied arguments: %s", encoded)
		}
		if _, leaked := event.Fields["arguments"]; leaked {
			t.Fatalf("audit event contains raw arguments: %#v", event.Fields)
		}
		digest, ok := event.Fields["arguments_digest"].(string)
		if !ok || len(digest) != 64 {
			t.Fatalf("audit event lacks argument digest: %#v", event.Fields)
		}
		if event.Fields["target"] != "hub" {
			t.Fatalf("audit event has wrong target: %#v", event.Fields)
		}
		switch event.Fields["tool"] {
		case "new_or_resume":
			if event.Fields["policy_decision"] != "deny" || event.Fields["policy_reason"] != "Codex starts are temporarily restricted" || event.Fields["result_reference"] != "none" || event.Fields["status"] != float64(http.StatusForbidden) {
				t.Fatalf("caller-ban audit event is incomplete: %#v", event.Fields)
			}
			sawDenied = true
		case "hub_status":
			if event.Fields["policy_decision"] != "allow" || event.Fields["status"] != float64(http.StatusOK) {
				t.Fatalf("ordinary allowed call was incorrectly marked denied: %#v", event.Fields)
			}
			sawAllowed = true
		}
	}
	if !sawDenied {
		t.Fatal("audit readback did not contain the caller-ban denial")
	}
	if !sawAllowed {
		t.Fatal("audit readback did not contain the ordinary allowed call")
	}
}
