package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func compactOutputFixture(t *testing.T) (*Server, *shellJob) {
	t.Helper()
	s := New(Config{CtlToken: "ctl", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	j := &shellJob{ID: "compact-fixture", Server: "test-host", ToolName: "shell_exec", Status: "completed", TraceID: "trace-id", TraceParent: "trace-parent", Result: map[string]any{
		"stdout": "Persist restart transitions: 3\n", "stderr": "", "returncode": 0,
		"cwd_effective": "/home/test/project", "duration_ms": 60, "stdout_bytes": 31,
	}}
	s.shellJobs[j.ID] = j
	return s, j
}

func assertCompactShellOutput(t *testing.T, result map[string]any) {
	t.Helper()
	if result["stdout"] != "Persist restart transitions: 3\n" || result["status"] != "completed" || result["job_id"] != "compact-fixture" {
		t.Fatalf("useful output or recovery handle missing: %#v", result)
	}
	for _, key := range []string{"response", "server_id", "task_id", "trace_id", "traceparent", "cwd_effective", "duration_ms", "stdout_bytes", "stderr", "returncode"} {
		if _, ok := result[key]; ok {
			t.Errorf("redundant %s in compact response: %#v", key, result)
		}
	}
	if len(result) != 3 {
		t.Errorf("want only job_id/status/stdout, got %#v", result)
	}
}

func TestCompactToolOutputJobDefault(t *testing.T) {
	s, j := compactOutputFixture(t)
	before, _ := json.Marshal(shellJobResponse(j))
	assertCompactShellOutput(t, mapValue(s.appsSDKCall("job", map[string]any{"id": j.ID})))
	after, _ := json.Marshal(shellJobResponse(j))
	if string(before) != string(after) {
		t.Fatal("presentation mutated stored diagnostics")
	}
	full := mapValue(s.appsSDKCall("job", map[string]any{"id": j.ID, "detail": "full"}))
	if !reflect.DeepEqual(full, shellJobResponse(j)) {
		t.Fatalf("full diagnostics changed: %#v", full)
	}
}

func TestCompactToolOutputHTTPJob(t *testing.T) {
	s, j := compactOutputFixture(t)
	for _, detail := range []string{"", "compact", "full"} {
		t.Run(detail, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/mcp-relay/job/"+j.ID+"?detail="+detail, nil)
			r.Header.Set("Authorization", "Bearer ctl")
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var result map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if detail == "full" {
				if result["trace_id"] != j.TraceID || result["response"] == nil {
					t.Fatalf("missing diagnostics: %#v", result)
				}
			} else {
				assertCompactShellOutput(t, result)
			}
		})
	}
}

func TestCompactToolOutputMCPJob(t *testing.T) {
	s, j := compactOutputFixture(t)
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"job","arguments":{"id":"`+j.ID+`"}}}`))
	r.Header.Set("Authorization", "Bearer ctl")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || body["error"] != nil {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	assertCompactShellOutput(t, mapValue(mapValue(body["result"])["structuredContent"]))
}

func TestCompactToolOutputErrorsResourcesAndBusinessData(t *testing.T) {
	t.Run("failed command and redaction", func(t *testing.T) {
		s, j := compactOutputFixture(t)
		j.Status = "failed"
		j.SecretValues = []string{"fixture-secret"}
		j.Result = map[string]any{"stdout": "partial\n", "stderr": "denied fixture-secret\n", "returncode": 7, "duration_ms": 100}
		j.Error = "command failed fixture-secret"
		result := mapValue(s.appsSDKCall("job", map[string]any{"id": j.ID}))
		if result["returncode"] != 7 || result["stdout"] != "partial\n" || result["stderr"] == nil || result["error"] == nil || result["status"] != "failed" {
			t.Fatalf("lost failure evidence: %#v", result)
		}
		for _, detail := range []string{"compact", "full"} {
			raw, _ := json.Marshal(s.appsSDKCall("job", map[string]any{"id": j.ID, "detail": detail}))
			if strings.Contains(string(raw), "fixture-secret") {
				t.Fatalf("secret leaked in %s", detail)
			}
		}
	})
	t.Run("spilled output remains retrievable", func(t *testing.T) {
		s, j := compactOutputFixture(t)
		j.Result = map[string]any{"stdout": "preview", "stderr": "", "returncode": 0, "_spilled": true, "stdout_path": "/output/stdout", "stdout_bytes": 90000, "stdout_truncated": true}
		result := mapValue(s.appsSDKCall("job", map[string]any{"id": j.ID}))
		if result["stdout"] != "preview" || result["stdout_bytes"] != 90000 || result["stdout_truncated"] != true || result["resources"] == nil || result["_spilled"] != true {
			t.Fatalf("lost output continuation: %#v", result)
		}
	})
	t.Run("non-shell payload is opaque", func(t *testing.T) {
		s, j := compactOutputFixture(t)
		j.ToolName = "mcp_call"
		business := map[string]any{"stdout": "", "duration_ms": 0, "trace_id": "business-id", "returncode": 0, "content": []any{map[string]any{"type": "image", "data": "payload"}}}
		j.Result = business
		result := mapValue(s.appsSDKCall("job", map[string]any{"id": j.ID}))
		if !reflect.DeepEqual(result["result"], business) {
			t.Fatalf("rewrote child payload: %#v", result)
		}
	})
	t.Run("relay content and distinct child IDs survive", func(t *testing.T) {
		payload := map[string]any{"content": []any{map[string]any{"type": "text", "text": "warning"}}, "structuredContent": map[string]any{"trace_id": "business-id"}, "isError": true}
		raw := map[string]any{"job_id": "parent", "task_id": "child", "server_id": "mcp:demo", "status": "completed", "response": payload, "message": "important warning"}
		result := compactToolOutput(raw, "example")
		if !reflect.DeepEqual(result["response"], payload) || result["task_id"] != "child" || result["message"] != "important warning" {
			t.Fatalf("lost upstream fields: %#v", result)
		}
	})
	t.Run("shell payload cannot overwrite task identity", func(t *testing.T) {
		s, j := compactOutputFixture(t)
		j.Result = map[string]any{"job_id": "business-id", "status": "business-status", "returncode": 0}
		result := mapValue(s.appsSDKCall("job", map[string]any{"id": j.ID}))
		if result["job_id"] != j.ID || result["status"] != j.Status || mapValue(result["result"])["job_id"] != "business-id" {
			t.Fatalf("identity collision: %#v", result)
		}
	})
}

func TestCompactToolOutputExecuteDetailDoesNotReexecuteOrLeakToTool(t *testing.T) {
	for _, transport := range []string{"http", "mcp"} {
		t.Run(transport, func(t *testing.T) {
			s := New(Config{CtlToken: "ctl", RelayAgentToken: "relay", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
			registerRelayAgent(t, s, "demo")
			var id string
			for _, detail := range []string{"compact", "full", "compact"} {
				args := `{"target":"demo","tool":"write","query":"hello","background":true,"idempotency_key":"same-write","detail":"` + detail + `"}`
				var result map[string]any
				if transport == "http" {
					result = postHubJSON(t, s, "/mcp-relay/call", "ctl", args)
				} else {
					body := postMCPRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute","arguments":`+args+`}}`)
					result = mapValue(mapValue(body["result"])["structuredContent"])
				}
				if id == "" {
					id = firstString(result, "job_id")
				}
				if id == "" || result["job_id"] != id {
					t.Fatalf("detail changed execution identity: %#v", result)
				}
				if detail == "full" && result["server_id"] != "demo" {
					t.Fatalf("missing full envelope: %#v", result)
				}
				if detail == "compact" && (result["server_id"] != nil || result["background"] != nil) {
					t.Fatalf("verbose pending response: %#v", result)
				}
			}
			s.mu.Lock()
			queued := len(s.relayQueues["demo"])
			forwarded := cloneMap(mapValue(s.relayJobs[id].Params["arguments"]))
			s.mu.Unlock()
			if queued != 1 || !reflect.DeepEqual(forwarded, map[string]any{"query": "hello"}) {
				t.Fatalf("reexecuted or forwarded presentation args: queued=%d args=%#v", queued, forwarded)
			}
		})
	}
}

