package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/sshexec"
)

func TestMCPNativeArgvRealAuthenticatedCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX printf")
	}
	server := New(Config{Token: "test", Name: "test-shell", LogLimit: 1 << 16, ExecTimeout: 12, SpillDir: t.TempDir()})
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	payload := "He said \"hello\" and it's safe. 🌍 $(touch " + marker + "); $HOME"
	requestBody := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "shell_exec", "arguments": map[string]any{"argv": []string{"printf", "%s", payload}, "timeout": 8},
	}}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP MCP failed: %d %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	result := body["result"].(map[string]any)
	structured := result["structuredContent"].(map[string]any)
	command := structured["result"].(map[string]any)
	if command["stdout"] != payload || command["returncode"] != float64(0) || command["stderr"] != "" {
		t.Fatalf("native argv was changed: %#v", command)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell metacharacters expanded: %v", err)
	}
}

func TestMCPNativeArgvSchemaAndValidation(t *testing.T) {
	s := New(Config{Name: "test-shell", LogLimit: 8192, ExecTimeout: 6, SpillDir: t.TempDir()})
	found := false
	for _, entry := range s.mcpTools() {
		if entry["name"] != "shell_exec" {
			continue
		}
		found = true
		schema := entry["inputSchema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		argv, ok := props["argv"].(map[string]any)
		if !ok || argv["minItems"] != 1 || argv["maxItems"] != shell.MaxArgvCount || schema["oneOf"] == nil {
			t.Fatalf("MCP schema does not describe mutually exclusive direct argv: %#v", schema)
		}
	}
	if !found {
		t.Fatal("shell_exec tool missing")
	}
	invalid := []map[string]any{
		{"cmd": "echo legacy", "argv": []string{"printf", "x"}},
		{"argv": []string{}},
		{"argv": []string{"printf", "nul\x00bad"}},
		{"argv": []string{" "}},
		{},
	}
	for i, args := range invalid {
		result, err := s.mcpShellExec(context.Background(), args)
		if err == nil || result != nil {
			t.Fatalf("invalid request %d was accepted: %#v %v", i, result, err)
		}
	}
	legacy, err := s.mcpShellExec(context.Background(), map[string]any{"cmd": "printf backwards-compatible"})
	if err != nil || !strings.Contains(strings.TrimSpace(stringify(legacy)), "backwards-compatible") {
		t.Fatalf("legacy cmd no longer works: %v %#v", err, legacy)
	}
}

func TestNativeArgvSSHDoesNotFallbackToRemoteShell(t *testing.T) {
	server := &Server{sshClient: &sshexec.Client{}, cfg: Config{LogLimit: 8192, ExecTimeout: 4, SpillDir: t.TempDir()}}
	req := shell.Request{Argv: []string{"printf", "%s", "no-ssh-shell"}}
	if result := server.runShell(context.Background(), req); result.ReturnCode != -1 || !strings.Contains(result.Error, "SSH backend") {
		t.Fatalf("unsupported remote argv fell back to a shell: %+v", result)
	}
	if result := server.runShellStream(context.Background(), req, func(shell.Event) {}); result.ReturnCode != -1 || !strings.Contains(result.Error, "SSH backend") {
		t.Fatalf("unsupported stream argv fell back to a shell: %+v", result)
	}
}

func stringify(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
