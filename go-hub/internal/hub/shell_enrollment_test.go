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

func TestShellCredentialHashSurvivesPromotionAndHeartbeat(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), ShellToken: strings.Repeat("g", 32)})
	defer s.Close()
	const name = "android"
	token := strings.Repeat("a", 24) // older enrollment accepted 16+ bytes
	agent := &Agent{AgentID: "shell:" + name, Kind: "virtual_shell", Status: "awaiting_approval",
		Meta: map[string]any{"self_enrolled": true, "approved": false, shellTokenHashMetaKey: shellTokenHash(token)}}
	s.agents[agent.AgentID] = agent // replicated registry has no raw credential
	request := func(method, path, body, credential string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/queue/android?timeout=1", "", s.cfg.ShellToken); w.Code != 401 {
		t.Fatalf("fleet credential took over hash-bound queue: HTTP %d", w.Code)
	}
	if w := request("POST", "/heartbeat", `{"name":"android"}`, token); w.Code != 200 || !strings.Contains(w.Body.String(), "awaiting_approval") {
		t.Fatalf("hash-bound pending heartbeat lost its enrollment: HTTP %d", w.Code)
	}
	if w := request("GET", "/queue/android?timeout=1", "", token); w.Code != 200 || !strings.Contains(w.Body.String(), "awaiting_approval") {
		t.Fatalf("hash-bound poll did not preserve pending state: HTTP %d", w.Code)
	}
	if a := s.agents[agent.AgentID]; firstString(a.Meta, shellTokenHashMetaKey) != shellTokenHash(token) || truthy(a.Meta["approved"]) {
		t.Fatal("heartbeat replaced the credential or approved itself")
	}
}
