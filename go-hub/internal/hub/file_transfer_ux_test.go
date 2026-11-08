package hub

import "testing"

func TestFileTransferReadActionCoversMetadataReads(t *testing.T) {
	for _, action := range []string{"download_begin", "download_chunk", "download_abort", "stat", "list"} {
		if !fileTransferReadAction(action) {
			t.Fatalf("%s must classify as read", action)
		}
	}
	for _, action := range []string{"upload_begin", "upload_replace", "upload_chunk", "upload_status", "upload_commit", "upload_abort", ""} {
		if fileTransferReadAction(action) {
			t.Fatalf("%s must classify as write", action)
		}
	}
}

func TestV2TransferActionClassification(t *testing.T) {
	for _, action := range []string{"stat", "list", "upload_replace"} {
		if !v2TransferAction(action) {
			t.Fatalf("%s must be a v2 action", action)
		}
	}
	for _, action := range []string{"upload_begin", "upload_chunk", "download_begin", "download_chunk", ""} {
		if v2TransferAction(action) {
			t.Fatalf("%s must not be a v2 action", action)
		}
	}
}

func v2Definition(t *testing.T) map[string]any {
	t.Helper()
	for _, tool := range fileToolsForAgent(Agent{AgentID: "file:host", Meta: map[string]any{"file_transfer_version": 2}}) {
		if tool["name"] == "file_transfer" {
			return tool
		}
	}
	t.Fatal("v2 file_transfer tool missing")
	return nil
}

func TestFileToolsForAgentVersions(t *testing.T) {
	none := fileToolsForAgent(Agent{AgentID: "file:host"})
	for _, tool := range none {
		if tool["name"] == "file_transfer" {
			t.Fatal("version 0 must not advertise file_transfer")
		}
	}
	v1 := fileToolsForAgent(Agent{AgentID: "file:host", Meta: map[string]any{"file_transfer_version": 1}})
	found := 0
	for _, tool := range v1 {
		if tool["name"] == "file_transfer" {
			found++
			enum := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string)
			for _, action := range enum {
				if action == "stat" || action == "list" || action == "upload_replace" {
					t.Fatal("v1 must not advertise v2 actions", action)
				}
			}
		}
	}
	if found != 1 {
		t.Fatal("v1 must advertise exactly one file_transfer")
	}
	tool := v2Definition(t)
	schema := tool["inputSchema"].(map[string]any)
	props := schema["properties"].(map[string]any)
	enum := props["action"].(map[string]any)["enum"].([]string)
	want := map[string]bool{"stat": false, "list": false, "upload_replace": false, "upload_begin": false, "download_begin": false}
	for _, action := range enum {
		if _, ok := want[action]; ok {
			want[action] = true
		}
	}
	for action, seen := range want {
		if !seen {
			t.Fatal("v2 enum missing", action)
		}
	}
	if pattern, ok := props["pattern"].(map[string]any); !ok || pattern["maxLength"] != 256 {
		t.Fatal("v2 pattern property missing")
	}
	if expected, ok := props["expected_sha256"].(map[string]any); !ok || expected["pattern"] != "^[0-9a-f]{64}$" {
		t.Fatal("v2 expected_sha256 property missing")
	}
	if hash, ok := props["hash"].(map[string]any); !ok || hash["type"] != "boolean" {
		t.Fatal("v2 hash property missing")
	}
	req := schema["required"].([]string)
	if len(req) != 2 || req[0] != "action" || req[1] != "path" {
		t.Fatal("v2 required must stay action+path")
	}
}
