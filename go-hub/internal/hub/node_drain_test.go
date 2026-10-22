package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func drainTestHub(t *testing.T, routes map[string]string) *Server {
	t.Helper()
	raw := "{}"
	if len(routes) > 0 {
		data, err := json.Marshal(routes)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(data)
	}
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "drain-owner", ShellToken: "drain-shell", NodePeersJSON: raw, DefaultTimeout: 30 * time.Second})
	t.Cleanup(func() { s.Close() })
	return s
}

func drainLocalExecutor(t *testing.T, s *Server) {
	t.Helper()
	if err := s.RegisterLocalExecutor("local", map[string]string{"server_id": "local-id", "public_key": "local-key", "fingerprint": "local-fingerprint"}, strings.Repeat("l", 32)); err != nil {
		t.Fatal(err)
	}
}

func drainAdminPost(s *Server, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/admin/api/node/drain", strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func drainAdminGet(s *Server, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/admin/api/node/drain", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func waitForDrainCondition(t *testing.T, s *Server, what string, cond func(NodeDrainStatus) bool) NodeDrainStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := s.NodeDrainStatus()
		if cond(status) {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("drain condition not met (%s): %+v", what, status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func completeLocalJob(t *testing.T, s *Server, jobID string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/queue/local/result", strings.NewReader(`{"id":"`+jobID+`","result":{"return_code":0,"stdout":"done"}}`))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("l", 32))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("executor result rejected: %d %s", w.Code, w.Body.String())
	}
}

type runtimeCountersStub struct{ callbacks, outbox int }

func (c runtimeCountersStub) ActiveCallbacks() int { return c.callbacks }
func (c runtimeCountersStub) OutboxPending() int   { return c.outbox }

func TestNodeDrainRejectsSubmissionsAndKeepsReads(t *testing.T) {
	var peerCalls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerCalls.Add(1)
		io.Copy(io.Discard, r.Body)
		writeJSON(w, http.StatusOK, map[string]any{"job_id": "peer-job", "status": "running"})
	}))
	defer peer.Close()
	s := drainTestHub(t, map[string]string{"shell:nodeB": peer.URL})
	drainLocalExecutor(t, s)

	// Before draining both submission paths work: one local job, one peer forward.
	if w := peerTestCall(s, `{"target":"shell:local","tool":"shell_exec","arguments":{"cmd":"printf before"},"background":true}`, "drain-owner", ""); w.Code != 200 {
		t.Fatalf("local submission failed pre-drain: %d %s", w.Code, w.Body.String())
	}
	if w := peerTestCall(s, `{"target":"shell:nodeB","tool":"shell_exec","arguments":{"cmd":"printf before"},"background":true}`, "drain-owner", ""); w.Code != 200 || peerCalls.Load() != 1 {
		t.Fatalf("peer submission failed pre-drain: %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	localJobID := ""
	for id := range s.shellJobs {
		localJobID = id
	}
	s.mu.Unlock()
	if localJobID == "" {
		t.Fatal("local job was not enqueued")
	}

	if st := s.NodeDrainStatus(); st.Draining || st.ReadyToStop || st.Counters.UnfinishedExecutions != 1 {
		t.Fatalf("unexpected pre-drain status: %+v", st)
	}

	s.EnableNodeDrain("planned update")
	if st := s.NodeDrainStatus(); !st.Draining || st.Reason != "planned update" || st.ReadyToStop {
		t.Fatalf("drain not active after begin: %+v", st)
	}

	// Local submissions are rejected with a retryable 503; nothing is enqueued.
	w := peerTestCall(s, `{"target":"shell:local","tool":"shell_exec","arguments":{"cmd":"printf during"},"background":true}`, "drain-owner", "")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"retryable":true`) || !strings.Contains(w.Body.String(), "node is draining; retry after drain completes") {
		t.Fatalf("local submission not drained: %d %s", w.Code, w.Body.String())
	}
	// Peer submissions are rejected before the outbound request exists.
	w = peerTestCall(s, `{"target":"shell:nodeB","tool":"shell_exec","arguments":{"cmd":"printf during"},"background":true}`, "drain-owner", "")
	if w.Code != http.StatusServiceUnavailable || peerCalls.Load() != 1 || !strings.Contains(w.Body.String(), `"retryable":true`) {
		t.Fatalf("peer submission not drained: %d %s", w.Code, w.Body.String())
	}
	// /mcp keeps the node peer failure envelope: -32000 with retryable data.
	// The direct execute facade is retired, so the surviving write-capable
	// public spelling is the code-mode container.
	w = peerTestMCPCall(s, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"exec","arguments":{"code":"return 1;"}}}`, "drain-owner")
	var envelope map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusServiceUnavailable || envelope["id"] != float64(7) {
		t.Fatalf("drained /mcp envelope wrong: %d %s", w.Code, w.Body.String())
	}
	if rpcErr := mapValue(envelope["error"]); rpcErr["code"] != float64(-32000) || !strings.Contains(w.Body.String(), `"retryable":true`) {
		t.Fatalf("drained /mcp error shape wrong: %s", w.Body.String())
	}
	// Receipt reads stay available while draining.
	w = peerTestMCPCall(s, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"get_mcp_job","arguments":{"id":"`+localJobID+`"}}}`, "drain-owner")
	if w.Code != 200 || strings.Contains(w.Body.String(), "retryable") {
		t.Fatalf("job receipt read blocked by drain: %d %s", w.Code, w.Body.String())
	}

	s.CancelNodeDrain()
	if st := s.NodeDrainStatus(); st.Draining || st.Reason != "" || st.Since != "" {
		t.Fatalf("cancel left drain state: %+v", st)
	}
	if w := peerTestCall(s, `{"target":"shell:local","tool":"shell_exec","arguments":{"cmd":"printf after"},"background":true}`, "drain-owner", ""); w.Code != 200 {
		t.Fatalf("submission failed after cancel: %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	count := len(s.shellJobs)
	s.mu.Unlock()
	if count != 2 {
		t.Fatalf("post-cancel submission missing: %d local jobs", count)
	}
}

func TestNodeDrainReadyToStopWaitsForInFlightSubmission(t *testing.T) {
	s := drainTestHub(t, nil)
	drainLocalExecutor(t, s)
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responses <- peerTestCall(s, `{"target":"shell:local","tool":"shell_exec","arguments":{"cmd":"sleep 30"},"background":false}`, "drain-owner", "")
	}()
	// The real submission path holds the gate: in-flight and unfinished work.
	waitForDrainCondition(t, s, "in-flight submission", func(st NodeDrainStatus) bool { return st.Counters.ActiveSubmissions >= 1 })
	waitForDrainCondition(t, s, "unfinished execution", func(st NodeDrainStatus) bool { return st.Counters.UnfinishedExecutions >= 1 })
	if st := s.NodeDrainStatus(); st.ReadyToStop {
		t.Fatalf("ready_to_stop true while a submission is in flight: %+v", st)
	}
	s.EnableNodeDrain("update while busy")
	if st := s.NodeDrainStatus(); st.ReadyToStop {
		t.Fatalf("ready_to_stop true while unfinished work remains: %+v", st)
	}
	// Drain closes new admissions but never strands the admitted submission.
	if w := peerTestCall(s, `{"target":"shell:local","tool":"shell_exec","arguments":{"cmd":"printf refused"},"background":true}`, "drain-owner", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("late submission admitted during drain: %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	jobID := ""
	for id := range s.shellJobs {
		jobID = id
	}
	s.mu.Unlock()
	completeLocalJob(t, s, jobID)
	if w := <-responses; w.Code != 200 {
		t.Fatalf("in-flight submission failed: %d %s", w.Code, w.Body.String())
	}
	st := waitForDrainCondition(t, s, "ready to stop", func(st NodeDrainStatus) bool { return st.ReadyToStop })
	if st.Counters.ActiveSubmissions != 0 || st.Counters.UnfinishedExecutions != 0 {
		t.Fatalf("counters did not settle: %+v", st)
	}
}

func TestNodeDrainCountersTrackRealLocalJob(t *testing.T) {
	s := drainTestHub(t, nil)
	drainLocalExecutor(t, s)
	s.SetNodeRuntimeCounters(runtimeCountersStub{callbacks: 2, outbox: 3})
	if w := peerTestCall(s, `{"target":"shell:local","tool":"shell_exec","arguments":{"cmd":"printf drain-counter"},"background":true}`, "drain-owner", ""); w.Code != 200 {
		t.Fatalf("submission failed: %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	jobID := ""
	for id := range s.shellJobs {
		jobID = id
	}
	s.mu.Unlock()
	st := waitForDrainCondition(t, s, "unfinished local job", func(st NodeDrainStatus) bool { return st.Counters.UnfinishedExecutions >= 1 })
	if st.Counters.ActiveCallbacks != 2 || st.Counters.OutboxPending != 3 {
		t.Fatalf("runtime counters missing from status: %+v", st)
	}
	s.EnableNodeDrain("counter test")
	if st := s.NodeDrainStatus(); st.ReadyToStop {
		t.Fatalf("ready_to_stop true with queued work: %+v", st)
	}
	completeLocalJob(t, s, jobID)
	st = waitForDrainCondition(t, s, "counters settled", func(st NodeDrainStatus) bool {
		return st.Counters.UnfinishedExecutions == 0 && st.Counters.ActiveSubmissions == 0
	})
	if st.ReadyToStop {
		t.Fatalf("ready_to_stop ignores runtime counters: %+v", st)
	}
	s.SetNodeRuntimeCounters(runtimeCountersStub{})
	if st := s.NodeDrainStatus(); !st.ReadyToStop {
		t.Fatalf("drain never became ready: %+v", st)
	}
}

func TestNodeDrainEndpointAuth(t *testing.T) {
	s := drainTestHub(t, nil)
	if w := drainAdminGet(s, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status allowed: %d %s", w.Code, w.Body.String())
	}
	if w := drainAdminPost(s, `{"action":"begin","reason":"auth test"}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated begin allowed: %d %s", w.Code, w.Body.String())
	}
	w := drainAdminPost(s, `{"action":"begin","reason":"auth test"}`, "drain-owner")
	if w.Code != 200 {
		t.Fatalf("begin failed: %d %s", w.Code, w.Body.String())
	}
	var status NodeDrainStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Draining || status.Reason != "auth test" || status.Since == "" {
		t.Fatalf("begin returned wrong status: %s", w.Body.String())
	}
	if w := drainAdminGet(s, "drain-owner"); w.Code != 200 || !strings.Contains(w.Body.String(), `"draining":true`) {
		t.Fatalf("authenticated status failed: %d %s", w.Code, w.Body.String())
	}
	// A second begin is idempotent: only the reason moves.
	if w := drainAdminPost(s, `{"action":"begin","reason":"second"}`, "drain-owner"); !strings.Contains(w.Body.String(), `"reason":"second"`) {
		t.Fatalf("second begin not merged: %s", w.Body.String())
	}
	if w := drainAdminPost(s, `{"action":"cancel"}`, "drain-owner"); w.Code != 200 || strings.Contains(w.Body.String(), `"draining":true`) {
		t.Fatalf("cancel failed: %d %s", w.Code, w.Body.String())
	}
	if w := drainAdminPost(s, `{}`, "drain-owner"); w.Code != http.StatusBadRequest {
		t.Fatalf("missing action accepted: %d %s", w.Code, w.Body.String())
	}
}
