package hub

import (
	"errors"
	"math"
)

// currentWriterControlBundle takes a coherent generation before replying to a
// member. Authority stores can change after the last export; reusing the old
// revision for changed bytes makes readers correctly reject all later updates.
// This endpoint bypasses authSnapshotGate, so it owns the exclusive gate.
func (s *Server) currentWriterControlBundle() (authSnapshot, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authWriteErrorLocked(); err != nil {
		return authSnapshot{}, err
	}
	bundle, err := s.readAuthSlot(s.authState)
	if err != nil {
		return authSnapshot{}, err
	}
	if bundle.WriterID != s.authLocalID || bundle.Generation == 0 {
		return authSnapshot{}, errors.New("invalid writer auth identity")
	}
	if authSnapshotDigest(bundle) != s.authState.Digest {
		if bundle.Generation == math.MaxUint64 {
			return authSnapshot{}, errors.New("auth generation exhausted")
		}
		bundle.Generation++
		next := s.authState
		next.Generation = bundle.Generation
		next.Digest = authSnapshotDigest(bundle)
		// Replicate the same sanitized state before publishing its revision.
		// In non-quorum mode this is a no-op.
		replica := bundle
		replica.Tokens = &managedMCPTokenState{Tokens: cloneAccessRecords(bundle.Tokens.Tokens)}
		s.excludeSnapshotControlCredentials(&replica)
		if err := s.validateAuthSnapshot(replica); err != nil {
			return authSnapshot{}, err
		}
		if err := s.commitQuorumAuthorityState("auth", "bundle", replica); err != nil {
			return authSnapshot{}, err
		}
		if err := writeAccessStateJSON(s.authStatePath(), next, 4096); err != nil {
			return authSnapshot{}, err
		}
		s.authState = next
	}
	s.excludeSnapshotControlCredentials(&bundle)
	return bundle, nil
}
