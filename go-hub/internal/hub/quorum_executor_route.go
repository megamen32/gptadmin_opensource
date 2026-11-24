package hub

import (
	"context"
	"errors"
	"strings"
)

func unavailableExecutorQueue(detail string) map[string]any {
	return map[string]any{"status": "failed", "error": map[string]any{"code": "executor_queue_unavailable", "message": detail}, "executed": false, "retryable": true}
}

type executorEnrollment struct {
	nodeMember
	voter bool
}

func (e *executorEnrollment) bound() bool {
	return e.LocalID != "" && e.LocalFingerprint != ""
}

func (e *executorEnrollment) matches(id, fingerprint string) bool {
	return id != "" && fingerprint != "" && (e.LocalID == "" || e.LocalID == id) &&
		(e.LocalFingerprint == "" || e.LocalFingerprint == fingerprint)
}

func conflictingExecutorPin(stored any, enrolled string) bool {
	return enrolled != "" && stored != nil && stored != "" && stored != enrolled
}

var errExecutorConflict = errors.New("conflicting quorum executor ownership")
var errExecutorUnavailable = errors.New("quorum executor is not available for new work")

// The caller holds s.mu. Resolve once by enrolled principal for both dispatch
// and queue credentials. A copy keeps subsequent heartbeats outside this read.
func (s *Server) executorEnrollmentLocked(target string) (*executorEnrollment, error) {
	s.membershipMu.Lock()
	defer s.membershipMu.Unlock()
	var members map[string]*nodeMember
	memberView := false
	if s.membership != nil {
		members = s.membership.Nodes
	} else if s.membershipView != nil {
		memberView = true
		members = make(map[string]*nodeMember, len(s.membershipView.Nodes))
		for _, member := range s.membershipView.Nodes {
			if member != nil {
				members[member.NodeID] = member
			}
		}
	}
	if members == nil {
		return nil, nil
	}
	var voter, ordinary *nodeMember
	var named, disabledVoter bool
	for id, m := range members {
		if m == nil || m.Name != target {
			continue
		}
		named = true
		if id == "" || m.NodeID != id {
			return nil, errExecutorConflict
		}
		var isVoter bool
		if s.quorumPrivateTLS != nil {
			_, isVoter = s.quorumPrivateTLS.peers[id]
		}
		if m.State == nodeStateDisabled {
			disabledVoter = disabledVoter || isVoter
			continue
		}
		if !containsString(m.Capabilities, "executor") || (m.State != nodeStateReady && m.State != nodeStateOffline && m.State != nodeStateDraining) {
			return nil, errExecutorUnavailable
		}
		if isVoter {
			peer := s.quorumPrivateTLS.peers[id]
			if voter != nil || (m.KeyHash != "" && !strings.EqualFold(peer.NodeKeyHash, m.KeyHash)) {
				return nil, errExecutorConflict
			}
			voter = m
		} else {
			if ordinary != nil {
				return nil, errExecutorConflict
			}
			ordinary = m
		}
	}
	owner := ordinary
	if voter != nil {
		// The old writer also enrolled its own executor under its server UUID.
		// Only that exact row can be an alias. Its credential never owns the queue.
		if ordinary != nil && (voter.KeyHash == "" || voter.LocalFingerprint == "" || ordinary.NodeID != voter.LocalID || ordinary.LocalID != voter.LocalID ||
			ordinary.LocalFingerprint != voter.LocalFingerprint || ordinary.State == nodeStateDraining) {
			return nil, errExecutorConflict
		}
		owner = voter
	} else if disabledVoter || (named && owner == nil) {
		return nil, errExecutorUnavailable
	}
	if owner == nil {
		return nil, nil
	}
	selected := &executorEnrollment{nodeMember: *owner, voter: voter != nil}
	if pin := s.localExecutors[target]; pin != nil {
		id, fp := pin["server_id"], pin["fingerprint"]
		if pin["public_key"] == "" || !selected.matches(id, fp) ||
			(owner.NodeID != s.quorumNode.Status().NodeID && (selected.voter || owner.NodeID != id)) {
			return nil, errExecutorConflict
		}
		// The original writer can have only its auto-created server UUID row.
		selected.LocalID, selected.LocalFingerprint = id, fp
	}
	if agent := s.agents[target]; agent != nil {
		if agent.Status == "disabled" {
			return nil, errExecutorUnavailable
		}
		id, fp := firstString(agent.Meta, "server_id"), firstString(agent.Meta, "fingerprint")
		if conflictingExecutorPin(agent.Meta["server_id"], selected.LocalID) || conflictingExecutorPin(agent.Meta["fingerprint"], selected.LocalFingerprint) {
			return nil, errExecutorConflict
		}
		// An original writer can be seen remotely after an election. Its
		// auto-created row uses the executor UUID itself as the Raft node ID.
		if selected.voter && selected.NodeID == id && !selected.bound() && truthyAny(agent.Meta["approved"]) && selected.matches(id, fp) {
			selected.LocalID, selected.LocalFingerprint = id, fp
		}
		if selected.bound() {
			if approved, set := agent.Meta["approved"]; agent.Status == "awaiting_approval" || (set && !truthyAny(approved)) {
				return nil, errExecutorUnavailable
			}
		}
	}
	if selected.voter && (!selected.bound() || (!memberView && selected.KeyHash == "" && selected.NodeID != selected.LocalID)) {
		return nil, errExecutorConflict
	}
	return selected, nil
}

