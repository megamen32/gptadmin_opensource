package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

type offlineShellMCPKey struct{}
type offlineShellAuthorityKey struct{}
type offlineDurableBearerKey struct{}

type offlineShellAuthority struct {
	index    uint64
	member   nodeMember
	registry map[string]Agent
	members  map[string]*nodeMember
}

func offlineShellMCP(r *http.Request) bool {
	return r != nil && r.Context().Value(offlineShellMCPKey{}) == true
}

// A voter may use an enabled credential against applied quorum authority
// when there is no known leader, including after it was the writer.
func (s *Server) offlineShellMCPRoute(r *http.Request) bool {
	return s.quorumControlOnly() && s.offlineShellBearer(r) != "" && s.cfg.PublicOrigin != "" && r.Method == http.MethodPost &&
		r.URL.Path == "/mcp" && (r.Header.Get(nodePeerHopHeader) == "" || r.Header.Get(nodePeerHopHeader) == "1") &&
		r.Header.Get("X-GPTAdmin-Quorum-Forwarded") == "" && s.ownerReceiptStoreIsLocal()
}

// Credential classes have separate owner opt-ins. Managed digest verification
// does not use the JWT signer, even when the caller uses the same MCP route.
func (s *Server) offlineShellBearer(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) < 8 || !strings.EqualFold(auth[:7], "Bearer ") {
		return ""
	}
	token := strings.TrimSpace(auth[7:])
	if !validNodeAdmissionToken(token) {
		return ""
	}
	if strings.HasPrefix(token, "gptk_") {
		if s.cfg.OfflineDurableBearer {
			return token
		}
	} else if s.cfg.ReaderOfflineSharedSigner && s.cfg.OAuthClientSecret != "" && strings.Count(token, ".") == 2 {
		return token
	}
	return ""
}

func offlineShellProfileAllows(r *http.Request, target string) bool {
	profile, ok := AccessProfileFromRequest(r)
	if !ok || profile.ApprovalMode != approvalModeUnrestricted || !strings.HasPrefix(target, "shell:") ||
		target != "shell:"+canonicalShellQueueName(strings.TrimPrefix(target, "shell:")) {
		return false
	}
	if r.Context().Value(offlineDurableBearerKey{}) == true {
		return authorizeToolCall(r, target, "shell_exec") == nil
	}
	return containsString(profile.AllowedTargets, target) && containsString(profile.AllowedTools, "shell_exec")
}

func (s *Server) offlineShellTargetAllowed(r *http.Request, target string) bool {
	if !offlineShellProfileAllows(r, target) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offlineShellExecutorAllowedLocked(r, target)
}

func (s *Server) offlineShellRPCAllowed(r *http.Request, body map[string]any) bool {
	params := mapValue(body["params"])
	switch firstString(body, "method") {
	case "initialize", "server/discover", "notifications/initialized", "tools/list", "tasks/get":
		return true
	case "tools/call":
		switch firstString(params, "name") {
		case "exec", "ro":
			return true
		case "job":
			_, scoped, err := decodeOwnerHandle(firstString(mapValue(params["arguments"]), "id", "job_id"))
			return err == nil && scoped
		case "search":
			target := firstString(mapValue(params["arguments"]), "target")
			return s.offlineShellTargetAllowed(r, target) || s.offlineRemoteAllowed(r, target)
		}
	}
	return false
}

