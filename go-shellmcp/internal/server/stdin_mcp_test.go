package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPStdinCarriesExactMultilineScript(t *testing.T) {
	server := New(Config{Token: "fixture", Name: "fixture", LogLimit: 8192, ExecTimeout: 10, SpillDir: t.TempDir()})
	marker := filepath.Join(t.TempDir(), "no-command-substitution")
	source := "line one\nquote: it's \"safe\"\n$HOME $(touch " + marker + ")\nПривет 🌍\n"
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "shell_exec", "arguments": map[string]any{"argv": []string{"cat"}, "stdin": source}}}
	encoded, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer fixture")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, request)
	if rec.Code != 200 {
		t.Fatalf("MCP HTTP status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	data := result["result"].(map[string]any)["structuredContent"].(map[string]any)["result"].(map[string]any)
	if data["stdout"] != source || data["returncode"] != float64(0) {
		t.Fatalf("stdin not roundtripped: %#v", data)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stdin executed as code: %v", err)
	}
}

func TestShellQueueForwardsStdinWithoutRewriting(t *testing.T) {
	source := "print(\"it's <safe> 🌍\")\n"
	queued := hub.QueueJob{ID: "queued", ToolName: "shell_exec", Arguments: map[string]any{"argv": []any{"cat"}, "stdin": source}}
	req := shellRequestFromQueueJob(queued, t.TempDir())
	if req.Cmd != "" || len(req.Argv) != 1 || req.Argv[0] != "cat" || req.Stdin != source {
		t.Fatalf("stdin lost in queue: %+v", req)
	}
	output := shell.Run(context.Background(), req, 4096)
	if output.ReturnCode != 0 || output.Stdout != source {
		t.Fatalf("stdin execution failed: %+v", output)
	}
}

func TestMCPStdinRejectsNULAndOversizedContent(t *testing.T) {
	s := New(Config{Name: "fixture", LogLimit: 4096, ExecTimeout: 6, SpillDir: t.TempDir()})
	values := []string{"nul" + string(rune(0)), "bad" + string([]byte{255}), strings.Repeat("x", shell.MaxStdinBytes+1)}
	for _, source := range values {
		result, err := s.mcpShellExec(context.Background(), map[string]any{"argv": []string{"cat"}, "stdin": source})
		if err == nil || result != nil {
			t.Fatalf("invalid stdin accepted: %d %#v %v", len(source), result, err)
		}
	}
}
