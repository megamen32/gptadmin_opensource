package hub

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestHandoverStatusRuntimeAndLegacyScope(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	result, status := s.callHubTool("handover_status", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	state, ok := result["state"].(hubHandoverState)
	if !ok || state.Status == "" {
		t.Fatalf("legacy state missing: %#v", result["state"])
	}
	if result["state_scope"] != "legacy_local_handover" || result["state_path"] != "/run/gptadmin-handover.json" {
		t.Fatalf("local scope missing: %#v", result)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	info := result["runtime"].(map[string]any)
	if info["executable"] != executable || info["build_version"] != BuildVersion || info["git_commit"] != GitCommit {
		t.Fatalf("runtime does not describe this process: %#v", info)
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatalf("status is not JSON serializable: %v", err)
	}
}

func TestHandoverStatusSeparatesInspectionsFromActions(t *testing.T) {
	result := hubHandoverStatus()
	hints := result["hints"].(map[string]any)
	update := hints["node_update"].(map[string]any)
	commands := update["read_only_commands"].([]string)
	want := []string{"sudo gptadmin node status --name primary", "sudo gptadmin node autoupdate status"}
	if len(commands) != len(want) {
		t.Fatalf("inspection commands = %#v", commands)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Fatalf("inspection command %d = %q", i, commands[i])
		}
	}
	if update["manual_update_action"] != "sudo gptadmin node autoupdate run" || update["manual_update_note"] == "" || update["hold_note"] == "" {
		t.Fatalf("mutation and unknown-outcome guidance missing: %#v", update)
	}
	haos := hints["haos_failover"].(map[string]any)
	if !strings.Contains(haos["note"].(string), "отдельный promotion controller") || !strings.Contains(haos["inspect"].(string), "failover.maintenance_until") {
		t.Fatalf("HAOS ownership guidance missing: %#v", haos)
	}
	if !strings.Contains(hints["local_handover"].(string), "отсутствии файла") {
		t.Fatal("missing-file idle caveat missing")
	}
}

func TestHandoverStatusToolDescriptionExplainsScope(t *testing.T) {
	for _, tool := range hubTools() {
		if tool["name"] != "handover_status" {
			continue
		}
		description := tool["description"].(string)
		for _, text := range []string{"legacy local", "executable/build/SHA", "read-only", "HAOS", "does not run updates"} {
			if !strings.Contains(description, text) {
				t.Fatalf("description missing %q: %s", text, description)
			}
		}
		return
	}
	t.Fatal("handover_status tool missing")
}
