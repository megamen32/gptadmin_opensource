package hub

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// grepMeshTopologyNode is deliberately a control-plane projection. It carries
// only the fields a GrepMesh peer needs to build topology; it does not expose
// the registered agent's relay target, tool schema, credentials, or arguments.
func grepMeshTopologyNode(agent Agent) (map[string]any, bool) {
	if !isGrepMeshAgent(agent) {
		return nil, false
	}

	hostID := firstNonEmptyMeta(agent.Meta, "host_id", "node_id")
	if hostID == "" {
		hostID = agent.AgentID
	}
	if hostID == "" {
		return nil, false
	}

	localURL := firstNonEmptyMeta(agent.Meta, "local_url", "local_endpoint")
	peerURL := firstNonEmptyMeta(agent.Meta, "peer_url", "advertise_url", "endpoint")
	if localURL == "" && peerURL == "" {
		return nil, false
	}

	node := map[string]any{
		"host_id":          hostID,
		"status":           agent.Status,
		"version":          firstNonEmptyMeta(agent.Meta, "version", "build_version"),
		"protocol_version": firstNonEmptyMeta(agent.Meta, "protocol_version"),
		"schema_version":   firstNonEmptyMeta(agent.Meta, "schema_version"),
		"local_url":        localURL,
		"peer_url":         peerURL,
		"capabilities":     append([]string(nil), agent.Capabilities...),
		"roots":            stringSlice(agent.Meta["roots"]),
		"last_seen":        agent.LastSeen,
	}
	if generation, ok := optionalInt(agent.Meta["generation"]); ok {
		node["generation"] = generation
	}
	if expiresAt := firstNonEmptyMeta(agent.Meta, "expires_at", "topology_expires_at"); expiresAt != "" {
		node["expires_at"] = expiresAt
	}
	return node, true
}

func isGrepMeshAgent(agent Agent) bool {
	if strings.EqualFold(agent.Kind, "grepmesh") {
		return true
	}
	return strings.EqualFold(firstNonEmptyMeta(agent.Meta, "service"), "grepmesh-mcp")
}

func firstNonEmptyMeta(meta map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := firstString(meta, key); value != "" {
			return value
		}
	}
	return ""
}

func optionalInt(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), v == float64(int64(v))
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// mcpRelayGrepMeshTopology is intentionally read-only and control-plane-only.
// Search/read calls must use the advertised peer_url directly, never this Hub.
func (s *Server) mcpRelayGrepMeshTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}

	s.mu.Lock()
	nodes := make([]map[string]any, 0)
	for _, agent := range s.agents {
		if agent == nil {
			continue
		}
		if node, ok := grepMeshTopologyNode(*agent); ok {
			nodes = append(nodes, node)
		}
	}
	s.mu.Unlock()

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i]["host_id"].(string) < nodes[j]["host_id"].(string)
	})
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}
