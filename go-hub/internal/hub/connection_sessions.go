package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Managed connections are opt-in: the legacy server card/facade keeps its URL.
// The registry is process-local and bounded; reconnect after restart requires
// initialize. No downstream credential or request ID is forwarded upstream.
const connectionSessionLimit = 128
const connectionSessionTTL = 5 * time.Minute

type connectionContextKey struct{}
type connectionScope struct {
	Target   string
	Key      string
	Instance string
}
type connectionSession struct {
	Principal string
	Target    string
	Scope     string
	Mode      string
	Parent    string
	Ref       string
	CloseJob  string
	Closing   bool
	Instance  string
	Expires   time.Time
}

func connectionDigest(values ...any) string {
	data, _ := json.Marshal(values)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func connectionPrincipal(r *http.Request) string {
	if r == nil {
		return ""
	}
	claims, _ := r.Context().Value(authClaimsContextKey{}).(map[string]any)
	if len(claims) == 0 {
		return ""
	}
	profile, _ := AccessProfileFromRequest(r)
	return connectionDigest("principal-v1", claims["iss"], claims["aud"], claims["sub"],
		claims["client_id"], r.Header.Get("Authorization"), profile.ID, profile.Version,
		claims["scope"], claims["access_mode"])
}

func (s *Server) connectionMCPEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Headers", "authorization, content-type, MCP-Protocol-Version, Mcp-Session-Id, Mcp-Method, Mcp-Name")
	w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id, MCP-Protocol-Version")
	w.Header().Set("Access-Control-Allow-Methods", "POST, DELETE, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "POST, DELETE, OPTIONS")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	alias := r.Clone(r.Context())
	u := *r.URL
	u.Path = "/server/" + strings.TrimPrefix(r.URL.Path, "/connection/")
	alias.URL = &u
	slug, tail, ok := parseAgentPath(alias.URL.Path)
	if !ok || tail != "mcp" {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown connection endpoint"})
		return
	}
	agent, ok := s.resolveExposedAgent(slug)
	if !ok || !isChildMCPAgent(agent) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "connection requires a configured child MCP"})
		return
	}
	if !s.mcpAuth(w, r) {
		return
	}
	principal := connectionPrincipal(r)
	if principal == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "managed connections require a verified client credential"})
		return
	}
	if !profileAllowsTarget(r, agent.AgentID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "target is not permitted"})
		return
	}
	if r.Method == http.MethodDelete {
		if s.forwardManagedConnection(w, r, agent, nil) {
			return
		}
		if !managedLocalConnector(w, agent) {
			return
		}
		s.closeConnection(w, r, agent, principal)
		return
	}
	var body map[string]any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON-RPC request"})
		return
	}
	if headerErr := validateMCP20260728RoutingHeaders(r, body); headerErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"jsonrpc": "2.0", "id": body["id"], "error": headerErr})
		return
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON-RPC request"})
		return
	}
	if s.forwardManagedConnection(w, r, agent, encoded) {
		return
	}
	if !managedLocalConnector(w, agent) {
		return
	}
	method := firstString(body, "method")
	var sess connectionSession
	if method == "initialize" {
		releaseAuthAdmission(r)
		id, created, err := s.newConnection(agent, principal)
		if err != nil {
			s.closeExpiredConnections(r)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": err.Error()})
			return
		}
		sess = created
		w.Header().Set("Mcp-Session-Id", id)
		s.closeExpiredConnections(r)
	} else {
		var valid bool
		sess, valid = s.lookupConnection(r.Header.Get("Mcp-Session-Id"), agent.AgentID, principal, connectionMode(agent), firstString(agent.Meta, "connection_instance"))
		if !valid {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "connection session expired or unavailable; initialize again"})
			return
		}
	}
	r = r.WithContext(context.WithValue(r.Context(), connectionContextKey{}, connectionScope{Target: agent.AgentID, Key: sess.Scope, Instance: sess.Instance}))
	result, rpcErr, noContent := s.agentMCPJSONRPC(r, agent, body)
	if noContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	response := map[string]any{"jsonrpc": "2.0", "id": body["id"]}
	if rpcErr != nil {
		response["error"] = rpcErr
	} else {
		if method == "initialize" {
			if initialized, ok := result.(map[string]any); ok {
				initialized["protocolVersion"] = connectionProtocol(firstString(mapValue(body["params"]), "protocolVersion"))
				initialized["capabilities"] = map[string]any{"tools": map[string]any{}}
			}
		}
		response["result"] = result
	}
	writeJSON(w, http.StatusOK, response)
}

