package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
)

type quorumAuthorityImage struct {
	AuthorityRevision uint64
	AppliedIndex      uint64
	Values            map[string]json.RawMessage
}

// ensureQuorumAuthorityMaterialized loads the majority-committed authority
// snapshot into this process before any Hub request or writer maintenance can
// run. It is safe to retry after a partial local file write.
func (s *Server) ensureQuorumAuthorityMaterialized(ctx context.Context) error {
	node := s.quorumNode
	if node == nil {
		return nil
	}
	if err := node.VerifyLeader(ctx); err != nil {
		s.quorumAuthorityActiveWriter.Store(false)
		return err
	}
	status := node.Status()
	s.quorumAuthorityActiveWriter.Store(false)
	s.quorumAuthorityLoadMu.Lock()
	defer s.quorumAuthorityLoadMu.Unlock()
	if s.quorumAuthorityLoadedLeader == status.NodeID {
		var loadedRevision uint64
		if err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT authority_revision FROM task_quorum_meta WHERE id=1`).Scan(&loadedRevision)
		}); err == nil && loadedRevision == s.quorumAuthorityLoadedRevision {
			s.quorumAuthorityActiveWriter.Store(true)
			return nil
		}
	}

	image, err := s.readQuorumAuthorityImage()
	if err != nil {
		return err
	}
	if image.AppliedIndex != node.ExternalAppliedIndex() {
		return errors.New("quorum authority snapshot is behind the committed task state")
	}
	decoded := make(map[string]json.RawMessage, len(image.Values))
	for domain, key := range quorumRequiredAuthorityRecords {
		name := domain + "/" + key
		payload, ok := image.Values[name]
		if !ok {
			return fmt.Errorf("quorum authority snapshot is missing %s", name)
		}
		// The row revision is stored in the envelope alongside the payload.
		// readQuorumAuthorityImage already decrypted and authenticated it.
		decoded[domain] = payload
	}

	previousApplying := s.taskQuorumApplying.Swap(true)
	applyErr := s.materializeQuorumAuthority(decoded)
	s.taskQuorumApplying.Store(previousApplying)
	if applyErr != nil {
		return applyErr
	}
	if err := node.VerifyLeader(ctx); err != nil {
		return err
	}
	if err := s.persistQuorumPromotionIdentity(); err != nil {
		return err
	}
	if err := node.VerifyLeader(ctx); err != nil {
		return err
	}
	var finalRevision uint64
	if err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT authority_revision FROM task_quorum_meta WHERE id=1`).Scan(&finalRevision)
	}); err != nil {
		return err
	}
	if node.ExternalAppliedIndex() == 0 || !node.Status().Leader {
		return errors.New("quorum leadership changed while authority state was materialized")
	}
	s.quorumAuthorityLoadedRevision = finalRevision
	s.quorumAuthorityLoadedLeader = node.Status().NodeID
	s.quorumAuthorityActiveWriter.Store(true)
	return nil
}

func (s *Server) effectiveAuthMode() string {
	if s.quorumNode == nil {
		return s.cfg.AuthMode
	}
	status := s.quorumNode.Status()
	if status.Leader && s.quorumAuthorityActiveWriter.Load() {
		return "writer"
	}
	return "reader"
}

func (s *Server) persistQuorumPromotionIdentity() error {
	s.authMu.RLock()
	s.mu.Lock()
	if !s.authInitialized {
		s.mu.Unlock()
		s.authMu.RUnlock()
		return errors.New("auth continuity is not initialized")
	}
	bundle, err := s.readAuthSlot(s.authState)
	s.mu.Unlock()
	s.authMu.RUnlock()
	if err != nil {
		return err
	}
	if err := s.commitQuorumAuthorityState("auth", "bundle", bundle); err != nil {
		return err
	}
	s.membershipMu.Lock()
	if s.membership == nil {
		s.membershipMu.Unlock()
		return errors.New("promoted membership is unavailable")
	}
	err = s.persistMembershipStateLocked(s.membership)
	s.membershipMu.Unlock()
	if err != nil {
		return err
	}
	s.rebuildMembershipRoutes()
	return nil
}