func (s *Server) authenticateOfflineShellMCP(w http.ResponseWriter, r *http.Request) bool {
	if s.quorumNode == nil {
		return s.authenticateMemberOfflineShell(w, r)
	}
	image, err := s.readQuorumAuthorityImageFor(map[string]string{"auth": "bundle", "security": "settings", "membership": "writer", "registry": "enrollment"})
	var bundle authSnapshot
	var security securitySettings
	var membership nodeMembershipState
	var registry persistentRegistryState
	_, applyErr := s.quorumNode.ExternalApplyError()
	if err != nil || applyErr != nil || image.AppliedIndex == 0 || image.AppliedIndex != s.quorumNode.ExternalAppliedIndex() ||
		json.Unmarshal(image.Values["auth/bundle"], &bundle) != nil || bundle.Generation == 0 ||
		bundle.Tokens == nil || bundle.Clients == nil || bundle.Profiles == nil ||
		json.Unmarshal(image.Values["security/settings"], &security) != nil ||
		json.Unmarshal(image.Values["membership/writer"], &membership) != nil ||
		json.Unmarshal(image.Values["registry/enrollment"], &registry) != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "applied quorum MCP authority unavailable"})
		return false
	}
	view := newOfflineShellAuthView(s.cfg, security, bundle)
	claims, profile, ok := s.authenticateOfflineShellBearer(w, r, view)
	if !ok {
		return false
	}
	ctx := context.WithValue(r.Context(), ownerReadAuthorityKey{}, view)
	nodeID := s.localReadbackOwnerID()
	member := membership.Nodes[nodeID]
	if member == nil || member.NodeID != nodeID {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "applied local executor enrollment unavailable"})
		return false
	}
	ctx = context.WithValue(ctx, offlineShellAuthorityKey{}, offlineShellAuthority{index: image.AppliedIndex, member: *member, registry: registry.Agents, members: membership.Nodes})
	*r = *requestWithAccessProfile(requestWithAuthClaims(r.WithContext(ctx), claims), s.resolveAccessProfileTargets(profile))
	if requestAccessMode(r) != accessModeFull || !s.offlineShellHasTarget(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "offline shell requires an explicit shell_exec profile"})
		return false
	}
	return true
}

// The isolated view verifies an applied snapshot without installing or
// modifying auth state. Both roles use the normal bearer verifier and binding.
func newOfflineShellAuthView(cfg Config, security securitySettings, bundle authSnapshot) *Server {
	view := &Server{cfg: cfg, security: security}
	view.cfg.ConfigDir, view.cfg.AuthMode, view.cfg.RelaxAuthChecks = "", "", false
	view.security.Bearer = maximumBearerSecurityProfile()
	view.installAuthMapsLocked(bundle)
	return view
}

func (s *Server) authenticateOfflineShellBearer(w http.ResponseWriter, r *http.Request, view *Server) (map[string]any, AccessProfile, bool) {
	token := s.offlineShellBearer(r)
	durable := strings.HasPrefix(token, "gptk_")
	claims, err := view.verifyJWTForRequest(r, token)
	if err != nil || (!durable && intFromAny(claims["exp"]) <= int(s.now().Unix())) {
		s.authAudit("mcp_auth_denied", r, map[string]any{"reason": "offline shell bearer verification failed"})
		if !s.authFailureRateLimited(w, r) {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.oauthResourceMetadataURL(r)+`"`)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "offline shell bearer not authorized"})
		}
		return nil, AccessProfile{}, false
	}
	var profile AccessProfile
	var bound bool
	if durable {
		profile, _, bound = view.accessProfileBinding(claims)
	} else {
		client, registered := view.oauthClients[firstString(claims, "client_id")]
		profile, bound = view.accessProfiles[client.ProfileID]
		bound = registered && bound
	}
	if !bound {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "offline shell requires an explicit shell_exec profile"})
		return nil, AccessProfile{}, false
	}
	claims["profile_id"] = profile.ID
	if durable {
		*r = *r.WithContext(context.WithValue(r.Context(), offlineDurableBearerKey{}, true))
	}
	return claims, profile, true
}

func (s *Server) offlineShellHasTarget(r *http.Request) bool {
	s.mu.Lock()
	for target := range s.localExecutors {
		if offlineShellProfileAllows(r, target) && s.offlineShellExecutorAllowedLocked(r, target) {
			s.mu.Unlock()
			return true
		}
	}
	staticTargets := make([]string, 0, len(s.nodePeers))
	for target := range s.nodePeers {
		staticTargets = append(staticTargets, target)
	}
	s.mu.Unlock()
	for _, target := range staticTargets {
		if s.offlineRemoteAllowed(r, target) {
			return true
		}
	}
	if authority, ok := r.Context().Value(offlineShellAuthorityKey{}).(offlineShellAuthority); ok {
		for _, member := range authority.members {
			if member != nil && s.offlineRemoteAllowed(r, member.Name) {
				return true
			}
		}
	}
	return false
}

