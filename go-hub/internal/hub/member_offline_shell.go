package hub

import (
	"context"
	"errors"
	"net/http"
)

type memberOfflineSnapshotKey struct{}

type memberOfflineSnapshot struct {
	state  authSnapshotState
	bundle authSnapshot
}

// Make a fallback available only for a qualified route. The proxy invokes it
// only when no connection was obtained and the action body was never read.
func (s *Server) memberOfflineShellFallback(r *http.Request, next http.Handler) http.HandlerFunc {
	if s.cfg.AuthMode != "reader" || s.cfg.PublicOrigin == "" || r.Method != http.MethodPost || r.URL.Path != "/mcp" ||
		r.Header.Get("X-GPTAdmin-Quorum-Forwarded") != "" {
		return nil
	}
	token := s.offlineShellBearer(r)
	if token == "" {
		return nil
	}
	return func(w http.ResponseWriter, r *http.Request) {
		_, err := s.verifyNodeWriterBearer(r, token)
		var failure *nodeBearerAdmissionError
		if errors.As(err, &failure) && failure.offlineEligible && (r.Header.Get("X-GPTAdmin-Offline-Owner") == "" || failure.reason != nodeBearerReasonWriterStatus) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), offlineShellMCPKey{}, true)))
			return
		}
		if failure != nil && !failure.unavailable {
			s.authAudit("mcp_auth_denied", r, map[string]any{"reason": failure.Error()})
			if !s.authFailureRateLimited(w, r) {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.oauthResourceMetadataURL(r)+`"`)
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			}
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "offline member admission unavailable", "executed": false})
	}
}

// The normal reader gate supplies the exact validated snapshot and pins its
// generation through authentication, just as it does for online requests.
func (s *Server) authenticateMemberOfflineShell(w http.ResponseWriter, r *http.Request) bool {
	snapshot, ok := r.Context().Value(memberOfflineSnapshotKey{}).(memberOfflineSnapshot)
	s.mu.Lock()
	nodeID := s.selfNodeIDLocked()
	identityMatches := s.nodeIdentity != nil && s.nodeIdentity.WriterID == snapshot.state.WriterID
	s.mu.Unlock()
	if !ok || !snapshot.state.ReadOnly || snapshot.bundle.Profiles == nil || nodeID == "" || !identityMatches {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "applied reader authority unavailable"})
		return false
	}

	view := newOfflineShellAuthView(s.cfg, s.securitySnapshot(), snapshot.bundle)
	claims, profile, ok := s.authenticateOfflineShellBearer(w, r, view)
	if !ok {
		return false
	}
	member := s.memberOfflineEnrollment(nodeID)
	if member == nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "offline shell requires an enrolled local executor"})
		return false
	}
	ctx := context.WithValue(r.Context(), ownerReadAuthorityKey{}, view)
	ctx = context.WithValue(ctx, localShellAuthStateKey{}, snapshot.state)
	ctx = context.WithValue(ctx, offlineShellAuthorityKey{}, offlineShellAuthority{member: *member})
	*r = *requestWithAccessProfile(requestWithAuthClaims(r.WithContext(ctx), claims), s.resolveAccessProfileTargets(profile))
	if requestAccessMode(r) != accessModeFull || !s.offlineShellHasTarget(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "offline shell requires an explicit shell_exec profile"})
		return false
	}
	return true
}

// The membership view came from the authenticated writer route. Keep the
// last ready enrollment across lease expiry while no writer is reachable.
func (s *Server) memberOfflineEnrollment(nodeID string) *nodeMember {
	s.membershipMu.Lock()
	defer s.membershipMu.Unlock()
	if s.membershipView != nil {
		for _, member := range s.membershipView.Nodes {
			if member != nil && member.NodeID == nodeID && member.State == nodeStateReady &&
				containsString(member.Capabilities, "executor") {
				copy := *member
				return &copy
			}
		}
	}
	return nil
}

func (s *Server) memberOfflineShellExecutorAllowedLocked(r *http.Request, target string, pin *localShellExecutorIdentity) bool {
	authority, ok := r.Context().Value(offlineShellAuthorityKey{}).(offlineShellAuthority)
	agent := s.agents[target]
	return ok && authority.member.Name == target && agent != nil && truthyAny(agent.Meta["approved"]) &&
		agent.Status != "disabled" && agent.Status != "awaiting_approval" &&
		firstString(agent.Meta, "server_id") == pin.ID && firstString(agent.Meta, "fingerprint") == pin.Fingerprint &&
		(authority.member.LocalID == "" || authority.member.LocalID == pin.ID) &&
		(authority.member.LocalFingerprint == "" || authority.member.LocalFingerprint == pin.Fingerprint)
}

func (s *Server) fenceMemberOfflineShellLocked(r *http.Request, target string, job *shellJob) error {
	state, ok := r.Context().Value(localShellAuthStateKey{}).(authSnapshotState)
	pin := s.localShellIdentityLocked(target)
	if !ok || state != s.authState || job.OwnerID == "" || job.ToolName != "shell_exec" || job.ReceiptTarget != target ||
		job.FilesystemACL != nil || r.Context().Err() != nil || !offlineShellProfileAllows(r, target) ||
		pin == nil || !s.memberOfflineShellExecutorAllowedLocked(r, target, pin) {
		return errExecutorUnavailable
	}
	job.LocalExecutor = pin
	return nil
}
