package hub

import (
	"strings"
	"testing"
	"time"
)

func TestHubDoesNotDispatchStdinToOldShellMCP(t *testing.T) {
	server := New(Config{ConfigDir: t.TempDir(), DefaultTimeout: 2 * time.Second})
	defer server.Close()
	server.mu.Lock()
	server.agents["shell:old"] = &Agent{AgentID: "shell:old", Kind: "shell", Status: "online", Meta: map[string]any{"build_version": 1073, "approved": true}}
	server.mu.Unlock()
	result := server.callShellTool("shell:old", "shell_exec", map[string]any{"argv": []any{"python3", "-"}, "stdin": "print(\"hello\")\n"}, true, time.Second)
	if result["status"] != "failed" || !strings.Contains(firstString(result, "error"), "1074") {
		t.Fatalf("old ShellMCP accepted unsupported stdin: %#v", result)
	}
	server.mu.Lock()
	jobs := len(server.shellJobs)
	server.mu.Unlock()
	if jobs != 0 {
		t.Fatalf("unsupported stdin queued a task: %d", jobs)
	}
}
