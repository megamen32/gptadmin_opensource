package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/output"
)

func TestFromEnvDefaultLogLimit(t *testing.T) {
	t.Setenv("LOG_LIMIT_B", "")
	cfg := FromEnv()
	if cfg.LogLimit != output.DefaultInlineTailBytes {
		t.Fatalf("LogLimit=%d want %d", cfg.LogLimit, output.DefaultInlineTailBytes)
	}
}

func TestExecEndpoint(t *testing.T) {
	s := New(Config{Token: "t", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewBufferString(`{"cmd":"printf ok"}`))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["stdout"] != "ok" {
		t.Fatalf("bad body: %#v", got)
	}
}

func TestExecLiveEndpoint(t *testing.T) {
	s := New(Config{Token: "t", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodPost, "/exec/live", bytes.NewBufferString(`{"cmd":"echo ok"}`))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"type":"chunk"`) || !strings.Contains(body, `"type":"exit"`) {
		t.Fatalf("bad live response code=%d body=%s", rec.Code, body)
	}
}

func TestBackgroundJob(t *testing.T) {
	s := New(Config{Token: "t", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewBufferString(`{"cmd":"printf bg","background":true}`))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 202 {
		t.Fatalf("want 202 got %d %s", rec.Code, rec.Body.String())
	}
	var start map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &start)
	id := start["job_id"].(string)
	for i := 0; i < 30; i++ {
		get := httptest.NewRequest(http.MethodGet, "/jobs/"+id, nil)
		get.Header.Set("Authorization", "Bearer t")
		gr := httptest.NewRecorder()
		s.Handler().ServeHTTP(gr, get)
		if strings.Contains(gr.Body.String(), `"state":"done"`) && strings.Contains(gr.Body.String(), `"stdout":"bg"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job did not finish")
}

func TestUnauthorized(t *testing.T) {
	s := New(Config{Token: "t"})
	req := httptest.NewRequest(http.MethodGet, "/system/health", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("want 401 got %d", rec.Code)
	}
}

func TestFileEndpoint(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{Token: "t", LogLimit: 4, ExecTimeout: 5, SpillDir: dir})
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewBufferString(`{"cmd":"printf 123456789"}`))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	p, _ := got["stdout_path"].(string)
	if p == "" {
		t.Fatalf("missing stdout_path: %s", rec.Body.String())
	}
	r2 := httptest.NewRequest(http.MethodGet, "/file?path="+p, nil)
	r2.Header.Set("Authorization", "Bearer t")
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, r2)
	if rec2.Code != 200 || rec2.Body.String() != "123456789" {
		t.Fatalf("file code=%d body=%q", rec2.Code, rec2.Body.String())
	}
}

func TestMCPHTTPEndpointToolsAndShellExec(t *testing.T) {
	s := New(Config{Token: "t", Name: "unit-host", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})

	toolsReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	toolsReq.Header.Set("Authorization", "Bearer t")
	toolsRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(toolsRec, toolsReq)
	if toolsRec.Code != 200 {
		t.Fatalf("tools/list status=%d body=%s", toolsRec.Code, toolsRec.Body.String())
	}
	var toolsBody map[string]any
	if err := json.Unmarshal(toolsRec.Body.Bytes(), &toolsBody); err != nil {
		t.Fatal(err)
	}
	result := toolsBody["result"].(map[string]any)
	foundShellExec := false
	for _, raw := range result["tools"].([]any) {
		tool := raw.(map[string]any)
		if tool["name"] == "shell_exec" {
			foundShellExec = true
		}
	}
	if !foundShellExec {
		t.Fatalf("tools/list missing shell_exec: %s", toolsRec.Body.String())
	}

	callReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"shell_exec","arguments":{"cmd":"printf real_mcp_ok","timeout":5}}}`))
	callReq.Header.Set("Authorization", "Bearer t")
	callRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(callRec, callReq)
	if callRec.Code != 200 {
		t.Fatalf("tools/call status=%d body=%s", callRec.Code, callRec.Body.String())
	}
	var callBody map[string]any
	if err := json.Unmarshal(callRec.Body.Bytes(), &callBody); err != nil {
		t.Fatal(err)
	}
	callResult := callBody["result"].(map[string]any)
	structured := callResult["structuredContent"].(map[string]any)
	payload := structured["result"].(map[string]any)
	if payload["stdout"] != "real_mcp_ok" {
		t.Fatalf("bad mcp shell_exec payload: %s", callRec.Body.String())
	}
}

func TestMCPHTTPPollingTransportDescriptor(t *testing.T) {
	s := New(Config{Token: "t", Name: "unit-host", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir(), QueueEnabled: true, Mode: "long_poll"})
	req := httptest.NewRequest(http.MethodGet, "/mcp?sse=1", nil)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Mcp-Session-Id", "session-test")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "streamable-http-poll") || !strings.Contains(rec.Body.String(), "session-test") {
		t.Fatalf("bad poll descriptor code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("MCP-Protocol-Version") == "" || rec.Header().Get("Mcp-Session-Id") != "session-test" {
		t.Fatalf("missing MCP transport headers: %#v", rec.Header())
	}
}

func TestMCPStdioNDJSON(t *testing.T) {
	s := New(Config{Token: "", Name: "unit-host", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n")
	var out bytes.Buffer
	if err := s.ServeMCPStdio(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 stdio responses, got %d: %q", len(lines), out.String())
	}
	if !strings.Contains(lines[0], "protocolVersion") || !strings.Contains(lines[1], "shell_exec") {
		t.Fatalf("bad stdio output: %q", out.String())
	}
}
