package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func instructionFixture(t *testing.T) (*Server, *http.Request, string) {
	t.Helper()
	s := newSecretTestServer(t)
	s.cfg.InstructionContextRequired = true
	path := filepath.Join(s.cfg.ConfigDir, "context-fixture.md")
	if err := os.WriteFile(path, []byte("Fixture instructions version one."), 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.StartupInstructionsFile = path
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer ctl")
	return s, r, path
}
func instructionDiscovery(t *testing.T, s *Server, r *http.Request) map[string]any {
	t.Helper()
	out := mapValue(s.appsSDKCallForRequest(r, "discover", map[string]any{}))
	if firstString(out, "context") == "" || firstString(out, "instructions") == "" {
		t.Fatalf("missing bootstrap text and context: %v", out)
	}
	return out
}
func TestInstructionContextDeliveryAndReuse(t *testing.T) {
	s, r, _ := instructionFixture(t)
	initial := instructionDiscovery(t, s, r)
	repeated := mapValue(s.appsSDKCallForRequest(r, "discover", map[string]any{"context": initial["context"]}))
	if repeated["instructions"] != nil || repeated["context"] != nil {
		t.Fatal("current context repeated the instruction document")
	}
	fresh := instructionDiscovery(t, s, r)
	if fresh["context"] == initial["context"] {
		t.Fatal("separate agent contexts were collapsed")
	}
}
func TestInstructionContextRequiredBeforePureCode(t *testing.T) {
	s, r, _ := instructionFixture(t)
	args := map[string]any{"target": "hub", "tool": "code_mode", "args": map[string]any{"code": "return {answer:42};"}}
	blocked := mapValue(s.appsSDKCallForRequest(r, "execute", args))
	if blocked["executed"] != false || firstString(mapValue(blocked["error"]), "code") != "instructions_required" {
		t.Fatalf("missing pre-execution receipt: %v", blocked)
	}
	args["context"] = blocked["context"]
	out := mapValue(s.appsSDKCallForRequest(r, "execute", args))
	if mapValue(out["result"])["answer"] != json.Number("42") {
		t.Fatalf("current context not accepted: %v", out)
	}
	if out["response"] != nil || out["limits"] != nil {
		t.Fatal("compact output retained runtime diagnostics")
	}
	if firstString(out, "operation_id") == "" {
		t.Fatal("diagnostic expansion reference absent")
	}
	delete(args, "context")
	if again := mapValue(s.appsSDKCallForRequest(r, "execute", args)); again["executed"] != false {
		t.Fatal("receipt delivery became an account-global acknowledgement")
	}
}
func TestInstructionContextExactFullSchemaAndReference(t *testing.T) {
	s, r, _ := instructionFixture(t)
	schema := mapValue(s.appsSDKCallForRequest(r, "schema", map[string]any{"target": "hub", "tool": "secret_request", "detail": "full"}))
	definitions := outputRows(mapValue(schema["response"])["tools"])
	if len(definitions) != 1 || firstString(mapValue(definitions[0]), "name") != "secret_request" {
		t.Fatal("full detail changed selected schema scope")
	}
	ref := firstString(schema, "call_ref")
	if ref == "" || schema["operation_id"] != nil {
		t.Fatal("schema has a missing or duplicate reference")
	}
	issued := mapValue(s.appsSDKCallForRequest(r, "execute", map[string]any{"ref": ref, "args": map[string]any{"label": "fixture"}, "detail": "full"}))
	if firstString(mapValue(issued["response"]), "secret_ref") == "" {
		t.Fatalf("schema reference did not carry its instruction context: %v", issued)
	}
	if s.expandOperation(r, map[string]any{"id": ref})["status"] == "failed" {
		t.Fatal("schema reference cannot expand")
	}
}
func TestInstructionContextRefreshAndInspection(t *testing.T) {
	s, r, path := instructionFixture(t)
	schema := mapValue(s.appsSDKCallForRequest(r, "schema", map[string]any{"target": "hub", "tool": "code_mode"}))
	if err := os.WriteFile(path, []byte("Fixture instructions version two."), 0600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"ref": schema["call_ref"], "args": map[string]any{"code": "return {answer:42};"}}
	blocked := mapValue(s.appsSDKCallForRequest(r, "execute", args))
	if blocked["executed"] != false || !strings.Contains(firstString(blocked, "instructions"), "version two") {
		t.Fatalf("stale reference did not refresh rules: %v", blocked)
	}
	args["context"] = blocked["context"]
	out := mapValue(s.appsSDKCallForRequest(r, "execute", args))
	if mapValue(out["result"])["answer"] != json.Number("42") {
		t.Fatalf("refreshed rules not usable: %v", out)
	}
	if s.expandOperation(r, map[string]any{"id": schema["call_ref"]})["status"] == "failed" {
		t.Fatal("instruction refresh blocked inspection")
	}
}
func TestInstructionContextMetadataReadIsNotDelivery(t *testing.T) {
	s, r, _ := instructionFixture(t)
	if s.appsSDKResourceReceipt(r, startupInstructionsResourceURI)["ok"] != true {
		t.Fatal("resource metadata unavailable")
	}
	if s.validInstructionContext(r) {
		t.Fatal("metadata-only read marked instructions as delivered")
	}
}
func TestInstructionContextMCPMetadataTransport(t *testing.T) {
	s, r, _ := instructionFixture(t)
	initial := instructionDiscovery(t, s, r)
	updated := requestWithInstructionContext(r, map[string]any{"_meta": map[string]any{instructionContextMeta: initial["context"]}})
	if !s.validInstructionContext(updated) {
		t.Fatal("MCP context metadata not accepted")
	}
	if toolArgsFromTopLevel(map[string]any{"context": initial["context"], "cmd": "printf ok"})["context"] != nil {
		t.Fatal("context contaminated tool arguments")
	}
}

func TestInstructionContextToolErrorEnvelope(t *testing.T) {
	s, r, _ := instructionFixture(t)
	failed := s.instructionFailure(r)
	result := mcpToolResult(failed)
	if result["isError"] != true || mapValue(result["structuredContent"])["executed"] != false {
		t.Fatal("instruction refusal is not a protocol tool error")
	}
}
func TestInstructionContextMetadataStrippingPreservesOtherKeys(t *testing.T) {
	args := map[string]any{"context": "fixture", "_meta": map[string]any{instructionContextMeta: "fixture", "caller-note": "keep"}, "label": "fixture"}
	clean := withoutInstructionContext(args)
	if clean["context"] != nil || mapValue(clean["_meta"])[instructionContextMeta] != nil || mapValue(clean["_meta"])["caller-note"] != "keep" || clean["label"] != "fixture" {
		t.Fatal("control metadata was retained or business data removed")
	}
	if mapValue(args["_meta"])[instructionContextMeta] != "fixture" {
		t.Fatal("input metadata was mutated")
	}
}
func TestInstructionContextTaskAcceptanceRequiresCurrentRules(t *testing.T) {
	s, r, _ := instructionFixture(t)
	_, err := s.mcpTaskUpdate(r, "fixture-task", map[string]any{"approval": map[string]any{"action": "accept", "content": map[string]any{"approved": true}}}, "hub")
	failure := mapValue(mapValue(err)["data"])
	if firstString(mapValue(failure["error"]), "code") != "instructions_required" || failure["executed"] != false {
		t.Fatalf("task acceptance did not require delivery: %v", err)
	}
	_, declined := s.mcpTaskUpdate(r, "fixture-task", map[string]any{"approval": map[string]any{"action": "decline"}}, "hub")
	if firstString(mapValue(mapValue(mapValue(declined)["data"])["error"]), "code") == "instructions_required" {
		t.Fatal("decline should not require current instructions")
	}
}
func TestInstructionContextHumanSettingsRouteRemainsUsable(t *testing.T) {
	s, _, _ := instructionFixture(t)
	r := httptest.NewRequest(http.MethodPut, "/admin/api/settings", strings.NewReader(`{"tool_output_verbose":false}`))
	r.Header.Set("Authorization", "Bearer ctl")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "instructions_required") {
		t.Fatalf("human operator settings blocked: %d %s", w.Code, w.Body.String())
	}
}
func TestInstructionContextPendingFacadeKeepsRequest(t *testing.T) {
	s, r, _ := instructionFixture(t)
	initial := instructionDiscovery(t, s, r)
	result := mapValue(s.appsSDKCallForRequest(r, "approve_pending_server", map[string]any{"server_id": "shell:unknown-fixture", "context": initial["context"]}))
	if firstString(mapValue(result["error"]), "code") == "instructions_required" {
		t.Fatal("request-aware facade dropped caller context")
	}
}

