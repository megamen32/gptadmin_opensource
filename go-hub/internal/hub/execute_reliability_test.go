package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These exercise the real handler and persisted configuration, not a ChatGPT
// client. Executable restart and live child-MCP probes are separate evidence.
func executeReliabilityRPC(t *testing.T, s *Server, method string, params map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer ctl")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var result map[string]any
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("MCP response status=%d body=%s", w.Code, w.Body.String())
	}
	return result
}

func executeReliabilityCall(t *testing.T, s *Server, name string, args map[string]any) map[string]any {
	t.Helper()
	return executeReliabilityRPC(t, s, "tools/call", map[string]any{"name": name, "arguments": args})
}

func TestExecuteReferenceLifecycleRecovery(t *testing.T) {
	for _, lifecycle := range []string{"expired", "evicted", "restarted"} {
		t.Run(lifecycle, func(t *testing.T) {
			s, _, _ := instructionFixture(t)
			defer func() { _ = s.Close() }()
			getSchema := func() map[string]any {
				response := executeReliabilityCall(t, s, "schema", map[string]any{"target": "hub", "tool": "demo", "detail": "full"})
				result := mapValue(mapValue(response["result"])["structuredContent"])
				if response["error"] != nil || firstString(result, "call_ref") == "" {
					t.Fatalf("schema unavailable: %v", response)
				}
				return result
			}
			ref := firstString(getSchema(), "call_ref")
			switch lifecycle {
			case "expired":
				item := s.operations[ref]
				item.Created = time.Now().Add(-operationTTL - time.Second)
				s.operations[ref] = item
			case "evicted":
				delete(s.operations, ref)
			case "restarted":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s = New(s.cfg)
			}
			for _, name := range []string{"execute", "callMcpTool", "call_mcp_tool"} {
				response := executeReliabilityCall(t, s, name, map[string]any{"ref": ref, "args": map[string]any{}})
				result := mapValue(response["result"])
				payload := mapValue(result["structuredContent"])
				if response["error"] != nil || result["isError"] != true || payload["executed"] != false || firstString(mapValue(payload["error"]), "code") != "schema_reference_unavailable" {
					t.Fatalf("stale ref must be actionable tool error without execution: %v", response)
				}
				if len(outputRows(result["content"])) == 0 {
					t.Fatal("recovery guidance hidden from text-only clients")
				}
			}
			if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
				t.Fatal("stale reference dispatched work")
			}
			// Do not replay the old operation: obtain an exact fresh schema, then
			// make a new harmless call carrying its current instruction receipt.
			fresh := getSchema()
			recovered := executeReliabilityCall(t, s, "execute", map[string]any{"ref": fresh["call_ref"], "args": map[string]any{}})
			result := mapValue(recovered["result"])
			payload := mapValue(result["structuredContent"])
			if recovered["error"] != nil || result["isError"] == true || hasMediaOrFailure(payload) || mapValue(payload["response"])["status"] != "ok" {
				t.Fatalf("fresh schema did not restore execution: %v", recovered)
			}
		})
	}
}

