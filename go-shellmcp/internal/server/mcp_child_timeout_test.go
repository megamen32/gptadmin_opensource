package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueuedChildExecutionBudget(t *testing.T) {
	for _, mode := range []string{"success_without_parent_deadline", "execution_budget", "caller_deadline", "caller_cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "caller_deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 40*time.Millisecond)
				defer stop()
			}
			child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if req["method"] == "notifications/initialized" {
					w.WriteHeader(202)
					return
				}
				if req["method"] == "tools/call" {
					if mode == "caller_cancel" {
						cancel()
					}
					delay := 200 * time.Millisecond
					if mode == "execution_budget" {
						delay = 2 * time.Second
					}
					select {
					case <-time.After(delay):
					case <-r.Context().Done():
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "caller-result"}}}})
			}))
			defer child.Close()
			dir := t.TempDir()
			s := New(Config{MCPConfig: filepath.Join(dir, "mcp.json"), IdentityDir: dir, SpillDir: dir, OutboxDir: filepath.Join(dir, "outbox"), ExecTimeout: 1})
			defer s.Close()
			s.childMCP.HTTP.Timeout = 80 * time.Millisecond
			if _, err := s.mcpManage(map[string]any{"action": "upsert", "config": map[string]any{"ref": "child", "transport": "streamable-http", "url": child.URL, "enabled": true}}); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			result, err := s.callMCPTool(ctx, "mcp_call", map[string]any{"ref": "child", "name": "slow", "arguments": map[string]any{}})
			switch mode {
			case "success_without_parent_deadline":
				if err != nil || !strings.Contains(fmt.Sprint(result), "caller-result") {
					t.Fatalf("result=%v err=%v", result, err)
				}
			case "caller_cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			default:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("deadline: %v", err)
				}
				if mode == "execution_budget" && time.Since(started) < 900*time.Millisecond {
					t.Fatalf("execution stopped at discovery timeout: %v", err)
				}
			}
		})
	}
}