// One credential can use different bound profiles. Reusing identical rule text
// must not make a receipt transferable to another profile.
func TestInstructionContextBoundProfileRules(t *testing.T) {
	s, caller, _ := instructionFixture(t)
	if err := s.updateInstructionSets(func(sets map[string]InstructionSet) error {
		for id, content := range map[string]string{"rules-a": "Rules for profile A.", "rules-b": "Rules for profile B."} {
			sets[id] = InstructionSet{ID: id, Content: content, Version: instructionSetVersion(content)}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ profile, set, want string }{
		{"profile-a", "rules-a", "Rules for profile A."},
		{"profile-b", "rules-b", "Rules for profile B."},
		{"profile-shared", "rules-a", "Rules for profile A."},
		{"profile-default", "", "Fixture instructions version one."},
	}
	requests := make([]*http.Request, len(cases))
	receipts := make([]string, len(cases))
	for i, tc := range cases {
		requests[i] = requestWithAccessProfile(caller, AccessProfile{
			ID: tc.profile, InstructionSetID: tc.set, Version: 1,
			AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted,
			AllowedTargets: []string{"*"}, AllowedTools: []string{"*"},
		})
		issued := instructionDiscovery(t, s, requests[i])
		if issued["instructions"] != tc.want || issued["profile"] != tc.profile {
			t.Fatalf("profile %s received the wrong rules: %v", tc.profile, issued)
		}
		receipts[i] = firstString(issued, "context")
		withContext := requestWithInstructionContext(requests[i], map[string]any{"context": receipts[i]})
		if failure := s.requireInstructionContext(withContext, "hub", "code_mode"); failure != nil {
			t.Fatalf("own profile receipt was refused: %v", failure)
		}
	}
	for i, source := range cases {
		for j, destination := range cases {
			if i == j {
				continue
			}
			t.Run(source.profile+"_to_"+destination.profile, func(t *testing.T) {
				foreign := requestWithInstructionContext(requests[j], map[string]any{"context": receipts[i]})
				failure := s.requireInstructionContext(foreign, "hub", "code_mode")
				if failure["executed"] != false || firstString(mapValue(failure["error"]), "code") != "instructions_required" ||
					failure["instructions"] != destination.want {
					t.Fatalf("cross-profile receipt accepted or wrong rules delivered: %v", failure)
				}
			})
		}
	}

	// Editing a shared set invalidates exactly its users, not other profiles.
	if err := s.updateInstructionSets(func(sets map[string]InstructionSet) error {
		set := sets["rules-a"]
		set.Content = "Updated rules for profile A."
		set.Version = instructionSetVersion(set.Content)
		sets[set.ID] = set
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i, tc := range cases {
		withContext := requestWithInstructionContext(requests[i], map[string]any{"context": receipts[i]})
		failure := s.requireInstructionContext(withContext, "hub", "code_mode")
		if tc.set == "rules-a" {
			if failure["executed"] != false || failure["instructions"] != "Updated rules for profile A." {
				t.Fatalf("profile %s kept outdated shared rules: %v", tc.profile, failure)
			}
		} else if failure != nil {
			t.Fatalf("unrelated profile %s was invalidated: %v", tc.profile, failure)
		}
	}
}

func TestInstructionContextMissingBoundSetDoesNotUseDefault(t *testing.T) {
	s, caller, _ := instructionFixture(t)
	caller = requestWithAccessProfile(caller, AccessProfile{
		ID: "missing-rules-profile", InstructionSetID: "unavailable-rules", Version: 1,
		AccessMode: accessModeFull, AllowedTargets: []string{"*"}, AllowedTools: []string{"*"},
	})
	failure := s.requireInstructionContext(caller, "hub", "code_mode")
	if failure["executed"] != false || firstString(mapValue(failure["error"]), "code") != "instructions_unavailable" ||
		failure["context"] != nil || failure["instructions"] != nil {
		t.Fatalf("missing named rules silently became default rules: %v", failure)
	}
}

func TestInstructionContextRejectsDifferentSigningAuthority(t *testing.T) {
	issuer, caller, _ := instructionFixture(t)
	other, otherCaller, _ := instructionFixture(t)
	issuer.cfg.OAuthClientSecret = "test-only-issuer-authority"
	other.cfg.OAuthClientSecret = "test-only-independent-authority"
	issued := instructionDiscovery(t, issuer, caller)
	foreign := requestWithInstructionContext(otherCaller, map[string]any{"context": issued["context"]})
	failure := other.requireInstructionContext(foreign, "hub", "code_mode")
	if failure["executed"] != false || firstString(mapValue(failure["error"]), "code") != "instructions_required" {
		t.Fatalf("an independent signing authority accepted a foreign context: %v", failure)
	}
	own := instructionDiscovery(t, other, otherCaller)
	valid := requestWithInstructionContext(otherCaller, map[string]any{"context": own["context"]})
	if failure := other.requireInstructionContext(valid, "hub", "code_mode"); failure != nil {
		t.Fatalf("local signing authority could not validate its own receipt: %v", failure)
	}
}