func managedLocalConnector(w http.ResponseWriter, agent Agent) bool {
	if intFromAny(agent.Meta["connection_scopes_version"]) < 1 || firstString(agent.Meta, "connection_instance") == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "upstream connector must be upgraded for isolated connections"})
		return false
	}
	if agent.Transport == "stdio" && (!truthy(agent.Meta["managed_stdio_owner"]) || !truthy(agent.Meta["managed_stdio_platform"])) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "stdio connection requires its verified owner-local Unix connector"})
		return false
	}
	return true
}

func connectionProtocol(requested string) string {
	switch requested {
	case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25", mcpProtocolVersion:
		return requested
	default:
		return "2025-03-26"
	}
}

func (s *Server) newConnection(agent Agent, principal string) (string, connectionSession, error) {
	now := time.Now()
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	if s.connectionSessions == nil {
		s.connectionSessions = make(map[string]connectionSession)
	}
	for id, session := range s.connectionSessions {
		if !session.Closing && !now.Before(session.Expires) {
			if s.connectionExpired == nil {
				s.connectionExpired = make(map[string]connectionSession)
			}
			if len(s.connectionExpired) < connectionSessionLimit || s.connectionExpired[session.Scope].Scope != "" {
				s.connectionExpired[session.Scope] = session
				delete(s.connectionSessions, id)
			}
		}
	}
	if len(s.connectionSessions) >= connectionSessionLimit {
		return "", connectionSession{}, fmt.Errorf("connection session capacity reached")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", connectionSession{}, fmt.Errorf("cannot initialize connection session")
	}
	id := hex.EncodeToString(random[:])
	sess := connectionSession{Principal: principal, Target: agent.AgentID,
		Scope: connectionDigest("connection-v1", principal, agent.AgentID, id, firstString(agent.Meta, "connection_instance")), Expires: now.Add(connectionSessionTTL),
		Mode: connectionMode(agent), Parent: firstString(agent.Meta, "parent_server_id"), Ref: firstString(agent.Meta, "child_ref"), Instance: firstString(agent.Meta, "connection_instance")}
	if sess.Mode == "stateless" {
		// Reuse a live group's opaque scope. Once its final downstream session
		// closes, a new initialize creates another group, so queued teardown can
		// never close a replacement that reused a deterministic key.
		for _, live := range s.connectionSessions {
			if !live.Closing && live.Principal == principal && live.Target == agent.AgentID && live.Mode == sess.Mode && live.Instance == sess.Instance {
				sess.Scope = live.Scope
				break
			}
		}
	}
	s.connectionSessions[id] = sess
	return id, sess, nil
}

func connectionMode(agent Agent) string {
	if firstString(agent.Meta, "connection_mode") == "stateless" {
		return "stateless"
	}
	return "session"
}

// Expired groups are closed through the same real ShellMCP callback path.
// At most eight queued closures per initialize; failures remain pending for
// retry instead of silently being called disposed.
func (s *Server) closeExpiredConnections(r *http.Request) {
	s.connectionMu.Lock()
	pending := make(map[string]connectionSession)
	for scope, session := range s.connectionExpired {
		live := false
		for _, other := range s.connectionSessions {
			if other.Scope == scope {
				live = true
				break
			}
		}
		if live {
			delete(s.connectionExpired, scope)
		} else if len(pending) < 8 && session.Parent != "" && session.Ref != "" {
			pending[scope] = session
			if session.CloseJob == "" {
				session.CloseJob = "submitting"
				s.connectionExpired[scope] = session
			}
		}
	}
	s.connectionMu.Unlock()
	for scope, session := range pending {
		if session.CloseJob != "" {
			s.mu.Lock()
			job := s.shellJobs[session.CloseJob]
			state := ""
			closed := false
			if job != nil {
				state = job.Status
				closed = connectionCloseSucceeded(shellJobResponse(job))
			}
			s.mu.Unlock()
			if state != "completed" && state != "failed" && state != "cancelled" {
				continue
			}
			if closed {
				s.connectionMu.Lock()
				delete(s.connectionExpired, scope)
				s.connectionMu.Unlock()
				continue
			}
		}
		result := s.callShellToolWithTraceParent(session.Parent, "mcp_connection_close",
			map[string]any{"ref": session.Ref, "_connection_scope": scope, "_connection_instance": session.Instance}, true, s.cfg.ActionSyncWait, requestTraceID(r), requestTraceParent(r))
		if connectionCloseSucceeded(result) {
			s.connectionMu.Lock()
			delete(s.connectionExpired, scope)
			s.connectionMu.Unlock()
		} else if firstString(result, "status") == "running" && firstString(result, "job_id") != "" {
			s.connectionMu.Lock()
			session.CloseJob = firstString(result, "job_id")
			s.connectionExpired[scope] = session
			s.connectionMu.Unlock()
		} else {
			s.connectionMu.Lock()
			session.CloseJob = ""
			s.connectionExpired[scope] = session
			s.connectionMu.Unlock()
		}
	}
}

