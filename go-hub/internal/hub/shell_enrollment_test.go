package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQueueSelfEnrollmentPinsLocallyGeneratedAgentCredential(t *testing.T) {
	s := New(Config{ShellToken: "shared-shell-token", ConfigDir: t.TempDir(), DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	const name = "windows-enrollment-canary"
	const token = "agent-local-token-0123456789abcdef"
	path := "/queue/" + name + "?timeout=1&server_id=shell%3A" + name + "&public_key=pub&fingerprint=fingerprint"

	first := httptest.NewRequest(http.MethodGet, path, nil)
	first.Header.Set("Authorization", "Bearer "+token)
	firstW := httptest.NewRecorder()
	s.Handler().ServeHTTP(firstW, first)
	if firstW.Code != http.StatusOK || !strings.Contains(firstW.Body.String(), `"status":"awaiting_approval"`) {
		t.Fatalf("self-enrollment status=%d body=%s", firstW.Code, firstW.Body.String())
	}

	s.mu.Lock()
	agent := s.agents["shell:"+name]
	digest, _ := agent.Meta["shell_token_hash"].(string)
	s.mu.Unlock()
	if agent == nil || digest == "" || digest == token {
		t.Fatalf("self-enrollment did not persist only the agent credential hash: %#v", agent)
	}

	wrong := httptest.NewRequest(http.MethodGet, path, nil)
	wrong.Header.Set("Authorization", "Bearer different-agent-token-0123456789")
	wrongW := httptest.NewRecorder()
	s.Handler().ServeHTTP(wrongW, wrong)
	if wrongW.Code != http.StatusUnauthorized {
		t.Fatalf("different credential status=%d body=%s", wrongW.Code, wrongW.Body.String())
	}

	same := httptest.NewRequest(http.MethodGet, path, nil)
	same.Header.Set("Authorization", "Bearer "+token)
	sameW := httptest.NewRecorder()
	s.Handler().ServeHTTP(sameW, same)
	if sameW.Code != http.StatusOK || !strings.Contains(sameW.Body.String(), `"status":"awaiting_approval"`) {
		t.Fatalf("same credential status=%d body=%s", sameW.Code, sameW.Body.String())
	}
}
