package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSystemResourceSummaryMCPToolIsTypedAndReadOnly(t *testing.T) {
	s := New(Config{Token: "t"})
	listReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	listReq.Header.Set("Authorization", "Bearer t")
	listRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("tools/list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var listBody map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &listBody); err != nil {
		t.Fatal(err)
	}
	tools := listBody["result"].(map[string]any)["tools"].([]any)
	var found bool
	for _, raw := range tools {
		tool := raw.(map[string]any)
		if tool["name"] != "system_resource_summary" {
			continue
		}
		found = true
		annotations, ok := tool["annotations"].(map[string]any)
		if !ok || annotations["readOnlyHint"] != true || annotations["destructiveHint"] != false {
			t.Fatalf("tool is not marked read-only: %#v", annotations)
		}
		schema := tool["inputSchema"].(map[string]any)
		if len(schema["properties"].(map[string]any)) != 0 || schema["additionalProperties"] != false {
			t.Fatalf("resource summary accepts arbitrary inputs: %#v", tool["inputSchema"])
		}
	}
	if !found {
		t.Fatalf("system_resource_summary absent from tools/list: %s", listRec.Body.String())
	}

	callReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"system_resource_summary","arguments":{}}}`))
	callReq.Header.Set("Authorization", "Bearer t")
	callRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(callRec, callReq)
	if callRec.Code != http.StatusOK {
		t.Fatalf("tools/call status=%d body=%s", callRec.Code, callRec.Body.String())
	}
	var callBody map[string]any
	if err := json.Unmarshal(callRec.Body.Bytes(), &callBody); err != nil {
		t.Fatal(err)
	}
	structured := callBody["result"].(map[string]any)["structuredContent"].(map[string]any)
	for _, key := range []string{"sampled_at", "status", "cpu", "memory", "root_filesystem"} {
		if _, ok := structured[key]; !ok {
			t.Errorf("typed sample missing %q: %#v", key, structured)
		}
	}
	if _, ok := structured["system"]; ok {
		t.Fatal("resource summary mislabeled static system metadata as a sample")
	}
}
