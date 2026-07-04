package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListAgentsUsesHubKind(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	r := httptest.NewRequest(http.MethodGet, "/mcp-relay/list_mcp_agents", nil)
	r.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Agents []Agent `json:"agents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) == 0 {
		t.Fatalf("no agents in response")
	}
	if got := body.Agents[0].Kind; got != "hub" {
		t.Fatalf("hub kind=%q, want hub", got)
	}
}

func TestRelayToolsRoundTrip(t *testing.T) {
	s := New(Config{CtlToken: "ctl", RelayAgentToken: "relay", DefaultTimeout: 1, PollMaxTimeout: 1})
	register := []byte(`{"agent_id":"demo","name":"Demo","capabilities":["tools/list","tools/call"]}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", bytes.NewReader(register))
	req.Header.Set("Authorization", "Bearer relay")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", w.Code, w.Body.String())
	}

	call := []byte(`{"target":"demo","tool_name":"ping","arguments":{"x":1},"background":true}`)
	req = httptest.NewRequest(http.MethodPost, "/mcp-relay/call_mcp_tool", bytes.NewReader(call))
	req.Header.Set("Authorization", "Bearer ctl")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("call status=%d body=%s", w.Code, w.Body.String())
	}
	var queued map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	jobID, _ := queued["job_id"].(string)
	if jobID == "" {
		t.Fatalf("missing job_id in %v", queued)
	}

	req = httptest.NewRequest(http.MethodGet, "/mcp-relay/poll/demo?timeout=1", nil)
	req.Header.Set("Authorization", "Bearer relay")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", w.Code, w.Body.String())
	}
	var job map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job["id"] != jobID || job["method"] != "tools/call" {
		t.Fatalf("bad relay job: %v", job)
	}
}