func TestCompactToolOutputSettingPersistsAndPerCallWins(t *testing.T) {
	cfg := Config{CtlToken: "ctl", RegistryStateFile: t.TempDir() + "/registry.json"}
	s := New(cfg)
	_, status := s.callHubTool("settings_set", map[string]any{"settings": map[string]any{"tool_output_verbose": true}})
	if status != http.StatusOK {
		t.Fatalf("setting rejected: %d", status)
	}
	reloaded := New(cfg)
	_, j := compactOutputFixture(t)
	reloaded.shellJobs[j.ID] = j
	full := mapValue(reloaded.appsSDKCall("job", map[string]any{"id": j.ID}))
	if full["trace_id"] != j.TraceID || full["response"] == nil {
		t.Fatalf("setting not persisted: %#v", full)
	}
	assertCompactShellOutput(t, mapValue(reloaded.appsSDKCall("job", map[string]any{"id": j.ID, "detail": "compact"})))
	_, status = reloaded.callHubTool("settings_set", map[string]any{"settings": map[string]any{"tool_output_verbose": false}})
	if status != http.StatusOK {
		t.Fatalf("reset rejected: %d", status)
	}
	assertCompactShellOutput(t, mapValue(reloaded.appsSDKCall("job", map[string]any{"id": j.ID})))
}

func TestCompactToolOutputInvalidDetailRejectedBeforeExecution(t *testing.T) {
	s := New(Config{CtlToken: "ctl", RelayAgentToken: "relay", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	registerRelayAgent(t, s, "demo")
	for _, detail := range []string{`"verbose"`, `true`, `{}`} {
		r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", strings.NewReader(`{"target":"demo","tool":"write","background":true,"detail":`+detail+`}`))
		r.Header.Set("Authorization", "Bearer ctl")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid detail accepted: %s", w.Body.String())
		}
	}
	if len(s.relayJobs) != 0 {
		t.Fatal("invalid presentation request executed a tool")
	}
}

func TestCompactToolOutputContractsAndContextBudget(t *testing.T) {
	for _, name := range []string{"execute", "job"} {
		found := false
		for _, tool := range appsSDKTools() {
			if tool["name"] == name {
				found = true
				detail := mapValue(mapValue(mapValue(tool["inputSchema"])["properties"])["detail"])
				if !reflect.DeepEqual(detail["enum"], []string{"compact", "full"}) {
					t.Fatalf("%s missing detail schema: %#v", name, detail)
				}
			}
		}
		if !found {
			t.Fatalf("missing tool %s", name)
		}
	}
	s, j := compactOutputFixture(t)
	full, _ := json.Marshal(s.appsSDKCall("job", map[string]any{"id": j.ID, "detail": "full"}))
	compact, _ := json.Marshal(s.appsSDKCall("job", map[string]any{"id": j.ID}))
	if len(compact)*2 >= len(full) {
		t.Fatalf("insufficient envelope reduction: compact=%d full=%d", len(compact), len(full))
	}
	t.Logf("fixture JSON bytes: compact=%d full=%d", len(compact), len(full))
}
