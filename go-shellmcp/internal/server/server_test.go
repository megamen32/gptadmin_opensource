package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	foundFileBackup := false
	for _, raw := range result["tools"].([]any) {
		tool := raw.(map[string]any)
		if tool["name"] == "shell_exec" {
			foundShellExec = true
		}
		if tool["name"] == "file_backup" {
			foundFileBackup = true
		}
	}
	if !foundShellExec || !foundFileBackup {
		t.Fatalf("tools/list missing expected tools shell_exec=%v file_backup=%v: %s", foundShellExec, foundFileBackup, toolsRec.Body.String())
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

func TestMCPFileBackupBackupListRestoreCleanup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHELLMCP_FILE_BACKUP_ROOT", "")
	t.Setenv("GPTADMIN_FILE_BACKUP_ROOT", "")
	s := New(Config{Token: "t", Name: "unit-host", DefaultHome: home, LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})
	target := filepath.Join(t.TempDir(), "config.txt")
	if err := os.WriteFile(target, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}

	backupBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"file_backup","arguments":{"action":"backup","path":%q,"ttl_days":7,"label":"unit"}}}`, target)
	backupReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(backupBody))
	backupReq.Header.Set("Authorization", "Bearer t")
	backupRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(backupRec, backupReq)
	if backupRec.Code != 200 {
		t.Fatalf("file_backup backup status=%d body=%s", backupRec.Code, backupRec.Body.String())
	}
	var backupResp map[string]any
	if err := json.Unmarshal(backupRec.Body.Bytes(), &backupResp); err != nil {
		t.Fatal(err)
	}
	backupResult := backupResp["result"].(map[string]any)
	backupStructured := backupResult["structuredContent"].(map[string]any)
	backupID := backupStructured["backup_id"].(string)
	artifact := backupStructured["artifact"].(string)
	if artifact == "" || backupID == "" {
		t.Fatalf("missing artifact/backup_id: %s", backupRec.Body.String())
	}
	data, err := os.ReadFile(artifact)
	if err != nil || string(data) != "before" {
		t.Fatalf("bad artifact data=%q err=%v", data, err)
	}

	if err := os.WriteFile(target, []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"file_backup","arguments":{"action":"restore","backup_id":%q,"overwrite":true}}}`, backupID)
	restoreReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(restoreBody))
	restoreReq.Header.Set("Authorization", "Bearer t")
	restoreRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(restoreRec, restoreReq)
	if restoreRec.Code != 200 {
		t.Fatalf("file_backup restore status=%d body=%s", restoreRec.Code, restoreRec.Body.String())
	}
	restored, err := os.ReadFile(target)
	if err != nil || string(restored) != "before" {
		t.Fatalf("restore failed data=%q err=%v", restored, err)
	}

	listReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"file_backup","arguments":{"action":"list","limit":5}}}`))
	listReq.Header.Set("Authorization", "Bearer t")
	listRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != 200 || !strings.Contains(listRec.Body.String(), backupID) {
		t.Fatalf("file_backup list failed status=%d body=%s", listRec.Code, listRec.Body.String())
	}
}
