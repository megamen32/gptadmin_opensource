package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLegacySchemaContractValidationExplicitOptIn(t *testing.T) {
	if (Config{}).SchemaContractValidation {
		t.Fatal("schema contract validation must default to disabled")
	}
	disabled := New(Config{CtlToken: "ctl", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	disabledSchema := postMCPRPC(t, disabled, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"schema","arguments":{"target":"hub"}}}`)
	disabledResponse := mapValue(mapValue(mapValue(disabledSchema["result"])["structuredContent"])["response"])
	if firstString(disabledResponse, "schema_version") != "" || firstString(disabledResponse, "schema_digest_sha256") != "" {
		t.Fatalf("disabled schema contract leaked metadata: %v", disabledResponse)
	}
	actionReq := httptest.NewRequest(http.MethodGet, "/actions/openapi.yaml", nil)
	actionRec := httptest.NewRecorder()
	disabled.Handler().ServeHTTP(actionRec, actionReq)
	if actionRec.Header().Get("Cache-Control") != "no-store" || strings.Contains(actionRec.Body.String(), "schema_version") || strings.Contains(actionRec.Body.String(), "schema_digest_sha256") {
		t.Fatalf("disabled Action contract exposed schema binding: cache=%q body=%s", actionRec.Header().Get("Cache-Control"), actionRec.Body.String())
	}

	s := New(Config{CtlToken: "ctl", SchemaContractValidation: true, DefaultTimeout: time.Second, PollMaxTimeout: time.Second})

	call := func(id int, name, arguments string) map[string]any {
		t.Helper()
		return postMCPRPC(t, s, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, name, arguments))
	}
	schema := call(1, "schema", `{"target":"hub"}`)
	response := mapValue(mapValue(mapValue(schema["result"])["structuredContent"])["response"])
	version := firstString(response, "schema_version")
	if version == "" {
		t.Fatalf("schema omitted version: %v", response)
	}
	stale := call(2, "execute", fmt.Sprintf(`{"target":"hub","tool":"demo","arguments":{"probe":"stale"},"schema_version":%q,"schema_digest_sha256":"%064d"}`, version, 0))
	staleJSON, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(staleJSON), "schema_mismatch") {
		t.Fatalf("opt-in MCP gate did not reject stale metadata: %s", staleJSON)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", strings.NewReader(fmt.Sprintf(`{"target":"hub","tool_name":"hub_status","arguments":{},"schema_version":%q,"schema_digest_sha256":"%064d"}`, version, 0)))
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "schema_mismatch") {
		t.Fatalf("opt-in Actions gate status=%d body=%s", w.Code, w.Body.String())
	}
}
