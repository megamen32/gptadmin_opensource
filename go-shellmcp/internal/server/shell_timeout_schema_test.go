package server

import (
	"strings"
	"testing"
)

func TestShellExecTimeoutSchemaExplainsSeconds(t *testing.T) {
	s := &Server{}
	for _, tool := range s.mcpTools() {
		if tool["name"] != "shell_exec" {
			continue
		}
		schema := tool["inputSchema"].(map[string]any)
		timeout := schema["properties"].(map[string]any)["timeout"].(map[string]any)
		description, _ := timeout["description"].(string)
		if !strings.Contains(description, "seconds") {
			t.Fatalf("timeout units missing: %v", timeout)
		}
		return
	}
	t.Fatal("shell_exec schema missing")
}
