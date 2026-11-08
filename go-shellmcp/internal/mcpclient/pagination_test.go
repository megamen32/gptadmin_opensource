package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

// Protocol fixtures below verify local pagination, not a live third-party MCP.
func toolsPaginationFixture(t *testing.T, page func(int, map[string]any) map[string]any) (supervisor.Agent, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result map[string]any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			params, _ := request.Params.(map[string]any)
			result = page(int(calls.Add(1)), params)
		default:
			t.Errorf("unexpected test request %q", request.Method)
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return supervisor.Agent{Ref: "paginated", Transport: "streamable-http", URL: server.URL, Enabled: true}, calls
}

func paginationList(c *Client, mode string, agent supervisor.Agent) ([]map[string]any, error) {
	switch mode {
	case "scoped":
		return c.ListToolsScoped(context.Background(), agent, strings.Repeat("a", 64))
	case "empty_scope":
		return c.ListToolsScoped(context.Background(), agent, "")
	default:
		return c.ListTools(context.Background(), agent)
	}
}

func TestListToolsPaginationPreservesSchemasAndOpaqueCursors(t *testing.T) {
	for _, mode := range []string{"legacy", "empty_scope", "scoped"} {
		t.Run(mode, func(t *testing.T) {
			rich := map[string]any{
				"name": "first", "description": strings.Repeat("schema description ", 200),
				"inputSchema":  map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}},
				"outputSchema": map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}},
				"annotations":  map[string]any{"readOnlyHint": true, "destructiveHint": false},
			}
			const cursor = "opaque /?= +"
			agent, calls := toolsPaginationFixture(t, func(page int, params map[string]any) map[string]any {
				switch page {
				case 1:
					if _, exists := params["cursor"]; exists {
						t.Error("initial tools/list unexpectedly included a cursor")
					}
					return map[string]any{"tools": []any{rich}, "nextCursor": cursor}
				case 2:
					if params["cursor"] != cursor {
						t.Errorf("opaque cursor changed: %v", params["cursor"])
					}
					// A continuation may contain no tools; enumeration must continue.
					return map[string]any{"tools": []any{}, "nextCursor": "last"}
				case 3:
					if params["cursor"] != "last" {
						t.Errorf("last cursor=%v", params["cursor"])
					}
					return map[string]any{"tools": []any{map[string]any{"name": "last", "inputSchema": map[string]any{"type": "object"}}}}
				default:
					t.Error("pagination did not stop at its terminal page")
					return map[string]any{"tools": []any{}}
				}
			})
			c := New()
			defer c.CloseAll()
			tools, err := paginationList(c, mode, agent)
			if err != nil || len(tools) != 2 || calls.Load() != 3 {
				t.Fatalf("tools/list discarded pages: tools=%d calls=%d error=%v", len(tools), calls.Load(), err)
			}
			if !reflect.DeepEqual(tools[0], rich) || tools[1]["name"] != "last" {
				t.Fatal("pagination changed or lost a tool definition")
			}
		})
	}
}

func TestListToolsPaginationRejectsIncompleteEnumeration(t *testing.T) {
	// These are the advertised finite enumeration budgets, not transport limits.
	const pagesLimit, toolsLimit, bytesLimit = 64, 4096, 8 << 20
	for _, mode := range []string{"legacy", "scoped"} {
		t.Run(mode, func(t *testing.T) {
			for _, test := range []struct {
				name      string
				wantError string
				wantCalls int32
				page      func(int, map[string]any) map[string]any
			}{
				{"repeated cursor", "repeated cursor", 2, func(_ int, _ map[string]any) map[string]any {
					return map[string]any{"tools": []any{map[string]any{"name": "partial"}}, "nextCursor": "same"}
				}},
				{"page budget", "page limit", pagesLimit, func(page int, _ map[string]any) map[string]any {
					return map[string]any{"tools": []any{}, "nextCursor": fmt.Sprint(page)}
				}},
				{"tool budget", "tool limit", 2, func(page int, _ map[string]any) map[string]any {
					if page == 2 {
						return map[string]any{"tools": []any{map[string]any{"name": "over"}}}
					}
					tools := make([]any, toolsLimit)
					for i := range tools {
						tools[i] = map[string]any{"name": fmt.Sprintf("tool-%d", i)}
					}
					return map[string]any{"tools": tools, "nextCursor": "over"}
				}},
				{"aggregate byte budget", "byte limit", 2, func(_ int, _ map[string]any) map[string]any {
					return map[string]any{"tools": []any{map[string]any{"name": "large", "description": strings.Repeat("x", bytesLimit/2)}}, "nextCursor": "over"}
				}},
				{"malformed continuation", "tools array", 2, func(page int, _ map[string]any) map[string]any {
					if page == 2 {
						return map[string]any{"tools": "not an array"}
					}
					return map[string]any{"tools": []any{map[string]any{"name": "partial"}}, "nextCursor": "tail"}
				}},
				{"malformed tool", "non-object tool", 1, func(_ int, _ map[string]any) map[string]any {
					return map[string]any{"tools": []any{map[string]any{"name": "partial"}, "not a tool"}}
				}},
				{"malformed cursor", "cursor", 1, func(_ int, _ map[string]any) map[string]any {
					return map[string]any{"tools": []any{map[string]any{"name": "partial"}}, "nextCursor": 42}
				}},
			} {
				t.Run(test.name, func(t *testing.T) {
					agent, calls := toolsPaginationFixture(t, test.page)
					c := New()
					defer c.CloseAll()
					tools, err := paginationList(c, mode, agent)
					if err == nil || !strings.Contains(err.Error(), test.wantError) || tools != nil {
						t.Fatalf("incomplete enumeration returned success/partial tools: count=%d error=%v", len(tools), err)
					}
					if calls.Load() != test.wantCalls {
						t.Fatalf("requests=%d, want bounded count %d", calls.Load(), test.wantCalls)
					}
				})
			}
		})
	}
}
