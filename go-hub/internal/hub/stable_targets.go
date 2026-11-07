package hub

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Short IDs are derived from the existing enrolled identity, never the route
// name. Legacy routes without identity metadata stay canonical until enrolled.
// ACLs, jobs and approvals continue to use the original canonical addresses.
func shortTargetFromAgents(target string, agents map[string]Agent) string {
	prefix, parent, ref := "", target, ""
	switch {
	case strings.HasPrefix(target, "shell:"):
		prefix = "h"
	case strings.HasPrefix(target, "file:"):
		prefix = "f"
		parent = "shell:" + strings.TrimPrefix(target, "file:")
	case strings.HasPrefix(target, "control:") && target != "control:hub":
		prefix = "k"
		parent = "shell:" + strings.TrimPrefix(target, "control:")
	case strings.HasPrefix(target, "mcp:"):
		child, ok := agents[target]
		if !ok {
			return target
		}
		parent = firstString(child.Meta, "parent_server_id")
		ref = firstString(child.Meta, "child_ref")
		if parent == "" || ref == "" {
			return target
		}
		prefix = "c"
	default:
		return target
	}
	host, ok := agents[parent]
	if !ok {
		return target
	}
	serverID, fingerprint := firstString(host.Meta, "server_id"), firstString(host.Meta, "fingerprint")
	if serverID == "" || fingerprint == "" {
		return target
	}
	// JSON tuple supplies unambiguous framing; no new identity or alias store.
	encoded, _ := json.Marshal([]string{"gptadmin-target-v1", prefix, serverID, fingerprint, ref})
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%s%x", prefix, sum[:6])
}

func targetAgentMap(agents []Agent) map[string]Agent {
	out := make(map[string]Agent, len(agents))
	for _, a := range agents {
		out[a.AgentID] = a
	}
	return out
}

func (s *Server) shortTarget(target string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return shortTargetFromAgents(target, targetAgentMap(s.publicAgentsLocked(nil)))
}

func (s *Server) canonicalPublicTarget(target string) (string, error) {
	// Existing canonical addresses cannot collide with the opaque alphabet.
	if strings.Contains(target, ":") || target == "hub" || target == "" {
		return target, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := ""
	agents := targetAgentMap(s.publicAgentsLocked(nil))
	for _, a := range agents {
		if a.AgentID != target && shortTargetFromAgents(a.AgentID, agents) != target {
			continue
		}
		if found != "" && found != a.AgentID {
			return "", fmt.Errorf("ambiguous target %q", target)
		}
		found = a.AgentID
	}
	if found != "" {
		return found, nil
	}
	return target, nil // ordinary target validation reports unknown names
}

func (s *Server) normalizePublicTargetArgs(args map[string]any) error {
	for _, key := range []string{"target", "server_id", "agent_id"} {
		value, ok := args[key].(string)
		if !ok || value == "" {
			continue
		}
		canonical, err := s.canonicalPublicTarget(value)
		if err != nil {
			return err
		}
		args[key] = canonical
	}
	return nil
}

func (s *Server) authorizePublicFacade(r *http.Request, name string, args map[string]any) error {
	if err := s.normalizePublicTargetArgs(args); err != nil {
		return err
	}
	return authorizeFacadeCall(r, name, args)
}

// Only routing descriptors are rewritten; arbitrary tool results, schemas,
// arguments and stored references must never be recursively transformed.
func (s *Server) compactTargetDescriptors(name string, value any) any {
	switch name {
	case "schema", "list_mcp_tools", "listMcpTools", "discover", "list_mcp_servers", "listMcpServers", "search":
	default:
		return value
	}
	s.mu.Lock()
	agents := targetAgentMap(s.publicAgentsLocked(nil))
	s.mu.Unlock()
	shortTarget := func(target string) string { return shortTargetFromAgents(target, agents) }
	out, ok := value.(map[string]any)
	if !ok {
		return value
	}
	switch name {
	case "schema", "list_mcp_tools", "listMcpTools":
		out = cloneMap(out)
		if id := firstString(out, "server_id"); id != "" {
			out["server_id"] = shortTarget(id)
		}
	case "discover", "list_mcp_servers", "listMcpServers":
		out = cloneMap(out)
		rows := []any{}
		for _, v := range outputRows(out["servers"]) {
			row := cloneMap(mapValue(v))
			id := firstString(row, "server_id")
			row["server_id"] = shortTarget(id)
			if strings.HasPrefix(id, "mcp:") {
				parts := strings.Split(id, ":")
				if len(parts) >= 3 {
					row["host"] = parts[len(parts)-2]
				}
			}
			rows = append(rows, row)
		}
		out["servers"] = rows
	case "search":
		out = cloneMap(out)
		rows := []any{}
		for _, v := range outputRows(out["results"]) {
			row := cloneMap(mapValue(v))
			if id := firstString(row, "target"); id != "" {
				row["target"] = shortTarget(id)
			}
			if ref := mapValue(row["schema_ref"]); len(ref) > 0 {
				ref = cloneMap(ref)
				ref["target"] = shortTarget(firstString(ref, "target"))
				row["schema_ref"] = ref
			}
			rows = append(rows, row)
		}
		out["results"] = rows
	}
	return out
}
