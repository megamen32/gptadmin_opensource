package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestVirtualMCPsAreDefaultOffPersistedAndExposeDedicatedActions(t *testing.T) {
	state := filepath.Join(t.TempDir(), "virtual_mcps.json")
	cfg := Config{CtlToken: "ctl", VirtualMCPStateFile: state}
	s := New(cfg)

	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var payload *bytes.Reader
		if body == nil {
			payload = bytes.NewReader(nil)
		} else {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			payload = bytes.NewReader(encoded)
		}
		req := httptest.NewRequest(method, path, payload)
		req.Header.Set("Authorization", "Bearer ctl")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}

	initial := request(http.MethodGet, "/admin/api/virtual-mcps", nil)
	if initial.Code != http.StatusOK || strings.Contains(initial.Body.String(), `"enabled":true`) {
		t.Fatalf("virtual MCP defaults=%d body=%s", initial.Code, initial.Body.String())
	}
	if disabled := request(http.MethodGet, "/server/webhooks/actions/openapi.yaml", nil); disabled.Code != http.StatusNotFound {
		t.Fatalf("disabled virtual MCP endpoint status=%d body=%s", disabled.Code, disabled.Body.String())
	}

	rootWebhook := request(http.MethodPost, "/mcp", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": webhookRoutesListTool, "arguments": map[string]any{}},
	})
	if rootWebhook.Code != http.StatusOK || !strings.Contains(rootWebhook.Body.String(), "optional virtual MCP") {
		t.Fatalf("root MCP leaked webhook tool status=%d body=%s", rootWebhook.Code, rootWebhook.Body.String())
	}

	enabled := request(http.MethodPut, "/admin/api/virtual-mcps/network-proxy", map[string]any{"enabled": true})
	if enabled.Code != http.StatusOK || !strings.Contains(enabled.Body.String(), `"enabled":true`) {
		t.Fatalf("enable status=%d body=%s", enabled.Code, enabled.Body.String())
	}

	servers := request(http.MethodGet, "/mcp-relay/servers?detail=full", nil)
	if servers.Code != http.StatusOK || !strings.Contains(servers.Body.String(), `"server_id":"network-proxy"`) || strings.Contains(servers.Body.String(), `"server_id":"webhooks"`) {
		t.Fatalf("virtual discovery status=%d body=%s", servers.Code, servers.Body.String())
	}

	globalSchema := request(http.MethodGet, "/actions/openapi.yaml", nil)
	for _, forbidden := range []string{"/webhook-routes", "/webhooks/v1/", "/proxy-control/", "webhookToken:"} {
		if strings.Contains(globalSchema.Body.String(), forbidden) {
			t.Fatalf("global Custom GPT schema leaked %q: %s", forbidden, globalSchema.Body.String())
		}
	}

	virtualSchema := request(http.MethodGet, "/server/network-proxy/actions/openapi.yaml", nil)
	if virtualSchema.Code != http.StatusOK || !strings.Contains(virtualSchema.Body.String(), "network_proxy_request") || strings.Contains(virtualSchema.Body.String(), "webhook_routes_list") || strings.Count(virtualSchema.Body.String(), "scheme: bearer") != 1 {
		t.Fatalf("virtual schema status=%d body=%s", virtualSchema.Code, virtualSchema.Body.String())
	}
	webhooks := request(http.MethodPut, "/admin/api/virtual-mcps/webhooks", map[string]any{"enabled": true})
	if webhooks.Code != http.StatusOK {
		t.Fatalf("enable webhooks status=%d body=%s", webhooks.Code, webhooks.Body.String())
	}
	webhookCall := request(http.MethodPost, "/server/webhooks/mcp", map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": webhookRoutesListTool, "arguments": map[string]any{}},
	})
	if webhookCall.Code != http.StatusOK || !strings.Contains(webhookCall.Body.String(), `"routes"`) {
		t.Fatalf("enabled webhooks MCP call status=%d body=%s", webhookCall.Code, webhookCall.Body.String())
	}

	restarted := New(cfg)
	persisted := httptest.NewRequest(http.MethodGet, "/mcp-relay/servers", nil)
	persisted.Header.Set("Authorization", "Bearer ctl")
	persistedRec := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(persistedRec, persisted)
	if persistedRec.Code != http.StatusOK || !strings.Contains(persistedRec.Body.String(), `"server_id":"network-proxy"`) {
		t.Fatalf("virtual MCP did not survive restart status=%d body=%s", persistedRec.Code, persistedRec.Body.String())
	}
}