func (s *Server) lookupConnection(id, target, principal, mode, instance string) (connectionSession, bool) {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	sess, ok := s.connectionSessions[id]
	if !ok || sess.Closing || sess.Target != target || sess.Principal != principal || sess.Mode != mode || sess.Instance != instance || !time.Now().Before(sess.Expires) {
		return connectionSession{}, false
	}
	sess.Expires = time.Now().Add(connectionSessionTTL)
	s.connectionSessions[id] = sess
	return sess, true
}

func (s *Server) closeConnection(w http.ResponseWriter, r *http.Request, agent Agent, principal string) {
	id := r.Header.Get("Mcp-Session-Id")
	s.connectionMu.Lock()
	sess, ok := s.connectionSessions[id]
	if !ok || sess.Target != agent.AgentID || sess.Principal != principal {
		s.connectionMu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if sess.Closing {
		s.connectionMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "upstream connection close is already in progress"})
		return
	}
	shared := false
	for otherID, other := range s.connectionSessions {
		if otherID != id && other.Scope == sess.Scope {
			shared = true
			break
		}
	}
	if shared {
		delete(s.connectionSessions, id)
	} else {
		// Keep the capacity reservation until teardown is confirmed. Otherwise
		// concurrent initialize can fill the slot before a failed close restores it.
		sess.Closing = true
		s.connectionSessions[id] = sess
	}
	s.connectionMu.Unlock()
	if !shared {
		releaseAuthAdmission(r)
		result := s.callShellToolWithTraceParent(sess.Parent, "mcp_connection_close",
			map[string]any{"ref": sess.Ref, "_connection_scope": sess.Scope, "_connection_instance": sess.Instance},
			false, s.cfg.DefaultTimeout, requestTraceID(r), requestTraceParent(r))
		if !connectionCloseSucceeded(result) {
			s.connectionMu.Lock()
			sess.Closing = false
			s.connectionSessions[id] = sess
			s.connectionMu.Unlock()
			writeJSON(w, http.StatusBadGateway, map[string]any{"detail": "upstream connection close was not confirmed"})
			return
		}
		s.connectionMu.Lock()
		delete(s.connectionSessions, id)
		s.connectionMu.Unlock()
	}
	w.WriteHeader(http.StatusNoContent)
}

func connectionCloseSucceeded(result map[string]any) bool {
	return firstString(result, "status") == "completed" && truthy(childMCPResponse(result)["closed"])
}

func (s *Server) shellConnectionArguments(r *http.Request, target, tool string, args map[string]any) map[string]any {
	switch tool {
	case "mcp_tools", "mcp_call", "mcp_request", "mcp_connection_close":
		ref := firstString(args, "ref")
		id := "mcp:" + target + ":" + ref
		s.mu.Lock()
		child, found := s.exposedAgentByIDLocked(id)
		s.mu.Unlock()
		if !found {
			child = Agent{AgentID: id}
		}
		prepared := childConnectionArguments(r, child, args)
		if tool == "mcp_connection_close" {
			delete(prepared, "_connection_expires_at")
		}
		return prepared
	default:
		return args
	}
}

func childConnectionArguments(r *http.Request, agent Agent, args map[string]any) map[string]any {
	args = cloneMap(args)
	delete(args, "_connection_scope")
	delete(args, "_connection_expires_at")
	delete(args, "_connection_instance")
	if r == nil {
		return args
	}
	if scope, ok := r.Context().Value(connectionContextKey{}).(connectionScope); ok && scope.Target == agent.AgentID {
		args["_connection_scope"] = scope.Key
		args["_connection_instance"] = scope.Instance
	}
	// Legacy facades remain unscoped until managed URL acceptance. They have no
	// downstream DELETE lifecycle, so bearer refresh must not create retained
	// mutable partitions. Only explicit /connection admission supplies a scope.
	if args["_connection_scope"] != nil {
		args["_connection_expires_at"] = time.Now().Add(time.Minute).UnixMilli()
	}
	return args
}