func TestExecuteExportStableWithLargeInventoryAndReconnect(t *testing.T) {
	s, _, _ := instructionFixture(t)
	defer func() { _ = s.Close() }()
	catalog := func() any {
		response := executeReliabilityRPC(t, s, "tools/list", map[string]any{})
		if response["error"] != nil {
			t.Fatalf("tools/list failed: %v", response)
		}
		tools := outputRows(mapValue(response["result"])["tools"])
		var names []string
		for _, row := range tools {
			names = append(names, firstString(mapValue(row), "name"))
		}
		if !reflect.DeepEqual(names, []string{"ui", "discover", "schema", "execute", "job", "expand"}) {
			t.Fatalf("routing catalog changed: %v", names)
		}
		return tools
	}
	baseline := catalog()
	// Real inventory transformation, with repeated child names on many hosts.
	// These inventory fixtures do not pretend to execute upstream MCP tools.
	for i := 0; i < 80; i++ {
		id := fmt.Sprintf("shell:fixture-%03d", i)
		s.agents[id] = &Agent{AgentID: id, Name: id, Kind: "virtual_shell", Status: "online", LastSeen: nowFloat(), Meta: map[string]any{
			"approved": true, "diagnostics": strings.Repeat("inventory diagnostic ", 200),
			"mcp_agents": []any{
				map[string]any{"ref": "Browser", "enabled": true, "transport": "streamable-http"},
				map[string]any{"ref": "Memory", "enabled": true, "transport": "stdio"},
			},
		}}
	}
	if err := s.saveRegistryStateLocked(); err != nil {
		t.Fatal(err)
	}
	for stage := 0; stage < 2; stage++ {
		if stage == 1 {
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = New(s.cfg)
		}
		// A new initialize request represents a fresh client connection; it
		// cannot change the facade schema based on cached inventory or order.
		init := executeReliabilityRPC(t, s, "initialize", map[string]any{"protocolVersion": "2025-11-25"})
		if init["error"] != nil || mapValue(init["result"])["protocolVersion"] != "2025-11-25" {
			t.Fatalf("reinitialize failed: %v", init)
		}
		if !reflect.DeepEqual(catalog(), baseline) {
			t.Fatal("inventory/reinitialize/restart changed public tool schemas")
		}
		compact := executeReliabilityCall(t, s, "discover", map[string]any{"detail": "compact", "query": "fixture-079"})
		view := mapValue(mapValue(compact["result"])["structuredContent"])
		rows := outputRows(view["servers"])
		if compact["error"] != nil || len(rows) < 3 || view["mesh"] != nil {
			t.Fatalf("compact discovery lost shell/children: %v", compact)
		}
		for _, row := range rows {
			if mapValue(row)["meta"] != nil || mapValue(row)["capabilities"] != nil {
				t.Fatal("compact discovery leaked inventory diagnostics")
			}
		}
		schema := executeReliabilityCall(t, s, "schema", map[string]any{"target": "shell:fixture-079", "tool": "shell_exec", "detail": "compact"})
		selected := mapValue(mapValue(schema["result"])["structuredContent"])
		definitions := outputRows(mapValue(selected["response"])["tools"])
		if len(definitions) != 1 || firstString(mapValue(definitions[0]), "name") != "shell_exec" || mapValue(definitions[0])["inputSchema"] == nil || firstString(selected, "call_ref") == "" {
			t.Fatalf("exact compact shell schema not callable: %v", schema)
		}
		// Use a real safe Hub operation; never fake a child execution result.
		execution := executeReliabilityCall(t, s, "execute", map[string]any{"target": "hub", "tool": "demo", "context": selected["context"]})
		payload := mapValue(mapValue(execution["result"])["structuredContent"])
		if execution["error"] != nil || mapValue(payload["response"])["status"] != "ok" {
			t.Fatalf("execute unusable after discovery/reconnect: %v", execution)
		}
	}
}

func TestExecuteUnavailableReferenceDoesNotWeakenGates(t *testing.T) {
	s, r, _ := instructionFixture(t)
	defer s.Close()
	// The non-MCP facade gets the same non-execution/recovery contract.
	direct := mapValue(s.appsSDKCallForRequest(r, "execute", map[string]any{"ref": "op_gone"}))
	if direct["executed"] != false || firstString(mapValue(direct["error"]), "code") != "schema_reference_unavailable" {
		t.Fatalf("direct facade lost reference recovery: %v", direct)
	}
	// An explicit new call still needs the normal current instruction context.
	blocked := executeReliabilityCall(t, s, "execute", map[string]any{"target": "hub", "tool": "code_mode", "args": map[string]any{"code": "return 42;"}})
	payload := mapValue(mapValue(blocked["result"])["structuredContent"])
	if payload["executed"] != false || firstString(mapValue(payload["error"]), "code") != "instructions_required" {
		t.Fatalf("reference recovery bypassed instruction gate: %v", blocked)
	}
	for _, req := range []*http.Request{
		requestWithAuthClaims(r, map[string]any{"scope": "gptadmin.read"}),
		requestWithAccessProfile(r, AccessProfile{ID: "restricted", AccessMode: "full", AllowedTargets: []string{"hub"}, AllowedTools: []string{"discover", "schema"}}),
	} {
		for _, tool := range compactFacadeTools(req, false) {
			if firstString(tool, "name") == "execute" {
				t.Fatal("restricted profile/read-only caller gained Execute")
			}
		}
		if authorizeFacadeCall(req, "execute", map[string]any{"target": "shell:fixture", "tool": "shell_exec"}) == nil {
			t.Fatal("restricted caller gained execution")
		}
	}
}
