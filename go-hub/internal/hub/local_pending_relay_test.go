package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestLocalPendingRelayRestartPoll(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{ConfigDir: dir, RelayAgentToken: "fixture"})
	params := map[string]any{"name": "read", "arguments": map[string]any{"token": "private-relay-input", "number": json.Number("9007199254740993")}, "_meta": map[string]any{"filesystem_acl": map[string]any{"read_roots": []any{"/allowed"}}}}
	request := httptest.NewRequest("POST", "/mcp-relay/call", nil)
	request = request.WithContext(context.WithValue(request.Context(), authClaimsContextKey{}, map[string]any{"client_id": "client", "jti": "token-id"}))
	id := s.enqueueRelayWithTraceParentForRequest(request, "standalone", "tools/call", params, "trace", "parent")
	j := s.relayJobs[id]
	if err := s.saveTaskStateLocked(id); err != nil {
		t.Fatal(err)
	}
	created := j.CreatedAt
	s.Close()
	n := New(Config{ConfigDir: dir, RelayAgentToken: "fixture"})
	defer n.Close()
	req := httptest.NewRequest("GET", "/mcp-relay/poll/standalone?timeout=1", nil)
	req.Header.Set("Authorization", "Bearer foreign")
	denied := httptest.NewRecorder()
	n.mcpRelayPoll(denied, req)
	if denied.Code != 401 || n.relayJobs[id].StartedAt != 0 {
		t.Fatal("unauthorized poll recovered input")
	}
	req.Header.Set("Authorization", "Bearer fixture")
	w := httptest.NewRecorder()
	n.mcpRelayPoll(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("queued input lost after restart: %d %s status=%s", w.Code, w.Body.String(), n.relayJobs[id].Status)
	}
	got := n.relayJobs[id]
	if got.CreatedAt != created || got.Caller.ClientID != "client" || got.TraceID != "trace" {
		t.Fatal("immutable metadata lost")
	}
	foreign := request.WithContext(context.WithValue(request.Context(), authClaimsContextKey{}, map[string]any{"client_id": "other", "jti": "foreign"}))
	if n.authorizeTaskReceipt(foreign, got.Caller, nil) == nil {
		t.Fatal("foreign caller can read restored receipt")
	}
	want, _ := json.Marshal(params)
	actual, _ := json.Marshal(got.Params)
	if string(want) != string(actual) {
		t.Fatalf("payload changed: %s != %s", actual, want)
	}
}

func TestLocalPendingRelayGuards(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{ConfigDir: dir, RelayAgentToken: "fixture"})
	id := s.enqueueRelay("standalone", "tools/call", map[string]any{"arguments": map[string]any{"token": "relay-secret-must-not-leak"}})
	state, err := s.readTaskRecords(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(state.Relay[id])
	if strings.Contains(string(raw), "relay-secret-must-not-leak") {
		t.Fatal("plaintext input persisted")
	}
	if _, err := s.openPendingValue(localPendingRelayNamespace, "different", state.Relay[id].SealedPending); err == nil {
		t.Fatal("cipher not bound to ID")
	}
	other := New(Config{ConfigDir: dir, RelayAgentToken: "fixture"})
	if err := other.recoverPendingRelayLocked(context.Background(), "standalone"); err != nil {
		t.Fatal(err)
	}
	if len(other.relayQueues["standalone"]) != 0 {
		t.Fatal("stole live owner")
	}
	other.Close()
	j := s.relayJobs[id]
	j.CreatedAt = nowFloat() - 10801
	started := s.enqueueRelay("standalone", "tools/call", map[string]any{"name": "never-replay"})
	s.relayJobs[started].Status = "running"
	s.relayJobs[started].StartedAt = nowFloat()
	s.shellJobs["approval"] = &shellJob{ID: "approval", OwnerID: j.OwnerID, Server: "host", Status: "input_required", CreatedAt: nowFloat(), Cmd: "unapproved-secret", ApprovalID: "approval-id"}
	if err := s.saveTaskStateLocked(id, started, "approval"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	n := New(Config{ConfigDir: dir, RelayAgentToken: "fixture"})
	defer n.Close()
	if err := n.recoverPendingRelayLocked(context.Background(), "standalone"); err != nil {
		t.Fatal(err)
	}
	if n.relayJobs[id].Status != "cancelled" || n.relayJobs[started].Status != "running" || len(n.relayQueues["standalone"]) != 0 {
		t.Fatal("expired/started replay")
	}
	if n.shellJobs["approval"].Status != "failed" || n.shellJobs["approval"].Cmd != "" {
		t.Fatal("unapproved input recovered")
	}
	state, err = n.readTaskRecords(id, started, "approval")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Relay[id].SealedPending) != 0 || len(state.Relay[started].SealedPending) != 0 || len(state.Shell["approval"].SealedPending) != 0 {
		t.Fatal("nonqueued executable payload retained")
	}
}

