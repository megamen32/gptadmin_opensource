package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCapabilitiesAndHeartbeatBlackboxExposeLazyChildHealth(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "child.sh")
	body := `#!/bin/sh
n=0
while IFS= read -r line; do
 case "$line" in *notifications/initialized*) continue ;; esac
 n=$((n+1))
 if [ "$n" -eq 1 ]; then
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}'
 else
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"ping"}]}}'
 fi
done
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}

	var heartbeat map[string]any
	hubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/heartbeat" {
			t.Fatalf("heartbeat path=%s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&heartbeat); err != nil {
			t.Fatalf("decode heartbeat: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer hubServer.Close()

	s := New(Config{
		Token:     "shell-token",
		HubURL:    hubServer.URL,
		MCPConfig: filepath.Join(dir, "mcp.json"),
		SpillDir:  t.TempDir(),
	})
	defer s.Close()
	if _, err := s.mcpManage(map[string]any{
		"action": "upsert",
		"config": map[string]any{"ref": "BrowserClaw", "transport": "stdio", "command": script, "enabled": true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.mcpManage(map[string]any{
		"action": "upsert",
		"config": map[string]any{"ref": "DisabledMCP", "transport": "stdio", "command": script, "enabled": false},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.callMCPTool(context.Background(), "mcp_tools", map[string]any{"ref": "BrowserClaw"}); err != nil {
		t.Fatal(err)
	}
	s.refreshMCPHealth(context.Background())

	server := httptest.NewServer(s.Handler())
	defer server.Close()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer shell-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var capabilities map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&capabilities); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("capabilities status=%d body=%v", resp.StatusCode, capabilities)
	}
	agents, _ := capabilities["mcp_agents"].([]any)
	if len(agents) != 2 {
		t.Fatalf("capabilities mcp_agents=%v", capabilities["mcp_agents"])
	}
	var descriptor map[string]any
	var disabled map[string]any
	for _, raw := range agents {
		candidate, _ := raw.(map[string]any)
		if candidate["ref"] == "BrowserClaw" {
			descriptor = candidate
		}
		if candidate["ref"] == "DisabledMCP" {
			disabled = candidate
		}
	}
	if descriptor == nil || disabled == nil {
		t.Fatalf("capabilities child descriptors=%v", capabilities["mcp_agents"])
	}
	health, _ := descriptor["health"].(map[string]any)
	protocol, _ := health["protocol"].(map[string]any)
	if protocol["state"] != "ready" || protocol["tools_count"] != float64(1) {
		t.Fatalf("capabilities child health=%v", descriptor)
	}
	disabledHealth, _ := disabled["health"].(map[string]any)
	disabledProtocol, _ := disabledHealth["protocol"].(map[string]any)
	if disabledProtocol["state"] != "disabled" {
		t.Fatalf("disabled child health=%v", disabled)
	}

	s.sendHeartbeat(context.Background())
	beatAgents, _ := heartbeat["mcp_agents"].([]any)
	if len(beatAgents) != 2 {
		t.Fatalf("heartbeat mcp_agents=%v", heartbeat["mcp_agents"])
	}
	var beatDescriptor map[string]any
	for _, raw := range beatAgents {
		candidate, _ := raw.(map[string]any)
		if candidate["ref"] == "BrowserClaw" {
			beatDescriptor = candidate
		}
	}
	if beatDescriptor == nil {
		t.Fatalf("heartbeat BrowserClaw descriptor=%v", heartbeat["mcp_agents"])
	}
	beatHealth, _ := beatDescriptor["health"].(map[string]any)
	beatProtocol, _ := beatHealth["protocol"].(map[string]any)
	if beatProtocol["state"] != "ready" {
		t.Fatalf("heartbeat child health=%v", beatDescriptor)
	}
}
