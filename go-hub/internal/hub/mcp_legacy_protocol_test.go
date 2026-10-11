package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Fast unit: detects initialize returning a protocol unsupported by a legacy
// native SDK. Each case normally <1s; the complete focused run maximum is180s
// including setup. Native88 connection/tool execution is separate acceptance.
func TestMCPLegacyProtocolNegotiation(t *testing.T) {
	s := New(Config{CtlToken: "fixture-owner"})
	defer s.Close()
	for _, path := range []string{"/mcp", "/server/hub/mcp"} {
		for _, version := range []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25", mcpProtocolVersion} {
			t.Run(path+"/"+version, func(t *testing.T) {
				body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 123, "method": "initialize", "params": map[string]any{"protocolVersion": version, "clientInfo": map[string]any{"name": "native-legacy", "version": "1"}, "capabilities": map[string]any{}}})
				req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer fixture-owner")
				req.Header.Set("Content-Type", "application/json")
				// Legacy clients normally omit the version header on initialize.
				w := httptest.NewRecorder()
				if path == "/mcp" {
					s.mcpEndpoint(w, req)
				} else {
					s.serverMCPEndpoint(w, req)
				}
				var response map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if w.Code != 200 || mapValue(response["result"])["protocolVersion"] != version || w.Header().Get("MCP-Protocol-Version") != version {
					t.Fatalf("supported client protocol changed: status=%d header=%q result=%v", w.Code, w.Header().Get("MCP-Protocol-Version"), response)
				}
				if response["id"] != float64(123) {
					t.Fatal("initialize request ID changed")
				}
			})
		}
	}
}

// Fast unit: negotiated legacy requests must remain usable without modern
// routing headers; success requires the real Hub read-only demo handler.
func TestMCPLegacyProtocolToolJourney(t *testing.T) {
	s := New(Config{CtlToken: "fixture-owner"})
	defer s.Close()
	for _, path := range []string{"/mcp", "/server/hub/mcp"} {
		for _, method := range []string{"notifications/initialized", "tools/list", "tools/call"} {
			t.Run(path+"/"+method, func(t *testing.T) {
				params := map[string]any{}
				body := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
				if method != "notifications/initialized" {
					body["id"] = 456
				}
				if method == "tools/call" {
					params["name"], params["arguments"] = "demo", map[string]any{}
				}
				encoded, _ := json.Marshal(body)
				req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
				req.Header.Set("Authorization", "Bearer fixture-owner")
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("MCP-Protocol-Version", "2025-11-25")
				w := httptest.NewRecorder()
				if path == "/mcp" {
					s.mcpEndpoint(w, req)
				} else {
					s.serverMCPEndpoint(w, req)
				}
				if w.Header().Get("MCP-Protocol-Version") != "2025-11-25" {
					t.Fatalf("response switched protocol: %q", w.Header().Get("MCP-Protocol-Version"))
				}
				if method == "notifications/initialized" {
					if w.Code != 204 {
						t.Fatalf("initialization notification rejected: %d", w.Code)
					}
					return
				}
				var response map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if w.Code != 200 || response["error"] != nil || response["result"] == nil {
					t.Fatalf("legacy operation failed: %d %v", w.Code, response)
				}
			})
		}
	}
}