// A quorum member executor polls the leader-owned durable queue using the
// mutually authenticated executor admission path. Do not forward the command
// to a follower Hub: its quorum admission would send it back to the leader.
// The task stays bound to its enrolled executor. Its admitting Hub persists
// the input according to the selected quorum storage mode before dispatch.
func (s *Server) quorumExecutorUsesLeaderQueue(ctx context.Context, target string) (bool, error) {
	if s.quorumNode == nil || !strings.HasPrefix(target, "shell:") {
		return false, nil
	}
	s.mu.Lock()
	owner, err := s.executorEnrollmentLocked(target)
	local := s.localExecutors[target] != nil
	_, routed := s.nodePeerRouteLocked(target)
	connected := s.nodeConns.get(target) != nil
	s.mu.Unlock()
	if err != nil {
		return false, err
	}
	if owner == nil {
		if !local && (routed || connected) {
			return false, errors.New("peer route has no enrolled quorum executor")
		}
		return false, nil // Standalone ShellMCP polls the admitting Hub's queue.
	}
	if owner.State == nodeStateDraining {
		return false, errExecutorUnavailable
	}
	if local {
		return false, nil
	}
	if owner.NodeID == s.quorumNode.Status().NodeID {
		return false, errors.New("local quorum executor is not registered in this process")
	}
	if !owner.bound() || (!owner.voter && !owner.QuorumQueue) {
		return false, errors.New("enrolled executor has not advertised a bound quorum queue")
	}
	// Only voters use addressed peer dispatch; nonvoters keep their enrolled queue.
	addressed := owner.voter && ctx.Value(localShellAdmissionKey{}) == target
	if addressed && !local && !routed && !connected {
		return false, errors.New("addressed executor has no authenticated peer route")
	}
	if addressed && !local && (!s.quorumAuthorityActiveWriter.Load() || s.quorumNode.Status().VoterCount < quorumMinimumVoters) {
		return false, nil
	}
	if !s.quorumAuthorityActiveWriter.Load() {
		return false, errors.New("quorum authority is not ready")
	}
	if s.quorumNode.Status().VoterCount < quorumMinimumVoters {
		return false, errors.New("quorum voter set is not ready")
	}
	if err := s.quorumNode.VerifyLeader(ctx); err != nil {
		return false, err
	}
	if err := s.taskQuorumReplicaReady(); err != nil {
		return false, err
	}
	return true, nil
}
