package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMCP20260728StatelessDiscoverAndToolsList(t *testing.T) {
	s := New(Config{Token: "test", SpillDir: t.TempDir()})
	for _, tc := range []struct {
		name      string
		body      string
		wantField string
	}{
		{"discover", `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`, "supportedVersions"},
		{"tools-list", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, "tools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(tc.body))
			req.Header.Set("Authorization", "Bearer test")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("MCP-Protocol-Version", "2026-07-28")
			req.Header.Set("Mcp-Method", map[string]string{"discover": "server/discover", "tools-list": "tools/list"}[tc.name])
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("MCP-Protocol-Version"); got != "2026-07-28" {
				t.Fatalf("protocol header=%q", got)
			}
			var rpc struct {
				Result map[string]any `json:"result"`
				Error  any            `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &rpc); err != nil {
				t.Fatal(err)
			}
			if rpc.Error != nil {
				t.Fatalf("rpc error=%v", rpc.Error)
			}
			if _, ok := rpc.Result[tc.wantField]; !ok {
				t.Fatalf("missing %s in %v", tc.wantField, rpc.Result)
			}
			if rpc.Result["resultType"] != "complete" {
				t.Fatalf("resultType=%v", rpc.Result)
			}
			if tc.name == "discover" {
				versions, ok := rpc.Result["supportedVersions"].([]any)
				if !ok || len(versions) != 1 || versions[0] != "2026-07-28" || rpc.Result["cacheScope"] != "public" {
					t.Fatalf("discover=%v", rpc.Result)
				}
			} else if rpc.Result["ttlMs"] == nil || rpc.Result["cacheScope"] != "private" {
				t.Fatalf("list cache metadata=%v", rpc.Result)
			}
		})
	}
}
