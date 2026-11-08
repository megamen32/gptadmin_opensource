package hub

import (
	"net/http"
	"strings"
)

// resourceAgentsForRequest enumerates the current registry without discovering
// child schemas. Resource data obeys the same target and personal-connection
// boundaries as execution, including when a client already knows a resource URI.
func (s *Server) resourceAgentsForRequest(r *http.Request) []Agent {
	s.mu.Lock()
	agents := s.publicAgentsLocked(r)
	s.mu.Unlock()
	visible := make([]Agent, 0, len(agents))
	for _, agent := range agents {
		if profileAllowsTarget(r, agent.AgentID) && agentOwnerConnectionAllowed(r, agent) {
			visible = append(visible, agent)
		}
	}
	return visible
}

func (s *Server) resourceServersForRequest(r *http.Request) []map[string]any {
	agents := s.resourceAgentsForRequest(r)
	servers := make([]map[string]any, 0, len(agents))
	for _, agent := range agents {
		server := agentAsServer(agent)
		if hostname := firstString(agent.Meta, "hostname"); hostname != "" && strings.HasPrefix(agent.AgentID, "shell:") {
			server["name"] = hostname
		}
		servers = append(servers, compactServer(server))
	}
	return servers
}
