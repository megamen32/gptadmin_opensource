package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func newSelfEnrollTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		cfg:                 Config{ShellToken: "shared-hub-shell-token-000000000"},
		agents:              map[string]*Agent{},
		localExecutorTokens: map[string]string{},
		localExecutors:      map[string]map[string]string{},
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func selfEnrollMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/queue/", s.requireShell(s.queue))
	return mux
}

func selfEnrollPoll(t *testing.T, ts *httptest.Server, name, token string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/queue/"+name+"?timeout=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp, body
}

func TestQueueSelfEnrollRegistersPendingAgentAndBindsToken(t *testing.T) {
	s := newSelfEnrollTestServer(t)
	ts := httptest.NewServer(selfEnrollMux(s))
	defer ts.Close()

	token := strings.Repeat("ab", 16) // 32 hex-ish chars, agent-minted
	resp, body := selfEnrollPoll(t, ts, "newpc", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first self-enroll poll: status %d, body %v", resp.StatusCode, body)
	}
	if body["status"] != "awaiting_approval" {
		t.Fatalf("self-enrolled agent must be awaiting_approval, got %v", body["status"])
	}
	agent := s.agents["shell:newpc"]
	if agent == nil {
		t.Fatal("agent was not registered")
	}
	if agent.Status != "awaiting_approval" {
		t.Fatalf("agent status = %q, want awaiting_approval", agent.Status)
	}
	if got := s.localExecutorTokens["shell:newpc"]; got != token {
		t.Fatalf("bound token = %q, want the agent-minted token", got)
	}
}

func TestQueueSelfEnrollRepollWithBoundToken(t *testing.T) {
	s := newSelfEnrollTestServer(t)
	ts := httptest.NewServer(selfEnrollMux(s))
	defer ts.Close()

	token := strings.Repeat("cd", 16)
	if _, body := selfEnrollPoll(t, ts, "repoll", token); body["status"] != "awaiting_approval" {
		t.Fatalf("first poll status = %v", body["status"])
	}
	resp, body := selfEnrollPoll(t, ts, "repoll", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bound-token repoll: status %d", resp.StatusCode)
	}
	if body["status"] != "awaiting_approval" {
		t.Fatalf("repoll status = %v, want awaiting_approval before approval", body["status"])
	}
}

func TestQueueSelfEnrollRejectsForeignTokenAfterBinding(t *testing.T) {
	s := newSelfEnrollTestServer(t)
	ts := httptest.NewServer(selfEnrollMux(s))
	defer ts.Close()

	if _, body := selfEnrollPoll(t, ts, "claimed", strings.Repeat("ef", 16)); body["status"] != "awaiting_approval" {
		t.Fatalf("first poll status = %v", body["status"])
	}
	resp, _ := selfEnrollPoll(t, ts, "claimed", strings.Repeat("99", 16))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("foreign token after binding: status %d, want 401", resp.StatusCode)
	}
}

func TestQueueSelfEnrollRefusesKnownAgentNames(t *testing.T) {
	s := newSelfEnrollTestServer(t)
	ts := httptest.NewServer(selfEnrollMux(s))
	defer ts.Close()

	s.mu.Lock()
	s.agents["shell:existing"] = &Agent{AgentID: "shell:existing", Name: "Shell: existing", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true}}
	s.mu.Unlock()

	resp, _ := selfEnrollPoll(t, ts, "existing", strings.Repeat("11", 16))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown token on known agent: status %d, want 401", resp.StatusCode)
	}
}

func TestQueueSelfEnrollResultPostUsesBoundToken(t *testing.T) {
	s := newSelfEnrollTestServer(t)
	ts := httptest.NewServer(selfEnrollMux(s))
	defer ts.Close()

	token := strings.Repeat("77", 16)
	if _, body := selfEnrollPoll(t, ts, "poster", token); body["status"] != "awaiting_approval" {
		t.Fatalf("first poll status = %v", body["status"])
	}
	// Empty body: the handler rejects it with 400 before touching job state;
	// the point of the test is only that requireShell authenticates the
	// bound credential instead of answering 401.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/queue/poster/result", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("bound token must authenticate result posts")
	}
}