// The caller holds s.mu. Historical reads still require the original task caller.
func (s *Server) offlineShellJobAllowedLocked(r *http.Request, job *shellJob) bool {
	target := "shell:" + job.Server
	pin := s.localShellIdentityLocked(target)
	return job.ToolName == "shell_exec" && offlineShellProfileAllows(r, target) && pin != nil &&
		(job.LocalExecutor == nil || *job.LocalExecutor == *pin)
}

// A reader's ordinary membership projection can lag the control database.
func (s *Server) offlineShellExecutorAllowedLocked(r *http.Request, target string) bool {
	authority, offline := r.Context().Value(offlineShellAuthorityKey{}).(offlineShellAuthority)
	pin := s.localShellIdentityLocked(target)
	if pin == nil {
		return false
	}
	if offline && s.quorumNode == nil {
		return s.memberOfflineShellExecutorAllowedLocked(r, target, pin)
	}
	if !offline {
		return s.localShellEligibleLocked(target)
	}
	m := authority.member
	agent, registered := authority.registry[target]
	if m.Name != target || m.State == nodeStateDisabled || m.State == nodeStateDraining ||
		!containsString(m.Capabilities, "executor") || !registered || !truthyAny(agent.Meta["approved"]) ||
		agent.Status == "disabled" || agent.Status == "awaiting_approval" {
		return false
	}
	return firstString(agent.Meta, "server_id") == pin.ID && firstString(agent.Meta, "fingerprint") == pin.Fingerprint &&
		(m.LocalID == "" || m.LocalID == pin.ID) && (m.LocalFingerprint == "" || m.LocalFingerprint == pin.Fingerprint)
}

// A former cluster-wide reservation can be read locally when both Raft and
// SQLite identify this same physical owner and the same persisted task.
func (s *Server) replayOfflineSharedShell(r *http.Request, key, fingerprint, target string, background bool) (map[string]any, int) {
	unknown := map[string]any{"status": "failed", "error": map[string]any{
		"code": "task_outcome_unknown", "message": "the cluster-wide key has no provable result on this owner; retry the same key after quorum returns",
	}, "executed": false}
	if s.quorumNode == nil {
		return unknown, http.StatusConflict
	}
	op, found := s.quorumNode.LocalOperation(sha256Hex([]byte(key)))
	if !found || op.Owner != s.localReadbackOwnerID() || op.JobID == "" ||
		(op.Expires != 0 && op.Expires <= s.now().Unix()) {
		return unknown, http.StatusConflict
	}
	var process, storedFingerprint, jobID string
	var created float64
	if err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT owner,created,fingerprint,job_id FROM task_requests WHERE key=?", key).
			Scan(&process, &created, &storedFingerprint, &jobID)
	}); err != nil || jobID != op.JobID || op.Witness != ownerRequestWitness(process, created) ||
		op.Fingerprint != sha256Hex([]byte(storedFingerprint)) {
		return unknown, http.StatusConflict
	}
	if fingerprint != storedFingerprint {
		return durableRequestConflict().response, http.StatusConflict
	}
	if !background {
		reply := s.lookupOwner(r, ownerRead{Action: "job", ID: jobID})
		if reply.Failure != nil {
			return unknown, http.StatusConflict
		}
		return mapValue(reply.Value), http.StatusOK
	}
	if _, err := s.mcpTaskGetForRequest(r, jobID, target); err != nil {
		return unknown, http.StatusConflict
	}
	return map[string]any{"status": "running", "background": true, "job_id": jobID}, http.StatusOK
}
