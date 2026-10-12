package hub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const managedNodeProtocol = "managed-http-v1"
const managedNodeProtocolHeader = "X-Gptadmin-Node-Connection-Protocol"
const managedNodePendingLimit = 8

func managedConnectionPath(path string) bool {
	if len(path) > 512 || !strings.HasPrefix(path, "/connection/") || strings.ContainsAny(path, "%?#\\") {
		return false
	}
	slug, tail, ok := parseAgentPath("/server/" + strings.TrimPrefix(path, "/connection/"))
	return ok && slug != "" && tail == "mcp" && !strings.Contains(slug, "/") && slug != "." && slug != ".."
}

// The destination owns the downstream session and its authenticated upstream
// partition. The gateway retains no session map, credentials or mutable data.
// Old peers cannot accept this operation merely by exposing an online card.
func (s *Server) forwardManagedConnection(w http.ResponseWriter, r *http.Request, agent Agent, body []byte) bool {
	parent := firstString(agent.Meta, "parent_server_id")
	if parent == "" || agent.Transport != "streamable-http" && agent.Transport != "sse" {
		return false
	}
	s.mu.Lock()
	local := s.localExecutors[parent] != nil
	s.mu.Unlock()
	if local {
		return false
	}
	conn := s.nodeConns.get(parent)
	if conn == nil {
		return false
	}
	fail := func(status int, detail string) { writeJSON(w, status, map[string]any{"detail": detail}) }
	if r.Header.Get(nodePeerHopHeader) != "" {
		fail(http.StatusLoopDetected, "managed connection destination does not own this child")
		return true
	}
	if !conn.managedHTTP {
		fail(http.StatusServiceUnavailable, "node peer must support managed HTTP session forwarding")
		return true
	}
	if !managedConnectionPath(r.URL.Path) || r.Method != http.MethodPost && r.Method != http.MethodDelete {
		fail(http.StatusBadRequest, "invalid managed connection operation")
		return true
	}
	if connectionPrincipal(r) == "" || r.Header.Get("Authorization") == "" || !profileAllowsTarget(r, agent.AgentID) {
		fail(http.StatusForbidden, "managed connection requires its verified owner")
		return true
	}
	seed := make([]byte, 12)
	if _, err := rand.Read(seed); err != nil {
		fail(http.StatusServiceUnavailable, "cannot allocate managed connection request")
		return true
	}
	frame := nodeConnFrame{Op: "call", ID: hex.EncodeToString(seed), Endpoint: r.URL.Path, Method: r.Method, Auth: r.Header.Get("Authorization"), Body: body, Headers: map[string]string{}}
	for _, name := range []string{"Accept", "MCP-Protocol-Version", "Mcp-Method", "Mcp-Name", "Mcp-Session-Id"} {
		if value := r.Header.Get(name); value != "" {
			frame.Headers[name] = value
		}
	}
	wait := make(chan nodeConnResult, 1)
	conn.pendingMu.Lock()
	if len(conn.pending) >= managedNodePendingLimit {
		conn.pendingMu.Unlock()
		fail(http.StatusServiceUnavailable, "node managed connection capacity exhausted")
		return true
	}
	conn.pending[frame.ID] = wait
	conn.pendingMu.Unlock()
	defer func() { conn.pendingMu.Lock(); delete(conn.pending, frame.ID); conn.pendingMu.Unlock() }()
	releaseAuthAdmission(r)
	// A write failure can be partial. Never fall back to another origin or
	// redispatch initialize/tool calls after any write attempt.
	if err := conn.write(frame); err != nil {
		fail(http.StatusBadGateway, "managed peer delivery was not confirmed; inspect the owner before retrying")
		return true
	}
	timeout := s.cfg.DefaultTimeout
	if timeout < 20*time.Second {
		timeout = 20 * time.Second
	}
	timer := time.NewTimer(timeout + 10*time.Second)
	defer timer.Stop()
	select {
	case result := <-wait:
		if result.status < 200 || result.status > 599 || result.status >= 300 && result.status < 400 {
			fail(http.StatusBadGateway, "invalid managed peer response")
			return true
		}
		if result.status != http.StatusNoContent {
			var object map[string]json.RawMessage
			if json.Unmarshal(result.body, &object) != nil || object == nil {
				fail(http.StatusBadGateway, "managed peer returned an invalid response; inspect the owner")
				return true
			}
		} else if len(result.body) != 0 {
			fail(http.StatusBadGateway, "invalid managed no-content response")
			return true
		}
		for _, name := range []string{"WWW-Authenticate", "MCP-Protocol-Version", "Mcp-Session-Id"} {
			if value := result.headers.Get(name); value != "" {
				w.Header().Set(name, value)
			}
		}
		if result.status != http.StatusNoContent {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(result.status)
		if result.status != http.StatusNoContent {
			_, _ = w.Write(result.body)
		}
	case <-timer.C:
		fail(http.StatusGatewayTimeout, "managed peer response deadline; inspect the owner before retrying")
	case <-r.Context().Done():
	}
	return true
}
