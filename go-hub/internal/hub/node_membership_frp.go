package hub

import (
	"errors"
	"log"
	"maps"
	"net/http"
	"time"
)

var errFRPJoinLeader = errors.New("FRP relay enrollment requires the quorum-confirmed membership leader")

// finishFRPRelayJoin takes ownership of membershipMu and releases it before DNS I/O.
func (s *Server) finishFRPRelayJoin(w http.ResponseWriter, r *http.Request, grant *nodeJoinGrant, member, previous *nodeMember, now time.Time) {
	member, publisher, code, err := s.enrollFRPRelayLocked(r, grant, member, previous, now)
	if member != nil {
		copyMember := *member
		member = &copyMember
	}
	firstJoin := !grant.Used
	writerID, writerURL, revision := s.membership.WriterID, s.membership.WriterURL, s.membership.Revision
	s.membershipMu.Unlock()
	if member != nil {
		if firstJoin {
			s.rebuildMembershipRoutes()
			err = publisher.publish(r.Context(), member.FRPRelayIPv4)
		} else {
			err = publisher.reconcile(r.Context(), member.FRPRelayIPv4)
		}
		if err != nil {
			code = http.StatusServiceUnavailable
		}
	}
	if err != nil {
		if code == http.StatusServiceUnavailable {
			log.Printf("FRP relay enrollment: %v", err)
			writeJSON(w, code, map[string]any{
				"detail": err.Error(), "code": "frp_dns_publication_failed", "retryable": true,
			})
			return
		}
		writeJSON(w, code, map[string]any{"detail": err.Error()})
		return
	}
	s.writeNodeJoinResponse(w, member, writerID, writerURL, revision)
}

func (s *Server) enrollFRPRelayLocked(r *http.Request, grant *nodeJoinGrant, member, previous *nodeMember, now time.Time) (*nodeMember, frpDNSPublisher, int, error) {
	switch {
	case grant.Used:
		if previous == nil || previous.Generation != 1 ||
			previous.FRPRelayIPv4 != grant.FRPRelayIPv4 || previous.State == nodeStateDisabled {
			return nil, nil, http.StatusForbidden, errors.New("join grant is invalid or already used")
		}
		member = previous
	case previous != nil:
		return nil, nil, http.StatusConflict, errors.New("FRP relay grant requires a first-time node")
	}
	if !validFRPRelayIPv4(grant.FRPRelayIPv4) {
		return nil, nil, http.StatusBadRequest, errors.New("invalid approved FRP relay IPv4 address")
	}
	if !s.frpJoinAdmitted(r) {
		return nil, nil, http.StatusServiceUnavailable, errFRPJoinLeader
	}
	publisher, err := frpDNSPublisherFromEnv(s.membership.FRPSpaceWebPool)
	if err != nil {
		return nil, nil, http.StatusServiceUnavailable, err
	}
	if !grant.Used {
		// Commit the exact identity and owner-approved IP before any DNS write.
		// The consumed grant is a retry handle for this identity until expiry.
		next := *s.membership
		next.Nodes = maps.Clone(next.Nodes)
		next.Grants = maps.Clone(next.Grants)
		usedGrant := *grant
		usedGrant.Used, usedGrant.UsedBy, usedGrant.UsedAt = true, member.NodeID, now
		member.Generation, member.FRPRelayIPv4 = 1, grant.FRPRelayIPv4
		next.Nodes[member.NodeID] = member
		next.Grants[grant.SecretHash] = &usedGrant
		next.Revision++
		if next.WriterURL == "" {
			next.WriterURL = s.requestOrigin(r)
		}
		if err := s.persistMembershipStateLocked(&next); err != nil {
			log.Printf("FRP relay membership commit: %v", err)
			return nil, nil, http.StatusServiceUnavailable, errors.New("FRP relay membership persistence failed")
		}
		s.membership = &next
	}
	return member, publisher, http.StatusOK, nil
}

// The quorum HTTP middleware verifies majority leadership and materializes the
// writer before dispatch. Owner-local mode also attaches its admission term.
func (s *Server) frpJoinAdmitted(r *http.Request) bool {
	node := s.quorumNode
	term := s.quorumAuthorityTerm.Load()
	if node == nil || s.cfg.QuorumConfigFile == "" || term == 0 || !s.quorumAuthorityActiveWriter.Load() ||
		term != node.CurrentTerm() || !node.Status().Leader {
		return false
	}
	if s.quorumControlOnly() {
		admitted, ok := r.Context().Value(ownerAdmissionTermKey{}).(uint64)
		return ok && admitted == term
	}
	return true
}
