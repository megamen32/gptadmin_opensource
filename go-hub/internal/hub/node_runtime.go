package hub

import (
	"fmt"
	"strings"
)

// RegisterLocalExecutor trusts only the identity supplied by the node's own
// in-process executor. It is deliberately not an HTTP enrollment endpoint.
func (s *Server) RegisterLocalExecutor(name string, identity map[string]string, token string) error {
	if len(token) < 32 {
		return fmt.Errorf("local executor credential must contain at least 32 bytes")
	}
	if strings.TrimSpace(name) == "" || name != canonicalShellQueueName(name) || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid local executor name")
	}
	for _, key := range []string{"server_id", "public_key", "fingerprint"} {
		if identity[key] == "" {
			return fmt.Errorf("missing local identity %s", key)
		}
	}
	id := "shell:" + name
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.agents[id]
	if previous != nil {
		for key, value := range identity {
			if old, ok := previous.Meta[key].(string); ok && old != "" && old != value {
				return fmt.Errorf("local executor identity differs for %s", key)
			}
		}
	}
	meta := map[string]any{"approved": true, "local_executor": true}
	for key, value := range identity {
		meta[key] = value
	}
	agent := &Agent{AgentID: id, Name: "Shell: " + name, Kind: "virtual_shell", Transport: "long_poll", Status: "online", LastSeen: nowFloat(), Capabilities: []string{"shell", "system", "tasks", "logs"}, Meta: meta}
	s.prepareAgentLocked(agent)
	s.agents[id] = agent
	if err := s.saveRegistryStateLocked(); err != nil {
		if previous == nil {
			delete(s.agents, id)
		} else {
			s.agents[id] = previous
		}
		return err
	}
	if s.localExecutors == nil {
		s.localExecutors = map[string]map[string]string{}
	}
	pinned := map[string]string{}
	for key, value := range identity {
		pinned[key] = value
	}
	s.localExecutors[id] = pinned
	if s.localExecutorTokens == nil {
		s.localExecutorTokens = map[string]string{}
	}
	s.localExecutorTokens[id] = token
	return nil
}

func (s *Server) localExecutorMatchesLocked(id string, identity map[string]string) bool {
	for key, value := range s.localExecutors[id] {
		if identity[key] != value {
			return false
		}
	}
	return true
}
