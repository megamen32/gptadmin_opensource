package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub"
	shell "github.com/megamen32/gptadmin/go-shellmcp/runtime"
)

// Exercises the actual embedded Node, queue admission/callback and MCP HTTP
// client. The child deliberately waits longer than the old 30s HTTP ceiling.
func TestEmbeddedQueuedChildCallOutlivesDiscoveryTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("real 31-second embedded HTTP integration")
	}
	t.Setenv(diagnosticsSocketEnv, "")
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		result := map[string]any{"protocolVersion": "2025-03-26", "tools": []any{map[string]any{"name": "slow", "inputSchema": map[string]any{"type": "object"}}}}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		if req.Method == "tools/call" {
			select {
			case <-time.After(31 * time.Second):
			case <-r.Context().Done():
				return
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "embedded-slow-result"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer child.Close()
	dir := t.TempDir()
	config := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(config, []byte(fmt.Sprintf(`[{"ref":"slow-child","transport":"streamable-http","url":%q,"enabled":true}]`, child.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, listener, hub.Config{ConfigDir: filepath.Join(dir, "hub"), CtlToken: "integration-control", ShellToken: "integration-shell", DefaultTimeout: 45 * time.Second}, shell.Config{Name: "timeout-integration", IdentityDir: filepath.Join(dir, "identity"), SpillDir: filepath.Join(dir, "spill"), OutboxDir: filepath.Join(dir, "outbox"), MCPConfig: config, ExecTimeout: 45, QueueTimeout: 1, PollInterval: time.Second, HeartbeatInterval: time.Second, DefaultHome: dir, DefaultCwd: dir})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("node stop: %v", err)
		}
	}()
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, path string, body []byte) []byte {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer integration-control")
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%s status=%d body=%s", path, res.StatusCode, raw)
		}
		return raw
	}
	raw := request("POST", "/mcp-relay/call", []byte(`{"target":"shell:timeout-integration","tool":"mcp_call","arguments":{"ref":"slow-child","name":"slow","arguments":{}},"background":true}`))
	var admission struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(raw, &admission); err != nil || admission.JobID == "" {
		t.Fatalf("admission: %s", raw)
	}
	deadline := time.Now().Add(50 * time.Second)
	for time.Now().Before(deadline) {
		raw = request("GET", "/mcp-relay/job/"+admission.JobID, nil)
		var status struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(raw, &status)
		if status.Status == "completed" || status.Status == "failed" {
			if !strings.Contains(string(raw), "embedded-slow-result") {
				t.Fatalf("nested child result failed: %s", raw)
			}
			t.Logf("embedded queue returned child result beyond 30s: %s", admission.JobID)
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("embedded task did not finish")
}
