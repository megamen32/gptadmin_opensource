package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadChildrenReadsEnabledStdioRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	body := "{\"mcpServers\":{\n  \"pty\":{\"command\":\"python3\",\"args\":[\"child.py\"],\"env\":{\"CHILD\":\"1\"},\"user\":\"admin\"},\n  \"disabled\":{\"command\":\"nope\",\"enabled\":false}\n}}"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	children, err := loadChildren(path)
	if err != nil {
		t.Fatalf("loadChildren: %v", err)
	}
	child, ok := children["pty"]
	if !ok || child.Command != "python3" || len(child.Args) != 1 || child.Env["CHILD"] != "1" || child.User != "admin" {
		t.Fatalf("unexpected children: %#v", children)
	}
	if _, ok := children["disabled"]; ok {
		t.Fatal("disabled child was exposed")
	}
}

func TestLoadChildrenSkipsEnabledRemoteRegistryEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte("{\"mcpServers\":{\"remote\":{\"url\":\"https://example.test/mcp\"}}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	children, err := loadChildren(path)
	if err != nil {
		t.Fatalf("loadChildren: %v", err)
	}
	if len(children) != 0 {
		t.Fatalf("remote child was exposed: %#v", children)
	}
}

func TestIdleTTLFromEnvSupportsFiveHourDefaultAndDurationOverride(t *testing.T) {
	t.Setenv("GPTADMIN_MCP_BRIDGE_IDLE_TTL", "")
	t.Setenv("MCP_WRAPPER_IDLE_MINUTES", "")
	if got := idleTTLFromEnv(); got != defaultIdle {
		t.Fatalf("default idle TTL=%s, want %s", got, defaultIdle)
	}
	t.Setenv("GPTADMIN_MCP_BRIDGE_IDLE_TTL", "5h")
	if got := idleTTLFromEnv(); got != 5*time.Hour {
		t.Fatalf("duration idle TTL=%s, want 5h", got)
	}
}
