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
	staleDigest := fmt.Sprintf("%064d", 0)
	// The legacy schema contract gate is internal and unchanged. It refuses
	// stale metadata for every business call, whoever submits it.
	gate := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	blocked, isBlocked := s.validateSchemaContract(gate, "hub", map[string]any{
		"target": "hub", "tool": "demo",
		"schema_version": version, "schema_digest_sha256": staleDigest,
	})
	if !isBlocked || firstString(mapValue(blocked["error"]), "code") != "schema_mismatch" {
		t.Fatalf("opt-in MCP gate did not reject stale metadata: %v", blocked)
	}
	// The public surface refuses it as well: the exec facade forwards the
	// stale metadata to the same gate and the business tool never runs.
	code := fmt.Sprintf("return await mcp.call({target:'hub',tool:'demo',args:{probe:'stale'},schema_version:%q,schema_digest_sha256:%q});", version, staleDigest)
	arguments, err := json.Marshal(map[string]any{"code": code})
	if err != nil {
		t.Fatal(err)
	}
	stale := call(2, "exec", string(arguments))
	structured := mapValue(mapValue(stale["result"])["structuredContent"])
	receipt := mapValue(mapValue(structured["error"])["receipt"])
	if firstString(structured, "status") != "failed" ||
		intFromAny(receipt["http_status"]) != http.StatusConflict ||
		!strings.Contains(firstString(mapValue(structured["error"]), "message"), "schema metadata does not match") {
		t.Fatalf("opt-in MCP gate did not reject stale metadata: %v", structured)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", strings.NewReader(fmt.Sprintf(`{"target":"hub","tool_name":"hub_status","arguments":{},"schema_version":%q,"schema_digest_sha256":"%064d"}`, version, 0)))
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "schema_mismatch") {
		t.Fatalf("opt-in Actions gate status=%d body=%s", w.Code, w.Body.String())
	}
}
