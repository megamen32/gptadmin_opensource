package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

// A node must reject malformed catalog rows before its connector allowlist
// removes them, otherwise it can advertise an incomplete catalog as complete.
func TestChildToolsRejectsInvalidNamesBeforeAllowlist(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{"missing", nil},
		{"blank", " \t\n"},
		{"non_string", 42},
	} {
		t.Run(test.name, func(t *testing.T) {
			malformed := map[string]any{"inputSchema": map[string]any{"type": "object"}}
			if test.value != nil {
				malformed["name"] = test.value
			}
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
					result = map[string]any{"tools": []any{
						map[string]any{"name": "allowed", "inputSchema": map[string]any{"type": "object"}},
						malformed,
					}}
				default:
					http.Error(w, "unexpected method", http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": result})
			}))
			defer child.Close()
			s := New(Config{MCPConfig: filepath.Join(t.TempDir(), "mcp.json"), SpillDir: t.TempDir()})
			defer s.Close()
			agent := supervisor.Agent{Ref: "invalid-name", Transport: "streamable-http", URL: child.URL, Enabled: true, ToolAllowlist: []string{"allowed"}}
			if err := s.supervisor.Upsert(agent); err != nil {
				t.Fatal(err)
			}
			result, err := s.DirectChildTools(context.Background(), map[string]any{"ref": agent.Ref})
			if err == nil || result != nil {
				t.Fatalf("malformed tool was hidden by allowlist and returned as complete: result=%v error=%v", result, err)
			}
			if !strings.Contains(err.Error(), "invalid name") {
				t.Fatalf("expected malformed-name error, got %v", err)
			}
		})
	}
}
