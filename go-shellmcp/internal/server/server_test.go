package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/mcpclient"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/output"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

func TestFromEnvDefaultLogLimit(t *testing.T) {
	t.Setenv("LOG_LIMIT_B", "")
	cfg := FromEnv()
	if cfg.LogLimit != output.DefaultInlineTailBytes {
		t.Fatalf("LogLimit=%d want %d", cfg.LogLimit, output.DefaultInlineTailBytes)
	}
}

func TestFileEndpointRejectsSymlinkEvenWhenLinkIsInsideSpillRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("not-for-shell-client"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	s := New(Config{Token: "t", SpillDir: root})
	request := httptest.NewRequest(http.MethodGet, "/file?path="+url.QueryEscape(link), nil)
	request.Header.Set("Authorization", "Bearer t")
	record := httptest.NewRecorder()
	s.Handler().ServeHTTP(record, request)
	if record.Code == http.StatusOK || strings.Contains(record.Body.String(), "not-for-shell-client") {
		t.Fatalf("file endpoint followed symlink: status=%d body=%q", record.Code, record.Body.String())
	}
}

func TestFromEnvUsesInstallerSpillDirectoryAliases(t *testing.T) {
	spill := t.TempDir()
	for _, key := range []string{"SHELL_SPOOL_DIR", "SHELLMCP_SPOOL_DIR", "SHELL_SPILL_DIR", "SHELLMCP_SPILL_DIR"} {
		t.Setenv(key, "")
	}
	t.Setenv("SHELLMCP_SPILL_DIR", spill)

	cfg := FromEnv()
	if cfg.SpillDir != spill {
		t.Fatalf("installer spill directory ignored: got %q want %q", cfg.SpillDir, spill)
	}
}

func TestFromEnvUsesWindowsInstallerPollingContract(t *testing.T) {
	t.Setenv("SHELLMCP_QUEUE", "1")
	t.Setenv("SHELLMCP_HOST", "127.0.0.1")
	t.Setenv("SHELLMCP_PORT", "25900")
	cfg := FromEnv()
	if !cfg.QueueEnabled || cfg.Mode != "long_poll" {
		t.Fatalf("installer polling config not applied: %+v", cfg)
	}
	if cfg.Addr != "127.0.0.1:25900" {
		t.Fatalf("installer bind config ignored: Addr=%q", cfg.Addr)
	}
}

func TestFromEnvDefaultsToLongPollingWithoutInboundListener(t *testing.T) {
	t.Setenv("SHELL_QUEUE", "")
	t.Setenv("SHELLMCP_QUEUE", "")
	t.Setenv("SHELL_MODE", "")
	t.Setenv("SHELLMCP_MODE", "")

	cfg := FromEnv()
	if !cfg.QueueEnabled || cfg.Mode != "long_poll" {
		t.Fatalf("default transport must be long_poll without a listener: %+v", cfg)
	}
}

func TestQueueTransportNeverNeedsLocalListener(t *testing.T) {
	for _, heartbeat := range []bool{false, true} {
		s := New(Config{QueueEnabled: true, HeartbeatEnabled: heartbeat})
		if s.needsLocalListener() {
			t.Fatalf("queue transport with heartbeat=%v must not bind a local listener", heartbeat)
		}
	}
	if !New(Config{QueueEnabled: false}).needsLocalListener() {
		t.Fatal("non-queue transport must retain its local listener")
	}
}

func TestServerCloseTerminatesSupervisorChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the POSIX shell available on deployed Linux and macOS hosts")
	}
	s := New(Config{MCPConfig: filepath.Join(t.TempDir(), "mcp.json"), SpillDir: t.TempDir()})
	defer s.supervisor.KillAll()
	if err := s.supervisor.Upsert(supervisor.Agent{Ref: "slow", Command: "/bin/sh", Args: []string{"-c", "sleep 30"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.supervisor.Start("slow"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := s.supervisor.Status("slow")
		if err == nil && !status.Running {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("supervisor child remained alive after Server.Close")
}

func TestQueueShellExecPreservesExplicitRunAsUser(t *testing.T) {
	req := shellRequestFromQueueJob(hub.QueueJob{
		Cmd:       "id -un",
		Cwd:       "/tmp",
		Timeout:   5,
		Arguments: map[string]any{"run_as_user": "root"},
	}, "/tmp/spool")
	if req.RunAsUser != "root" {
		t.Fatalf("queued run_as_user was lost: %+v", req)
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

func TestFileEndpointAllowsCanonicalizedSpillRootPrefix(t *testing.T) {
	physical := t.TempDir()
	alias := filepath.Join(t.TempDir(), "spill-root")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	path := filepath.Join(alias, "stdout.txt")
	if err := os.WriteFile(filepath.Join(physical, "stdout.txt"), []byte("canonical-root"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(Config{Token: "t", SpillDir: alias})
	req := httptest.NewRequest(http.MethodGet, "/file?path="+path, nil)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "canonical-root" {
		t.Fatalf("file code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestFileEndpointRejectsIntermediateSymlinkBelowCanonicalizedSpillRoot(t *testing.T) {
	physical := t.TempDir()
	alias := filepath.Join(t.TempDir(), "spill-root")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	targetDir := filepath.Join(physical, "target")
	if err := os.Mkdir(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "secret.txt"), []byte("not-for-shell-client"), 0o600); err != nil {
		t.Fatal(err)
	}
	intermediateLink := filepath.Join(physical, "nested")
	if err := os.Symlink(targetDir, intermediateLink); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	s := New(Config{Token: "t", SpillDir: alias})
	path := filepath.Join(alias, "nested", "secret.txt")
	req := httptest.NewRequest(http.MethodGet, "/file?path="+url.QueryEscape(path), nil)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "not-for-shell-client") {
		t.Fatalf("file endpoint followed intermediate symlink: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestRejectSymlinksBelowRootFailsClosedOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rejectSymlinksBelowRoot(root, outside); err == nil {
		t.Fatal("expected path outside root to be rejected")
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
	names := map[string]bool{}
	for _, raw := range result["tools"].([]any) {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		names[name] = true
	}
	if !names["shell_exec"] || !names["system_inspect"] || !names["file_backup"] || !names["tasks"] || !names["system_info"] || !names["mcp_manage"] {
		t.Fatalf("tools/list missing expected tools: names=%v body=%s", names, toolsRec.Body.String())
	}
	for _, raw := range result["tools"].([]any) {
		tool := raw.(map[string]any)
		if tool["name"] == "mcp_manage" {
			actions := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]any)
			if len(actions) != 8 || actions[0] != "list" || actions[1] != "upsert" || actions[2] != "remove" || actions[3] != "enable" || actions[4] != "disable" {
				t.Fatalf("unexpected mcp_manage actions: %#v", actions)
			}
		}
	}
	for _, hidden := range []string{"capability_registry", "mcp_http", "mcp_stdio", "mcp_transport_http", "mcp_transport_stdio"} {
		if names[hidden] {
			t.Fatalf("tools/list exposed hidden/internal tool %q: %s", hidden, toolsRec.Body.String())
		}
	}

	systemReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"system_info","arguments":{}}}`))
	systemReq.Header.Set("Authorization", "Bearer t")
	systemRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(systemRec, systemReq)
	if systemRec.Code != 200 {
		t.Fatalf("system_info status=%d body=%s", systemRec.Code, systemRec.Body.String())
	}
	var systemBody map[string]any
	if err := json.Unmarshal(systemRec.Body.Bytes(), &systemBody); err != nil {
		t.Fatal(err)
	}
	systemResult := systemBody["result"].(map[string]any)
	systemStructured := systemResult["structuredContent"].(map[string]any)
	if systemStructured["system"] == nil || systemStructured["capability_registry"] == nil {
		t.Fatalf("system_info did not include merged system/capability data: %s", systemRec.Body.String())
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

func TestMCPSystemInspectRedactsFileSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.env")
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhZG1pbiJ9.c2lnbmF0dXJl"
	if err := os.WriteFile(path, []byte("STATUS=healthy\nTOKEN="+jwt+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Token: "t", Name: "unit-host", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir(), InspectRoots: []string{dir}})
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"system_inspect","arguments":{"action":"read_file","path":%q}}}`, path)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "STATUS=healthy") || strings.Contains(rec.Body.String(), jwt) {
		t.Fatalf("system_inspect did not redact output: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMCPManagePersistsRemoteServerLifecycle(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "mcp-agents.json")
	s := New(Config{MCPConfig: configPath, SpillDir: t.TempDir()})
	_, err := s.mcpManage(map[string]any{
		"action": "upsert",
		"config": map[string]any{
			"ref":       "docs",
			"transport": "streamable-http",
			"url":       "https://example.test/mcp",
			"enabled":   true,
		},
	})
	if err != nil {
		t.Fatalf("upsert remote MCP: %v", err)
	}
	if _, err := s.mcpManage(map[string]any{"action": "disable", "ref": "docs"}); err != nil {
		t.Fatalf("disable remote MCP: %v", err)
	}
	agents, err := supervisor.LoadAgents(configPath)
	if err != nil {
		t.Fatalf("reload MCP config: %v", err)
	}
	if len(agents) != 1 || agents[0].Enabled || agents[0].URL != "https://example.test/mcp" {
		t.Fatalf("unexpected persisted remote MCP: %#v", agents)
	}
}

func TestMCPManageOwnsOneOnDemandStdioSession(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "starts")
	script := filepath.Join(dir, "child.sh")
	body := `#!/bin/sh
echo x >> "$COUNT_FILE"
instance=$(wc -l < "$COUNT_FILE" | tr -d ' ')
while IFS= read -r line; do
 case "$line" in
  *'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}' ;;
  *'"method":"notifications/initialized"'*) ;;
  *'"method":"tools/list"'*) printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"tools\":[{\"name\":\"echo-$instance\",\"inputSchema\":{\"type\":\"object\"}}]}}" ;;
 esac
done
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}

	s := New(Config{MCPConfig: filepath.Join(dir, "mcp.json"), SpillDir: t.TempDir()})
	defer s.Close()
	if _, err := s.mcpManage(map[string]any{"action": "upsert", "config": map[string]any{
		"ref": "local", "transport": "stdio", "command": script,
		"env": map[string]any{"COUNT_FILE": counter}, "enabled": false,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.mcpManage(map[string]any{"action": "enable", "ref": "local"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if data, err := os.ReadFile(counter); err == nil {
		t.Fatalf("enable eagerly started an unconnected process: %q", data)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}

	tools, err := s.callMCPTool(context.Background(), "mcp_tools", map[string]any{"ref": "local"})
	if err != nil || !strings.Contains(fmt.Sprint(tools["structuredContent"]), "echo-1") {
		t.Fatalf("first tools=%#v err=%v", tools, err)
	}
	status, err := s.mcpManage(map[string]any{"action": "status", "ref": "local"})
	if err != nil || !mcpRunning(t, status) {
		t.Fatalf("active status=%#v err=%v", status, err)
	}

	if _, err := s.mcpManage(map[string]any{"action": "restart", "ref": "local"}); err != nil {
		t.Fatal(err)
	}
	tools, err = s.callMCPTool(context.Background(), "mcp_tools", map[string]any{"ref": "local"})
	if err != nil || !strings.Contains(fmt.Sprint(tools["structuredContent"]), "echo-2") {
		t.Fatalf("restarted tools=%#v err=%v", tools, err)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if starts := strings.Count(string(data), "x"); starts != 2 {
		t.Fatalf("stdio process starts=%d want exactly two sessions; data=%q", starts, data)
	}

	if _, err := s.mcpManage(map[string]any{"action": "disable", "ref": "local"}); err != nil {
		t.Fatal(err)
	}
	status, err = s.mcpManage(map[string]any{"action": "status", "ref": "local"})
	if err != nil || mcpRunning(t, status) {
		t.Fatalf("disabled status=%#v err=%v", status, err)
	}
}

func mcpRunning(t *testing.T, response map[string]any) bool {
	t.Helper()
	structured, ok := response["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("structuredContent type=%T", response["structuredContent"])
	}
	status, ok := structured["status"].(mcpclient.RuntimeStatus)
	if !ok {
		t.Fatalf("runtime status type=%T", structured["status"])
	}
	return status.Running
}

func TestVersionUsesTransportFeatureNames(t *testing.T) {
	s := New(Config{Token: "t", Name: "unit-host", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("version status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `"status":"prototype"`) {
		t.Fatalf("completed Go runtime still advertises itself as prototype: %s", body)
	}
	if strings.Contains(body, `"mcp_http"`) || strings.Contains(body, `"mcp_stdio"`) {
		t.Fatalf("/version exposed ambiguous MCP feature names: %s", body)
	}
	if !strings.Contains(body, `"mcp_transport_http"`) || !strings.Contains(body, `"mcp_transport_stdio"`) {
		t.Fatalf("/version missing transport feature names: %s", body)
	}
}

func TestMCPHTTPGetDoesNotAdvertiseProprietaryPolling(t *testing.T) {
	s := New(Config{Token: "t", Name: "unit-host", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir(), QueueEnabled: true, Mode: "long_poll"})
	req := httptest.NewRequest(http.MethodGet, "/mcp?sse=1", nil)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || strings.Contains(rec.Body.String(), "streamable-http-poll") {
		t.Fatalf("GET code=%d body=%s want standards-safe 405", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("MCP-Protocol-Version") == "" || rec.Header().Get("Mcp-Session-Id") != "" {
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

func TestChildMCPToolsAndCallThroughPublicTools(t *testing.T) {
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": []any{map[string]any{"name": "ping", "inputSchema": map[string]any{"type": "object"}}}}})
		case "tools/call":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "pong"}}}})
		default:
			t.Fatalf("unexpected child method %q", method)
		}
	}))
	defer child.Close()

	s := New(Config{MCPConfig: filepath.Join(t.TempDir(), "mcp.json"), SpillDir: t.TempDir()})
	if _, err := s.mcpManage(map[string]any{"action": "upsert", "config": map[string]any{"ref": "child", "transport": "streamable-http", "url": child.URL, "enabled": true}}); err != nil {
		t.Fatal(err)
	}
	tools, err := s.callMCPTool(context.Background(), "mcp_tools", map[string]any{"ref": "child"})
	if err != nil || !strings.Contains(fmt.Sprint(tools["structuredContent"]), "ping") {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
	called, err := s.callMCPTool(context.Background(), "mcp_call", map[string]any{"ref": "child", "name": "ping", "arguments": map[string]any{}})
	if err != nil || !strings.Contains(fmt.Sprint(called["structuredContent"]), "pong") {
		t.Fatalf("called=%#v err=%v", called, err)
	}
}

func TestPollingListenAndServeStopsOnContextCancellation(t *testing.T) {
	s := New(Config{QueueEnabled: true, PollInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServeContext(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServeContext: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("polling server did not stop after context cancellation")
	}
}
