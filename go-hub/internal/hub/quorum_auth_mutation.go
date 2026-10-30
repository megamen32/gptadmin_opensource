package hub

import (
	"errors"
	"log"
	"math"
)

// quorumPrepareAuthMutationLocked commits the candidate authority image before
// its existing local store is written. The caller owns s.mu and the store lock.
// It returns a rollback for the rare local-write failure after quorum commit.
func (s *Server) quorumPrepareAuthMutationLocked(tokens *managedMCPTokenState, clients *oauthClientsState, profiles *accessProfilesState) (func() error, error) {
	if s.quorumNode == nil || s.cfg.QuorumConfigFile == "" {
		return nil, nil
	}
	if s.taskQuorumApplying.Load() {
		return nil, errors.New("quorum authority is being materialized")
	}
	if err := s.authWriteErrorLocked(); err != nil {
		return nil, err
	}
	before, err := s.readAuthSlot(s.authState)
	if err != nil {
		return nil, err
	}
	if before.WriterID == "" || before.WriterID != s.authLocalID || before.Generation == 0 || s.authState.ReadOnly {
		return nil, errors.New("quorum auth writer identity is not ready")
	}
	if before.Generation == math.MaxUint64 {
		return nil, errors.New("auth generation exhausted")
	}
	before.Tokens = &managedMCPTokenState{Tokens: cloneAccessRecords(before.Tokens.Tokens)}
	s.excludeSnapshotControlCredentials(&before)
	after := before
	if tokens != nil {
		after.Tokens = &managedMCPTokenState{Tokens: cloneAccessRecords(tokens.Tokens)}
		s.excludeSnapshotControlCredentials(&after)
	}
	if clients != nil {
		after.Clients = clients
	}
	if profiles != nil {
		after.Profiles = profiles
	}
	after.Generation++
	if err := s.validateAuthSnapshot(after); err != nil {
		return nil, err
	}
	if err := s.commitQuorumAuthorityState("auth", "bundle", after); err != nil {
		return nil, err
	}
	return func() error { return s.commitQuorumAuthorityState("auth", "bundle", before) }, nil
}

func (s *Server) quorumRollbackAuthMutationLocked(rollback func() error) {
	if rollback == nil {
		return
	}
	if err := rollback(); err != nil {
		log.Printf("quorum auth rollback failed: %v", err)
	}
	// A failed atomic-write operation may have renamed the local file before
	// failing its fsync. Fence this node even when quorum rollback succeeded;
	// materialization must reconcile the complete image before more admission.
	s.quorumAuthorityActiveWriter.Store(false)
	s.authState.Pending = true
	if err := writeAccessStateJSON(s.authStatePath(), s.authState, 4096); err != nil {
		log.Printf("quorum auth fence persistence failed: %v", err)
	}
}