func TestLocalPendingRelayUnreadableFailsClosed(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{ConfigDir: dir, RelayAgentToken: "fixture"})
	id := s.enqueueRelay("standalone", "resources/read", map[string]any{"uri": "private://resource"})
	s.Close()
	n := New(Config{ConfigDir: dir, RelayAgentToken: "fixture"})
	defer n.Close()
	n.secretStoreErr = errors.New("test key unavailable")
	req := httptest.NewRequest("GET", "/mcp-relay/poll/standalone?timeout=1", nil)
	req.Header.Set("Authorization", "Bearer fixture")
	w := httptest.NewRecorder()
	n.mcpRelayPoll(w, req)
	if w.Code != 503 || n.relayJobs[id].StartedAt != 0 || n.relayJobs[id].Status != "queued" {
		t.Fatalf("unreadable dispatch %d %s", w.Code, w.Body.String())
	}
	n.secretStoreErr = nil
	w = httptest.NewRecorder()
	n.mcpRelayPoll(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("readable original not recovered %d %s", w.Code, w.Body.String())
	}
}

func TestLocalPendingRelayProcessRestartHTTP(t *testing.T) {
	if dir := os.Getenv("GPTADMIN_RELAY_RESTART_FIXTURE"); dir != "" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		origin := "http://" + listener.Addr().String()
		s := New(Config{ConfigDir: dir, AdminPassword: "fixture-admin", ShellToken: "fixture-shell", RelayAgentToken: "fixture-shell", PublicOrigin: origin, DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
		s.agents["standalone"] = &Agent{AgentID: "standalone", Name: "Fixture", Status: "online", Transport: "long_poll", LastSeen: nowFloat(), Meta: map[string]any{"approved": true}}
		fmt.Println(origin)
		if err := http.Serve(listener, s.Handler()); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	start := func() (*exec.Cmd, string, *http.Client) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLocalPendingRelayProcessRestartHTTP$")
		command.Env = append(os.Environ(), "GPTADMIN_RELAY_RESTART_FIXTURE="+dir)
		output, err := command.StdoutPipe()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err = command.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		stopped := false
		t.Cleanup(func() {
			if !stopped {
				_ = command.Process.Kill()
				_ = command.Wait()
			}
			cancel()
		})
		line, err := bufio.NewReader(output).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		origin := strings.TrimSpace(line)
		if !strings.HasPrefix(origin, "http://127.0.0.1:") {
			t.Fatalf("unexpected fixture startup: %q", origin)
		}
		jar, _ := cookiejar.New(nil)
		client := &http.Client{Jar: jar, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.PostForm(origin+"/admin/login", url.Values{"password": {"fixture-admin"}})
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 302 {
			t.Fatalf("fixture login: %d", response.StatusCode)
		}
		return command, origin, client
	}
	call := func(client *http.Client, origin, method, path string, body any, shell bool) map[string]any {
		t.Helper()
		var reader io.Reader
		if body != nil {
			payload, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(payload)
		}
		request, err := http.NewRequest(method, origin+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		if shell {
			request.Header.Set("Authorization", "Bearer fixture-shell")
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			t.Fatalf("%s %s: HTTP %d: %s", method, path, response.StatusCode, data)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("%s: %v: %s", path, err, data)
		}
		return result
	}
	primary, origin, client := start()
	body := map[string]any{"target": "standalone", "tool": "echo", "args": map[string]any{"token": "encrypted-process-canary", "integer": json.Number("9007199254740993")}, "background": true, "idempotency_key": "already-started"}
	first := call(client, origin, http.MethodPost, "/mcp-relay/call", body, false)
	startedID := firstString(first, "job_id", "task_id")
	dispatched := call(client, origin, http.MethodGet, "/mcp-relay/poll/standalone?timeout=1", nil, true)
	if startedID == "" || dispatched["id"] != startedID {
		t.Fatalf("first dispatch %v %v", first, dispatched)
	}
	body["idempotency_key"] = "still-queued"
	queued := call(client, origin, http.MethodPost, "/mcp-relay/call", body, false)
	id := firstString(queued, "job_id", "task_id")
	if id == "" {
		t.Fatal(queued)
	}
	if err := primary.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = primary.Wait()
	_, origin, client = start()
	replay := call(client, origin, http.MethodPost, "/mcp-relay/call", body, false)
	if firstString(replay, "job_id", "task_id") != id {
		t.Fatalf("idempotency lost %v", replay)
	}
	delivered := call(client, origin, http.MethodGet, "/mcp-relay/poll/standalone?timeout=1", nil, true)
	if delivered["id"] != id || firstString(mapValue(mapValue(delivered["params"])["arguments"]), "token") != "encrypted-process-canary" {
		t.Fatalf("restart payload lost %v", delivered)
	}
	empty := call(client, origin, http.MethodGet, "/mcp-relay/poll/standalone?timeout=1", nil, true)
	if empty["id"] != nil {
		t.Fatalf("started replayed %v", empty)
	}
	state := call(client, origin, http.MethodGet, "/mcp-relay/job/"+startedID, nil, false)
	if state["status"] != "running" {
		t.Fatalf("started receipt lost %v", state)
	}
	t.Log("Real HTTP/process proof: accepted queued request survives SIGKILL; same idempotency key and ID; exact encrypted arguments restored; started request never redispatched")
}
