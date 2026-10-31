package hub

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// browserMCPAlias is a *catalogue route*, not another browser process or credential.
// The owner is the physical machine with Chrome; upstream is the GPTAdmin node
// which already owns the pinned Vibe MCP transport.
type browserMCPAlias struct {
	Owner       string `json:"owner"`
	Ref         string `json:"ref"`
	Name        string `json:"name"`
	Upstream    string `json:"upstream"`
	UpstreamRef string `json:"upstream_ref"`
	HideBacking bool   `json:"hide_backing"`
}

type browserMCPAliasConfig struct {
	Aliases []browserMCPAlias `json:"aliases"`
}

func isLogicalBrowserAgent(a Agent) bool {
	return a.Kind == "child_mcp" && firstString(a.Meta, "logical_owner_server_id") != "" &&
		truthyAny(a.Meta["fixed_session"])
}

// Caller holds s.mu, like publicAgentsLocked. Config contains only routes,
// never credentials. It can be updated atomically without restarting Chrome.
func (s *Server) browserMCPAliasesLocked() []Agent {
	if s.cfg.ConfigDir == "" {
		return nil
	}
	path := filepath.Join(s.cfg.ConfigDir, "browser-mcp-aliases.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		log.Printf("browser aliases read: %v", err)
		return nil
	}
	var config browserMCPAliasConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		log.Printf("browser aliases parse: %v", err)
		return nil
	}
	out := make([]Agent, 0, len(config.Aliases))
	seen := make(map[string]bool)
	for i, rule := range config.Aliases {
		if i >= 32 {
			break
		}
		rule.Owner = strings.TrimSpace(rule.Owner)
		rule.Ref = strings.TrimSpace(rule.Ref)
		rule.Upstream = strings.TrimSpace(rule.Upstream)
		rule.UpstreamRef = strings.TrimSpace(rule.UpstreamRef)
		if !strings.HasPrefix(rule.Owner, "shell:") ||
			!strings.HasPrefix(rule.Upstream, "shell:") || rule.Ref != "Browser" ||
			rule.UpstreamRef == "" || rule.Owner == rule.Upstream {
			continue
		}
		id := "mcp:" + rule.Owner + ":" + rule.Ref
		if seen[id] {
			continue
		}
		seen[id] = true
		physical := s.agents[rule.Upstream]
		owner := s.agents[rule.Owner]
		if owner == nil || physical == nil {
			continue
		}
		var descriptor map[string]any
		for _, item := range sliceValue(physical.Meta["mcp_agents"]) {
			child := mapValue(item)
			if firstString(child, "ref") == rule.UpstreamRef && truthyAny(child["enabled"]) {
				descriptor = child
				break
			}
		}
		// Node-owned child MCPs can be reachable through GPTAdmin's node
		// transport even when an older standalone ShellMCP heartbeat has not
		// advertised them. The root-owned route file pins the child ref;
		// mcp_tools/mcp_call independently validate it on the upstream Node.
		if descriptor == nil {
			descriptor = map[string]any{"transport": "streamable-http"}
		}
		status := owner.Status
		health := mapValue(descriptor["health"])
		protocol := mapValue(health["protocol"])
		if firstString(protocol, "state") == "failed" {
			status = "failed"
		}
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			name = "Browser on " + strings.TrimPrefix(rule.Owner, "shell:")
		}
		out = append(out, Agent{
			AgentID: id, Name: name, Kind: "child_mcp",
			Transport: firstString(descriptor, "transport"), Status: status,
			LastSeen:     owner.LastSeen,
			Capabilities: []string{"tools/list", "tools/call"},
			Meta: map[string]any{
				"parent_server_id":        rule.Upstream,
				"child_ref":               rule.UpstreamRef,
				"enabled":                 true,
				"logical_owner_server_id": rule.Owner,
				"fixed_session":           true,
				"hide_backing":            rule.HideBacking,
				"owner_profile_id":        firstString(descriptor, "owner_profile_id"),
			},
		})
	}
	return out
}

func backingHiddenByBrowserAliases(child Agent, aliases []Agent) bool {
	for _, alias := range aliases {
		if !truthyAny(alias.Meta["hide_backing"]) {
			continue
		}
		if firstString(alias.Meta, "parent_server_id") == firstString(child.Meta, "parent_server_id") &&
			firstString(alias.Meta, "child_ref") == firstString(child.Meta, "child_ref") {
			return true
		}
	}
	return false
}

func hideBrowserRetargetTool(tools []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if firstString(tool, "name") != "set_remote" {
			out = append(out, tool)
		}
	}
	return out
}
