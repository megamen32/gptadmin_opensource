package node

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub"
	shell "github.com/megamen32/gptadmin/go-shellmcp/runtime"
)

// transferUXHarness starts a real Hub plus a real in-process Node executor and
// returns helpers for MCP facade calls with the control token.
func transferUXHarness(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	router := hub.New(hub.Config{ConfigDir: filepath.Join(dir, "hub"), CtlToken: "binary-test-control-token", ShellToken: "binary-test-shell-token", DefaultTimeout: 20 * time.Second})
	t.Cleanup(func() { _ = router.Close() })
	web := httptest.NewServer(router.Handler())
	t.Cleanup(web.Close)
	cfg := shell.Config{Name: "transfer-ux-facade", Token: "binary-test-local-token-12345678901234567890", HubURL: web.URL, IdentityDir: filepath.Join(dir, "identity"), SpillDir: filepath.Join(dir, "spill"), OutboxDir: filepath.Join(dir, "outbox"), QueueEnabled: true, Mode: "long_poll", QueueTimeout: 1, HeartbeatEnabled: true, HeartbeatInterval: time.Second, PollInterval: time.Second, ExecTimeout: 20, LogLimit: 8192, DisableSelfUpdate: true, DefaultHome: dir, DefaultCwd: dir, InspectRoots: []string{dir}}
	executor := shell.New(cfg)
	t.Cleanup(func() { _ = executor.Close() })
	name, identity, err := executor.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err = router.RegisterLocalExecutor(name, identity, cfg.Token); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- executor.ListenAndServeContext(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return web, name, dir
}

func transferUXMCP(t *testing.T, web *httptest.Server, method string, params map[string]any) (any, []byte) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	request, _ := http.NewRequest("POST", web.URL+"/mcp", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer binary-test-control-token")
	request.Header.Set("Content-Type", "application/json")
	response, e := http.DefaultClient.Do(request)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("MCP status=%d body=%s", response.StatusCode, raw)
	}
	var frame map[string]any
	if e = json.Unmarshal(raw, &frame); e != nil {
		t.Fatalf("response=%s error=%v", raw, e)
	}
	return frame["result"], raw
}

func TestSearchRowsCarryTargetTitle(t *testing.T) {
	web, name, _ := transferUXHarness(t)
	// Wait for the executor heartbeat to publish the catalog.
	deadline := time.Now().Add(15 * time.Second)
	for {
		result, raw := transferUXMCP(t, web, "tools/call", map[string]any{"name": "search", "arguments": map[string]any{"query": "file_transfer", "limit": 20}})
		sc, _ := result.(map[string]any)
		rows := anySlice(mapValue(sc["structuredContent"])["results"])
		titles := map[string]any{}
		for _, row := range rows {
			r := mapValue(row)
			if target, _ := r["target"].(string); target != "" {
				titles[target] = r["target_title"]
			}
		}
		if len(titles) > 0 {
			// Every catalog row for the enrolled executor must expose a title.
			for target, title := range titles {
				if title == nil || strings.TrimSpace(fmt.Sprint(title)) == "" {
					t.Fatalf("target %q has no target_title: %s", target, raw)
				}
			}
			// The enrolled host's own file tools appear under a short target id
			// and carry a human-readable title that names the host.
			found := false
			for target, title := range titles {
				if strings.HasPrefix(target, "f") && len(target) == 13 && strings.Contains(fmt.Sprint(title), name) {
					found = true
				}
			}
			if !found {
				t.Fatalf("no short file target titled with %q in %v", name, titles)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("search never returned results: %s", raw)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestShellExecRelaysStdoutBase64ForCSI(t *testing.T) {
	web, name, _ := transferUXHarness(t)
	code := fmt.Sprintf("return await mcp.call(%q, %q, %s);", "shell:"+name, "shell_exec", `{"argv":["printf","csi \u001b[m \u001b[H probe\n"]}`)
	deadline := time.Now().Add(15 * time.Second)
	for {
		result, raw := transferUXMCP(t, web, "tools/call", map[string]any{"name": "exec", "arguments": map[string]any{"code": code, "max_output_bytes": 65536, "timeout_ms": 30000}})
		sc, _ := result.(map[string]any)
		inner := mapValue(sc["structuredContent"])
		if first, _ := inner["status"].(string); first == "completed" {
			payload := mapValue(inner["result"])
			if payload == nil {
				t.Fatalf("no shell result: %s", raw)
			}
			stdout, _ := payload["stdout"].(string)
			if !strings.Contains(stdout, "csi") {
				t.Fatalf("stdout=%q", stdout)
			}
			b64, _ := payload["stdout_base64"].(string)
			if b64 == "" {
				t.Fatalf("stdout_base64 missing for CSI output: %s", raw)
			}
			decoded, err := decodeBase64String(b64)
			if err != nil || string(decoded) != stdout {
				t.Fatalf("stdout_base64 mismatch: %v", err)
			}
			if bytes.Contains(decoded, []byte{0x1b}) != true {
				t.Fatal("decoded tail must contain ESC bytes")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("shell_exec never completed: %s", raw)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func decodeBase64String(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func mapValue(value any) map[string]any {
	m, _ := value.(map[string]any)
	return m
}
