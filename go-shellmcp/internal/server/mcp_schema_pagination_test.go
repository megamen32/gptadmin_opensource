package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

func TestChildToolsCompletenessRequiresAllPages(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "complete"
		if fail {
			name = "repeated_cursor"
		}
		t.Run(name, func(t *testing.T) {
			var pages atomic.Int32
			child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					http.Error(w, "invalid test request", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				var result map[string]any
				switch request["method"] {
				case "initialize":
					result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}}
				case "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
					return
				case "tools/list":
					page := pages.Add(1)
					if page == 1 || fail {
						result = map[string]any{"tools": []any{map[string]any{"name": "hidden"}}, "nextCursor": "tail"}
					} else {
						result = map[string]any{"tools": []any{map[string]any{"name": "allowed", "inputSchema": map[string]any{"type": "object"}}}}
					}
				default:
					http.Error(w, "unexpected method", http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": result})
			}))
			defer child.Close()
			s := New(Config{MCPConfig: filepath.Join(t.TempDir(), "mcp.json"), SpillDir: t.TempDir()})
			defer s.Close()
			agent := supervisor.Agent{Ref: "pages", Transport: "streamable-http", URL: child.URL, Enabled: true, ToolAllowlist: []string{"allowed"}}
			if err := s.supervisor.Upsert(agent); err != nil {
				t.Fatal(err)
			}
			result, err := s.DirectChildTools(context.Background(), map[string]any{"ref": "pages"})
			if fail {
				if err == nil || result != nil {
					t.Fatalf("incomplete child catalog returned success: result=%v error=%v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			body, _ := result["structuredContent"].(map[string]any)
			tools, _ := body["tools"].([]map[string]any)
			if body["schema_complete"] != true || body["schema_completeness"] != "complete" || len(tools) != 1 || tools[0]["name"] != "allowed" || pages.Load() != 2 {
				t.Fatalf("full bounded catalog did not preserve completeness/allowlist: result=%v pages=%d", body, pages.Load())
			}
		})
	}
}