func TestQueueSelfEnrollRejectsMalformedTokens(t *testing.T) {
	s := newSelfEnrollTestServer(t)
	ts := httptest.NewServer(selfEnrollMux(s))
	defer ts.Close()

	for _, token := range []string{"short", "has space inside it is long enough", strings.Repeat("<>", 20)} {
		resp, _ := selfEnrollPoll(t, ts, "badtoken", token)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("malformed token %q: status %d, want 401", token, resp.StatusCode)
		}
	}
	if s.agents["shell:badtoken"] != nil {
		t.Fatal("malformed token must not register an agent")
	}
}

func TestQueueSelfEnrollCannotSpoofBindHeader(t *testing.T) {
	s := newSelfEnrollTestServer(t)
	mux := selfEnrollMux(s)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	bearer := strings.Repeat("55", 16)
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/queue/spoof?timeout=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set(selfEnrollTokenHeader, "attacker-chosen-value-000000000")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := s.localExecutorTokens["shell:spoof"]; got != bearer {
		t.Fatalf("bound token = %q, want the bearer; spoofed header must be ignored", got)
	}
}

func TestSelfEnrollBindingsPersistThroughRegistryState(t *testing.T) {
	s := newSelfEnrollTestServer(t)
	s.cfg.RegistryStateFile = t.TempDir() + "/registry_state.json"

	token := strings.Repeat("33", 16)
	s.mu.Lock()
	s.agents["shell:keeper"] = &Agent{AgentID: "shell:keeper", Name: "Shell: keeper", Kind: "virtual_shell", Status: "awaiting_approval", LastSeen: 1, Meta: map[string]any{"self_enrolled": true}}
	s.localExecutorTokens["shell:keeper"] = token
	s.agents["shell:samebox"] = &Agent{AgentID: "shell:samebox", Name: "Shell: samebox", Kind: "virtual_shell", Status: "online", LastSeen: 1, Meta: map[string]any{}}
	s.localExecutorTokens["shell:samebox"] = "same-box-secret-re-registered-on-boot"
	if err := s.saveRegistryStateLocked(); err != nil {
		t.Fatalf("save: %v", err)
	}
	s.mu.Unlock()

	restored := newSelfEnrollTestServer(t)
	restored.cfg.RegistryStateFile = s.cfg.RegistryStateFile
	if err := restored.loadRegistryState(); err != nil {
		t.Fatalf("load: %v", err)
	}
	restored.mu.Lock()
	defer restored.mu.Unlock()
	if got := restored.localExecutorTokens["shell:keeper"]; got != token {
		t.Fatalf("self-enrolled token lost across restart: %q", got)
	}
	if _, ok := restored.localExecutorTokens["shell:samebox"]; ok {
		t.Fatal("same-box token must not be persisted over boot-time registration")
	}
}

func TestStandaloneAutoTrustApprovesHubCredentialAgent(t *testing.T) {
	s := newSelfEnrollTestServer(t)
	s.cfg.StandaloneAutoTrust = true
	ts := httptest.NewServer(selfEnrollMux(s))
	defer ts.Close()

	// The installer shares the Hub's own shell credential with the local
	// agent; on a standalone install that agent must be trusted at once.
	resp, body := selfEnrollPoll(t, ts, "localpc?os=linux&default_user=owner&public_key=pub&fingerprint=fp", "shared-hub-shell-token-000000000")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll: status %d", resp.StatusCode)
	}
	if body["status"] == "awaiting_approval" {
		t.Fatalf("standalone install must not park the local agent in pending: %v", body)
	}
	s.mu.Lock()
	agent := s.agents["shell:localpc"]
	s.mu.Unlock()
	if agent == nil || agent.Status == "awaiting_approval" || !truthyAny(agent.Meta["approved"]) {
		t.Fatalf("standalone agent should be approved online: %#v", agent)
	}
	if !truthyAny(agent.Meta["local_install"]) {
		t.Fatal("standalone agent should be tagged local_install")
	}
}

func TestFleetHubWithoutStandaloneStillGatesHubCredentialAgents(t *testing.T) {
	s := newSelfEnrollTestServer(t) // StandaloneAutoTrust not set
	ts := httptest.NewServer(selfEnrollMux(s))
	defer ts.Close()

	resp, body := selfEnrollPoll(t, ts, "fleetpc?os=linux&default_user=owner&public_key=pub&fingerprint=fp", "shared-hub-shell-token-000000000")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll: status %d", resp.StatusCode)
	}
	if body["status"] != "awaiting_approval" {
		t.Fatalf("fleet hub must keep the pending gate: %v", body)
	}
}
