package hub

import (
	"strings"
	"testing"
)

func TestShellExecTimeoutSchemaExplainsSeconds(t *testing.T) {
	for _, tool := range shellTools() {
		if tool["name"] != "shell_exec" {
			continue
		}
		timeout := mapValue(mapValue(mapValue(tool["inputSchema"])["properties"])["timeout"])
		if !strings.Contains(firstString(timeout, "description"), "seconds") {
			t.Fatalf("timeout units missing: %v", timeout)
		}
		return
	}
	t.Fatal("shell_exec schema missing")
}
