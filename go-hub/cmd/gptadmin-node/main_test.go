package main

import (
	"github.com/megamen32/gptadmin/go-hub/internal/hub"
	shell "github.com/megamen32/gptadmin/go-shellmcp/runtime"
	"path/filepath"
	"testing"
)

func TestNodeExecutorDefaultsUsePersistentChildRegistry(t *testing.T) {
	dir := t.TempDir()
	cfg := hub.Config{ConfigDir: dir}
	got := applyExecutorDefaults(cfg, shell.Config{})
	if got.MCPConfig != filepath.Join(dir, "shellmcp-mcp.json") {
		t.Fatalf("child registry must default to node config directory; got %q", got.MCPConfig)
	}
}

func TestNodeExecutorDefaultsKeepExplicitChildRegistry(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "no-legacy-mcp.json")
	got := applyExecutorDefaults(hub.Config{ConfigDir: dir}, shell.Config{MCPConfig: explicit})
	if got.MCPConfig != explicit {
		t.Fatalf("explicit registry was replaced: %q", got.MCPConfig)
	}
}