func (s *Server) readQuorumAuthorityImage() (quorumAuthorityImage, error) {
	image := quorumAuthorityImage{Values: make(map[string]json.RawMessage, len(quorumRequiredAuthorityRecords))}
	var clusterID string
	var baselineReady, authorityReady int
	rows := make(map[string]struct {
		payload  []byte
		revision uint64
	}, len(quorumRequiredAuthorityRecords))
	err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT cluster_id,applied_index,authority_revision,baseline_ready,authority_ready FROM task_quorum_meta WHERE id=1`).Scan(&clusterID, &image.AppliedIndex, &image.AuthorityRevision, &baselineReady, &authorityReady); err != nil {
			return err
		}
		if clusterID != s.quorumClusterID || baselineReady != 1 || authorityReady != 1 {
			return errors.New("quorum task and authority baseline is not ready")
		}
		for domain, key := range quorumRequiredAuthorityRecords {
			name := domain + "/" + key
			var row struct {
				payload  []byte
				revision uint64
			}
			if err := tx.QueryRow(`SELECT payload,revision FROM task_quorum_authority WHERE namespace=? AND row_key=?`, "authority/"+domain, key).Scan(&row.payload, &row.revision); err != nil {
				return fmt.Errorf("read committed authority %s: %w", name, err)
			}
			rows[name] = row
		}
		return nil
	})
	if err != nil {
		return quorumAuthorityImage{}, err
	}
	for domain, key := range quorumRequiredAuthorityRecords {
		name := domain + "/" + key
		row := rows[name]
		opened, err := s.quorumNode.OpenExternalValue("authority/"+domain, key, row.revision, row.payload)
		if err != nil {
			return quorumAuthorityImage{}, fmt.Errorf("authenticate committed authority %s: %w", name, err)
		}
		if !json.Valid(opened) {
			return quorumAuthorityImage{}, fmt.Errorf("committed authority %s is not valid JSON", name)
		}
		image.Values[name] = append(json.RawMessage(nil), opened...)
	}
	return image, nil
}

func (s *Server) materializeQuorumAuthority(values map[string]json.RawMessage) error {
	var named instructionSetsState
	if err := json.Unmarshal(values["instruction_sets_named/current"], &named); err != nil {
		return fmt.Errorf("decode named instruction state: %w", err)
	}
	if named.Sets == nil {
		return errors.New("named instruction snapshot is empty")
	}
	for id, set := range named.Sets {
		if err := validateNamedInstructionSetID(id); err != nil || set.ID != id || set.Version != instructionSetVersion(set.Content) {
			return fmt.Errorf("invalid committed instruction set %q", id)
		}
		if err := validateInstructionContent(set.Content); err != nil {
			return err
		}
	}
	if err := writeInstructionSetsStateAtomic(s.instructionSetsStatePath(), named.Sets); err != nil {
		return fmt.Errorf("materialize named instructions: %w", err)
	}
	s.instructionSetsMu.Lock()
	s.instructionSets = cloneInstructionSets(named.Sets)
	s.instructionSetsMu.Unlock()

	var defaultSet InstructionSet
	if err := json.Unmarshal(values["instruction_sets_default/current"], &defaultSet); err != nil {
		return fmt.Errorf("decode default instruction state: %w", err)
	}
	if defaultSet.ID != defaultInstructionSetID || defaultSet.Version != instructionSetVersion(defaultSet.Content) {
		return errors.New("invalid committed default instruction set")
	}
	if err := validateInstructionContent(defaultSet.Content); err != nil {
		return err
	}
	if inline, configured := effectiveInlineStartupInstructions(s.cfg.StartupInstructions); configured {
		if instructionSetVersion(inline) != defaultSet.Version {
			return errors.New("local inline startup instructions conflict with committed quorum authority")
		}
	} else {
		result, err := writeInstructionContentAtomic(s.cfg.StartupInstructionsFile, defaultSet.Content)
		if err != nil && !result.Committed {
			return fmt.Errorf("materialize default instructions: %w", err)
		}
		if result.UpdatedAt != nil {
			defaultSet.UpdatedAt = result.UpdatedAt
		}
	}
	s.instructionMu.Lock()
	s.instructionSet = cloneInstructionSet(defaultSet)
	s.instructionMu.Unlock()

	var registry persistentRegistryState
	if err := json.Unmarshal(values["registry/current"], &registry); err != nil {
		return fmt.Errorf("decode committed registry state: %w", err)
	}
	if err := s.materializeQuorumRegistry(registry); err != nil {
		return err
	}

	var virtual virtualMCPState
	if err := json.Unmarshal(values["virtual_mcp/current"], &virtual); err != nil {
		return fmt.Errorf("decode virtual MCP state: %w", err)
	}
	s.mu.Lock()
	s.virtualMCP = map[string]bool{}
	for id, enabled := range virtual.Enabled {
		if _, ok := virtualMCPDefinitions[id]; !ok && enabled {
			s.mu.Unlock()
			return fmt.Errorf("unknown committed virtual MCP %q", id)
		}
		s.virtualMCP[id] = enabled
	}
	if err := s.saveVirtualMCPStateLocked(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("materialize virtual MCP state: %w", err)
	}
	s.mu.Unlock()

	var routes map[string]WebhookRoute
	if err := json.Unmarshal(values["webhook_routes/current"], &routes); err != nil {
		return fmt.Errorf("decode webhook routes: %w", err)
	}
	if err := s.materializeQuorumWebhookRoutes(routes); err != nil {
		return err
	}
	var webhookState persistedWebhookState
	if err := json.Unmarshal(values["webhooks/current"], &webhookState); err != nil {
		return fmt.Errorf("decode webhook receipts: %w", err)
	}
	if err := s.materializeQuorumWebhooks(webhookState); err != nil {
		return err
	}

	var security securitySettings
	if err := json.Unmarshal(values["security/settings"], &security); err != nil {
		return fmt.Errorf("decode security state: %w", err)
	}
	s.mu.Lock()
	if err := s.persistSecurity(security); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("materialize security state: %w", err)
	}
	s.security = security
	s.mu.Unlock()

	var webauthn webAuthnState
	if err := json.Unmarshal(values["webauthn/credentials"], &webauthn); err != nil {
		return fmt.Errorf("decode WebAuthn state: %w", err)
	}
	s.mu.Lock()
	if err := s.saveWebAuthnState(webauthn); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("materialize WebAuthn state: %w", err)
	}
	s.webauthnState = webauthn
	s.mu.Unlock()

	var secretState quorumSecretIngressState
	if err := json.Unmarshal(values["secret_ingress/current"], &secretState); err != nil {
		return fmt.Errorf("decode secret ingress state: %w", err)
	}
	if s.secretStore == nil {
		return errors.New("local secret store is unavailable")
	}
	if err := s.secretStore.restoreQuorumAuthorityState(secretState); err != nil {
		return fmt.Errorf("materialize secret ingress state: %w", err)
	}

	var authBundle authSnapshot
	if err := json.Unmarshal(values["auth/bundle"], &authBundle); err != nil {
		return fmt.Errorf("decode auth bundle: %w", err)
	}
	if s.authLocalID == "" {
		return errors.New("local auth identity is unavailable")
	}
	if authBundle.WriterID != s.authLocalID || s.authState.ReadOnly || s.authState.Digest != authSnapshotDigest(authBundle) {
		if authBundle.Generation < s.authState.Generation {
			authBundle.Generation = s.authState.Generation
		}
		if authBundle.Generation == math.MaxUint64 {
			return errors.New("auth generation is exhausted")
		}
		authBundle.Generation++
		authBundle.WriterID = s.authLocalID
	}
	s.authMu.Lock()
	s.mu.Lock()
	if err := s.commitAuthSnapshotLocked(authBundle, false); err != nil {
		s.mu.Unlock()
		s.authMu.Unlock()
		return fmt.Errorf("materialize auth state: %w", err)
	}
	s.mu.Unlock()
	s.authMu.Unlock()

	var membership *nodeMembershipState
	if err := json.Unmarshal(values["membership/writer"], &membership); err != nil {
		return fmt.Errorf("decode membership state: %w", err)
	}
	if err := s.promoteQuorumMembership(membership); err != nil {
		return err
	}
	return nil
}

func (s *Server) materializeQuorumRegistry(state persistentRegistryState) error {
	path := s.registryStatePath()
	if path == "" {
		return errors.New("registry state path is unavailable")
	}
	s.mu.Lock()
	localTokens := make(map[string]string, len(s.localExecutorTokens))
	for id, token := range s.localExecutorTokens {
		localTokens[id] = token
	}
	state.LocalExecutorTokens = localTokens
	err := withStateFileLock(path, func() error { return writeAccessStateJSON(path, state, 16<<20) })
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("materialize registry state file: %w", err)
	}
	s.agents = map[string]*Agent{}
	s.relayCredentials = map[string]string{}
	s.relayEnrollments = map[string]relayEnrollment{}
	s.localExecutorTokens = localTokens
	s.settings = defaultHubSettings()
	s.settingsRevision = 0
	s.settingsHistory = nil
	s.agentPolicies = map[string]agentCleanupPolicy{}
	s.tombstones = nil
	if err := s.loadRegistryState(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("load materialized registry state: %w", err)
	}
	s.mu.Unlock()
	return nil
}

func (s *Server) materializeQuorumWebhookRoutes(routes map[string]WebhookRoute) error {
	items := make([]WebhookRoute, 0, len(routes))
	for id, route := range routes {
		if id == "" || route.ID != id {
			return errors.New("invalid committed webhook route identity")
		}
		items = append(items, route)
	}
	if err := validateWebhookRoutes(items); err != nil {
		return fmt.Errorf("invalid committed webhook routes: %w", err)
	}
	s.mu.Lock()
	s.webhookRoutes = webhookRouteMap(items)
	s.mu.Unlock()
	return nil
}

func (s *Server) materializeQuorumWebhooks(state persistedWebhookState) error {
	path := s.webhookStatePath()
	if path == "" {
		return errors.New("webhook state path is unavailable")
	}
	s.mu.Lock()
	s.webhookStateWriteMu.Lock()
	if err := writeAccessStateJSON(path, state, 16<<20); err != nil {
		s.webhookStateWriteMu.Unlock()
		s.mu.Unlock()
		return fmt.Errorf("write committed webhook state: %w", err)
	}
	s.webhookJobs = map[string]*webhookJob{}
	s.webhookDeliveries = map[string]*webhookDelivery{}
	err := s.loadWebhookState()
	s.webhookStateWriteMu.Unlock()
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("load committed webhook state: %w", err)
	}
	return nil
}

func (s *Server) promoteQuorumMembership(state *nodeMembershipState) error {
	if state == nil || state.Nodes == nil || state.Grants == nil {
		return errors.New("committed membership state is incomplete")
	}
	writerID := s.authLocalID
	if state.Nodes[writerID] == nil {
		writerID = s.quorumNode.Status().NodeID
	}
	member := state.Nodes[writerID]
	if member == nil {
		return errors.New("this quorum voter is not enrolled in authoritative membership")
	}
	publicURL := strings.TrimSpace(s.cfg.NodePublicURL)
	if publicURL == "" && len(member.Endpoints) > 0 {
		publicURL = strings.TrimSpace(member.Endpoints[0])
	}
	parsed, err := url.Parse(publicURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return errors.New("this quorum voter has no valid public Hub URL")
	}
	if state.WriterID != writerID {
		state.WriterID = writerID
		state.Epoch++
		state.Revision++
	}
	if state.Epoch == 0 || state.Revision == 0 {
		return errors.New("authoritative membership epoch is invalid")
	}
	state.WriterURL = publicURL
	member.Endpoints = []string{publicURL}
	member.State = nodeStateReady
	member.LastSeen = s.now()
	member.LeaseExpires = member.LastSeen.Add(nodeLeaseTTL)
	member.LocalID = s.authLocalID
	member.Build = GitCommit
	s.membershipMu.Lock()
	s.membership = state
	err = s.persistMembershipStateLocked(state)
	s.membershipMu.Unlock()
	if err != nil {
		return fmt.Errorf("persist promoted membership: %w", err)
	}
	s.rebuildMembershipRoutes()
	return nil
}
