package node

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub"
	shell "github.com/megamen32/gptadmin/go-shellmcp/runtime"
)

func TestRealExecutorRunsDurableQueuedJobAfterDrainRestart(t *testing.T) {
	dir := t.TempDir()
	hc := hub.Config{ConfigDir: dir, CtlToken: "test-control-token", ShellToken: "test-shell-token", DefaultTimeout: 10 * time.Second}
	cfg := shell.Config{Name: "drain-integration", Token: "test-local-executor-token-123456", IdentityDir: filepath.Join(dir, "identity"), SpillDir: filepath.Join(dir, "spill"), OutboxDir: filepath.Join(dir, "outbox"), QueueEnabled: true, Mode: "long_poll", QueueTimeout: 1, PollInterval: time.Second, HeartbeatEnabled: true, HeartbeatInterval: time.Second, DisableSelfUpdate: true, DefaultHome: dir, DefaultCwd: dir, ExecTimeout: 10}
	start := func() (*hub.Server, *httptest.Server, *shell.Server) {
		t.Helper()
		router := hub.New(hc)
		web := httptest.NewServer(router.Handler())
		cfg.HubURL = web.URL
		executor := shell.New(cfg)
		name, identity, err := executor.LocalIdentity()
		if err != nil {
			t.Fatal(err)
		}
		if err = router.RegisterLocalExecutor(name, identity, cfg.Token); err != nil {
			t.Fatal(err)
		}
		router.SetNodeRuntimeCounters(executor)
		t.Cleanup(func() { executor.Close(); web.Close(); router.Close() })
		return router, web, executor
	}
	call := func(web *httptest.Server, method, path string, body []byte) map[string]any {
		t.Helper()
		r, _ := http.NewRequest(method, web.URL+path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer test-control-token")
		r.Header.Set("Content-Type", "application/json")
		if path == "/mcp" {
			var rpc map[string]any
			_ = json.Unmarshal(body, &rpc)
			params := rpc["params"].(map[string]any)
			r.Header.Set("MCP-Protocol-Version", "2026-07-28")
			r.Header.Set("Mcp-Method", rpc["method"].(string))
			r.Header.Set("Mcp-Name", params["taskId"].(string))
		}

		res, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, res.StatusCode, raw)
		}
		var d map[string]any
		if err = json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	first, web, ex := start()
	body := []byte(`{"target":"shell:drain-integration","tool":"shell_exec","arguments":{"cmd":"printf before-drain >> durable-marker.txt; cat durable-marker.txt"},"background":true,"idempotency_key":"drain-restart-once"}`)
	admission := call(web, "POST", "/mcp-relay/call", body)
	id, _ := admission["job_id"].(string)
	if id == "" {
		t.Fatalf("no job: %v", admission)
	}
	taskBody, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tasks/get", "params": map[string]any{"taskId": id, "_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientInfo": map[string]any{"name": "durable-test", "version": "1"}, "io.modelcontextprotocol/clientCapabilities": map[string]any{"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}}}}})
	beforeRPC := call(web, "POST", "/mcp", taskBody)
	before, ok := beforeRPC["result"].(map[string]any)
	if !ok || before["createdAt"] == nil {
		t.Fatalf("missing original task time: %v", beforeRPC)
	}
	first.EnableNodeDrain("restart")
	if !first.NodeDrainStatus().ReadyToStop {
		t.Fatal("durable queued job blocks safe stop")
	}
	marker := filepath.Join(dir, "durable-marker.txt")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("job executed before executor start")
	}
	ex.Close()
	web.Close()
	first.Close()
	second, web2, ex2 := start()
	after := call(web2, "GET", "/mcp-relay/job/"+id+"?detail=full", nil)
	afterRPC := call(web2, "POST", "/mcp", taskBody)
	afterTask, ok := afterRPC["result"].(map[string]any)
	if !ok || afterTask["createdAt"] != before["createdAt"] {
		t.Fatalf("original task age lost: %v -> %v", before, afterRPC)
	}
	if after["status"] != "queued" {
		t.Fatalf("queue age/state lost: %v -> %v", before, after)
	}
	duplicate := call(web2, "POST", "/mcp-relay/call", body)
	if duplicate["job_id"] != id {
		t.Fatalf("duplicate ID: %v", duplicate)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ex2.ListenAndServeContext(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(20 * time.Second)
	var result map[string]any
	for time.Now().Before(deadline) {
		result = call(web2, "GET", "/mcp-relay/job/"+id+"?detail=full", nil)
		if result["status"] == "completed" || result["status"] == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	raw, _ := json.Marshal(result)
	if result["status"] != "completed" || !bytes.Contains(raw, []byte("before-drain")) {
		t.Fatalf("real execution failed: %s", raw)
	}
	contents, err := os.ReadFile(marker)
	if err != nil || string(contents) != "before-drain" {
		t.Fatalf("side effect is not exactly once: %q %v", contents, err)
	}
	second.EnableNodeDrain("after-result")
	// An already accepted long poll retains the Hub runtime timeout (55s).
	deadline = time.Now().Add(75 * time.Second)
	for !second.NodeDrainStatus().ReadyToStop && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !second.NodeDrainStatus().ReadyToStop {
		t.Fatal("running callbacks did not finish drain")
	}
}
