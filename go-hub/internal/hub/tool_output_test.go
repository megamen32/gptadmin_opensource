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
			var refusal map[string]any
			for _, detail := range []string{"compact", "full", "compact"} {
				args := `{"target":"demo","tool":"write","query":"hello","background":true,"idempotency_key":"same-write","detail":"` + detail + `"}`
				var result map[string]any
				if transport == "http" {
					result = postHubJSON(t, s, "/mcp-relay/call", "ctl", args)
				} else {
					body := postMCPRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute","arguments":`+args+`}}`)
					result = mapValue(mapValue(body["result"])["structuredContent"])
				}
				if transport == "http" {
					// The relay route is unchanged: the presentation detail
					// never changes the execution identity.
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
					continue
				}
				// The public execute facade is retired. The call is refused
				// before anything runs, so there is no execution identity to
				// vary with the presentation detail.
				if firstString(result, "status") != "failed" {
					t.Fatalf("the retired execute facade was not refused: %#v", result)
				}
				failure := mapValue(result["error"])
				if firstString(failure, "code") != "execute_facade_retired" {
					t.Fatalf("the refusal does not explain the migration: %#v", result)
				}
				message := firstString(failure, "message")
				for _, hint := range []string{"search", "exec", "ro"} {
					if !strings.Contains(message, hint) {
						t.Fatalf("the refusal does not point at %q: %#v", hint, result)
					}
				}
				if refusal == nil {
					refusal = result
				} else if !reflect.DeepEqual(result, refusal) {
					t.Fatalf("the refusal changed with the presentation detail: %#v", result)
				}
			}
			s.mu.Lock()
			queued := len(s.relayQueues["demo"])
			jobs := len(s.relayJobs)
			var forwarded map[string]any
			if id != "" {
				forwarded = cloneMap(mapValue(s.relayJobs[id].Params["arguments"]))
			}
			s.mu.Unlock()
			if transport == "mcp" {
				// The refusal executed nothing and leaked no tool payload.
				if queued != 0 || jobs != 0 {
					t.Fatalf("the retired facade executed a tool: queued=%d jobs=%d", queued, jobs)
				}
				encoded, _ := json.Marshal(refusal)
				if strings.Contains(string(encoded), "hello") {
					t.Fatalf("the refusal leaked the tool arguments: %s", encoded)
				}
				if len(refusal) != 2 || len(mapValue(refusal["error"])) != 2 {
					t.Fatalf("the refusal leaked tool internals: %#v", refusal)
				}
				return
			}
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
	// Every published tool that offers the presentation detail publishes the
	// same compact/full contract.
	detailTools := map[string]bool{}
	for _, tool := range appsSDKTools() {
		name := firstString(tool, "name")
		properties := mapValue(mapValue(tool["inputSchema"])["properties"])
		detail, present := properties["detail"]
		if !present {
			continue
		}
		detailTools[name] = true
		if !reflect.DeepEqual(mapValue(detail)["enum"], []string{"compact", "full"}) {
			t.Fatalf("%s missing detail schema: %#v", name, detail)
		}
	}
	// The task readers and the schema reader still carry the detail contract.
	for _, name := range []string{"discover", "schema", "job"} {
		if !detailTools[name] {
			t.Fatalf("the published catalog lost the detail contract of %s", name)
		}
	}
	// The retired execute facade is not published any more.
	for _, tool := range appsSDKTools() {
		if firstString(tool, "name") == "execute" {
			t.Fatal("the Apps SDK catalog publishes the retired execute facade")
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
