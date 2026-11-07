package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestContentSpillReceiptCallsAndACL(t *testing.T) {
	s, owner := codeModeFixture(t)
	defer s.Close()
	spill, status := codeModeRun(t, s, owner, map[string]any{"code": `return 'HINTCONTRACTMARKER '.repeat(12000);`, "max_output_bytes": 4096})
	if status != http.StatusOK {
		t.Fatalf("spill failed: %d %#v", status, spill)
	}
	receipt := mapValue(spill["result"])
	source := firstString(receipt, "source")
	if source == "" {
		t.Fatalf("missing source: %#v", receipt)
	}
	other := requestWithAuthClaims(owner, map[string]any{"sub": "different-principal", "client_id": "different-principal", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull})
	for _, tc := range []struct{ key, call string }{{"read", "mcp.content.read"}, {"search", "mcp.content.search"}, {"index", "mcp.content.indexSource"}} {
		t.Run(tc.key, func(t *testing.T) {
			hint := mapValue(receipt[tc.key])
			if firstString(hint, "call") != tc.call {
				t.Fatalf("wrong call: %#v", hint)
			}
			args := cloneMap(mapValue(hint["args"]))
			if tc.key == "search" {
				args["query"] = "HINTCONTRACTMARKER"
			}
			encoded, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			code := fmt.Sprintf("return await %s(%s);", tc.call, encoded)
			got, status := codeModeRun(t, s, owner, map[string]any{"code": code})
			if status != http.StatusOK || firstString(got, "status") != "completed" {
				t.Errorf("hint failed: %d %#v", status, got)
			} else {
				result := mapValue(got["result"])
				if tc.key == "read" && !strings.Contains(firstString(result, "text"), "HINTCONTRACTMARKER") {
					t.Errorf("read lost content: %#v", result)
				}
				if tc.key == "search" && intFromAny(result["count"]) == 0 {
					t.Errorf("search missed content: %#v", result)
				}
				if tc.key == "index" && !truthy(result["indexed"]) {
					t.Errorf("indexSource did not index source: %#v", result)
				}
			}
			denied, status := codeModeRun(t, s, other, map[string]any{"code": code})
			if status == http.StatusOK || firstString(mapValue(denied["error"]), "code") != "content_unavailable" {
				t.Errorf("hint crossed principal boundary: %d %#v", status, denied)
			}
		})
	}
	quoted, _ := json.Marshal(source)
	positional := fmt.Sprintf(`const i=await mcp.content.indexSource(%s); const hits=await mcp.content.search('HINTCONTRACTMARKER',{source:%s}); const body=await mcp.content.read(%s,1); return {count:hits.count,text:body.text};`, quoted, quoted, quoted)
	got, status := codeModeRun(t, s, owner, map[string]any{"code": positional})
	if status != http.StatusOK || intFromAny(mapValue(got["result"])["count"]) == 0 || !strings.Contains(firstString(mapValue(got["result"]), "text"), "HINTCONTRACTMARKER") {
		t.Errorf("positional API regressed: %d %#v", status, got)
	}
}

func TestContentHintObjectRechecksOriginACL(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	target := "mcp:shell:content-parent:personal-log"
	s.mu.Lock()
	s.agents["shell:content-parent"] = &Agent{AgentID: "shell:content-parent", Name: "fixture", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true, "mcp_agents": []any{map[string]any{"ref": "personal-log", "name": "personal log", "enabled": true, "owner_profile_id": "owner-profile"}}}}
	s.mu.Unlock()
	store, err := s.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.IndexFormatted(operationPrincipal(r), "private-source", target+"\x00read_log", "content-text", "PRIVATE_HINT_MARKER")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(source.ID)
	for _, expression := range []string{
		fmt.Sprintf("mcp.content.read({source:%s,chunk_id:1})", quoted),
		fmt.Sprintf("mcp.content.search({source:%s,query:'PRIVATE_HINT_MARKER'})", quoted),
		fmt.Sprintf("mcp.content.indexSource({source:%s})", quoted),
	} {
		got, status := codeModeRun(t, s, r, map[string]any{"code": "return await " + expression + ";"})
		if status == http.StatusOK || firstString(mapValue(got["error"]), "code") != "content_unavailable" {
			t.Errorf("origin ACL bypass for %s: %d %#v", expression, status, got)
		}
	}
}
