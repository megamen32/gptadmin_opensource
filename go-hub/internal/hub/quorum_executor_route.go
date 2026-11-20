package hub

import (
	"context"
	"errors"
	"strings"
)

func unavailableExecutorQueue(detail string) map[string]any {
	return map[string]any{"status": "failed", "error": map[string]any{"code": "executor_queue_unavailable", "message": detail}, "executed": false, "retryable": true}
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
	s.membershipMu.Lock()
	peerID := ""
	if s.membership != nil {
		for id, member := range s.membership.Nodes {
			if member == nil || member.NodeID != id || member.Name != target {
				continue
			}
			if !containsString(member.Capabilities, "executor") || (member.State != nodeStateReady && member.State != nodeStateOffline) {
				s.membershipMu.Unlock()
				return false, errors.New("quorum executor is not available for new work")
			}
			isPeer := false
			if s.quorumPrivateTLS != nil {
				_, isPeer = s.quorumPrivateTLS.peers[id]
			}
			if !isPeer && (!member.QuorumQueue || member.LocalID == "" || member.LocalFingerprint == "") {
				s.membershipMu.Unlock()
				return false, errors.New("enrolled executor has not advertised a bound quorum queue")
			}
			if peerID != "" {
				s.membershipMu.Unlock()
				return false, errors.New("multiple quorum executors own the same target")
			}
			peerID = id
		}
	}
	s.membershipMu.Unlock()
	if peerID == "" {
		s.mu.Lock()
		_, routed := s.nodePeerRouteLocked(target)
		local := s.localExecutors[target] != nil
		s.mu.Unlock()
		if !local && (routed || s.nodeConns.get(target) != nil) {
			return false, errors.New("peer route has no enrolled quorum executor")
		}
		return false, nil // Standalone ShellMCP polls the admitting Hub's queue.
	}
	if peerID == s.quorumNode.Status().NodeID {
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
