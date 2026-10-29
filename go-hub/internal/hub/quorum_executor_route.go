package hub

import (
	"context"
	"errors"
	"strings"
)

// A quorum member executor polls the leader-owned durable queue using the
// mutually authenticated executor admission path. Do not forward the command
// to a follower Hub: its quorum admission would send it back to the leader.
// The task remains bound to its enrolled executor target, and its never-sent
// input is encrypted and replicated before the queue is published.
func (s *Server) quorumExecutorUsesLeaderQueue(ctx context.Context, target string) (bool, error) {
	if s.quorumNode == nil || s.quorumPrivateTLS == nil || !strings.HasPrefix(target, "shell:") {
		return false, nil
	}
	s.membershipMu.Lock()
	peerID := ""
	if s.membership != nil {
		for id, member := range s.membership.Nodes {
			if member == nil || member.NodeID != id || member.Name != target {
				continue
			}
			if _, ok := s.quorumPrivateTLS.peers[id]; !ok {
				continue
			}
			if !containsString(member.Capabilities, "executor") || (member.State != nodeStateReady && member.State != nodeStateOffline) {
				s.membershipMu.Unlock()
				return false, errors.New("quorum executor is not available for new work")
			}
			if peerID != "" {
				s.membershipMu.Unlock()
				return false, errors.New("multiple quorum executors own the same target")
			}
			peerID = id
		}
	}
	s.membershipMu.Unlock()
	if peerID == "" || peerID == s.quorumNode.Status().NodeID {
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
