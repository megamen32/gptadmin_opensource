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

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
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
	term := node.CurrentTerm()
	if err := node.VerifyLeader(ctx); err != nil {
		s.quorumAuthorityActiveWriter.Store(false)
		return err
	}
	return s.materializeVerifiedQuorumAuthority(ctx, term)
}

// The caller must have just verified majority contact, starting in this term.
func (s *Server) materializeVerifiedQuorumAuthority(ctx context.Context, verifiedTerm uint64) error {
	node := s.quorumNode
	s.quorumAuthorityLoadMu.Lock()
	defer s.quorumAuthorityLoadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	status := node.Status()
	term := node.CurrentTerm()
	if !status.Leader || verifiedTerm == 0 || term != verifiedTerm {
		s.quorumAuthorityActiveWriter.Store(false)
		return errors.New("quorum leadership changed before materialization")
	}
	if s.quorumAuthorityActiveWriter.Load() && s.quorumAuthorityTerm.Load() == term && s.quorumAuthorityLoadedLeader == status.NodeID {
		var revision uint64
		if err := s.withQuorumReadDatabase(func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT authority_revision FROM task_quorum_meta WHERE id=1`).Scan(&revision)
		}); err == nil && revision == s.quorumAuthorityLoadedRevision {
			return nil
		}
	}

	// Leadership can precede local application of the previous term's log.
	// ReadRecord includes a Raft barrier before returning the baseline.
	marker, found, err := node.ReadRecord(ctx, "cluster_meta", "task_baseline")
	if err != nil || !found || marker.Deleted {
		s.quorumAuthorityActiveWriter.Store(false)
		if err != nil {
			return err
		}
		return errors.New("quorum task and authority baseline is not ready")
	}
	// Drain admitted auth mutations before reading the image. Other domain
	// owners may hold locks needed for projection, so their proposals fail
	// promptly while promotion holds the write gate.
	s.authMu.Lock()
	defer s.authMu.Unlock()
	s.quorumAuthorityWrites.Lock()
	defer s.quorumAuthorityWrites.Unlock()
	s.quorumAuthorityActiveWriter.Store(false)
	s.quorumAuthorityLoadedLeader = ""

	image, err := s.readQuorumAuthorityImage()
	if err != nil {
		return err
	}
	if image.AppliedIndex != node.ExternalAppliedIndex() {
		return errors.New("quorum authority snapshot is behind the committed task state")
	}
	s.quorumAuthorityTerm.Store(term)
	if err := s.materializeQuorumAuthority(image.Values); err != nil {
		return err
	}
	revision, err := s.persistQuorumPromotionIdentity(image.AuthorityRevision)
	if err != nil {
		return err
	}
	if s.quorumControlOnly() && s.cfg.LegacyMemberControlSource != "" && s.authLocalID == s.cfg.LegacyMemberControlSource {
		s.membershipMu.Lock()
		pendingPin := s.membership == nil || s.membership.ControlSourceID == ""
		s.membershipMu.Unlock()
		if pendingPin {
			// This writer just took over from another physical voter. Its
			// ordinary promotion has committed both authority projections.
			// Keep admission closed until the next majority-verified pass reads
			// those records back and commits the configured historical source.
			return errors.New("original writer promoted; waiting for committed control source migration")
		}
	}
	if err := node.VerifyLeader(ctx); err != nil {
		return err
	}
	if node.CurrentTerm() != term {
		return errors.New("quorum leadership changed while materializing authority")
	}
	if err := s.withQuorumReadDatabase(func(tx *sql.Tx) error { return checkQuorumAuthorityRevision(tx, revision) }); err != nil {
		return err
	}
	// Cache only the image we installed plus our committed identity changes.
	// A later replicated revision must pass through materialization itself.
	s.quorumAuthorityLoadedRevision = revision
	s.quorumAuthorityLoadedLeader = status.NodeID
	s.quorumAuthorityActiveWriter.Store(true)
	return nil
}

func (s *Server) effectiveAuthMode() string {
	if s.quorumNode == nil {
		return s.cfg.AuthMode
	}
	status := s.quorumNode.Status()
	if status.Leader && s.quorumAuthorityActiveWriter.Load() && s.quorumAuthorityTerm.Load() == s.quorumNode.CurrentTerm() {
		return "writer"
	}
	return "reader"
}

// The caller owns the auth and authority write gates. Keep each domain within
// its existing Raft entry budget. Admission opens only after both identities
// have committed against the installed image and its own revision advances.
func (s *Server) persistQuorumPromotionIdentity(expected uint64) (uint64, error) {
	s.mu.Lock()
	if !s.authInitialized {
		s.mu.Unlock()
		return 0, errors.New("auth continuity is not initialized")
	}
	bundle, err := s.readAuthSlot(s.authState)
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	// A domain handler may already have edited its candidate in memory. The
	// local file holds the projection which completed under the write gate.
	membership, err := loadNodeMembershipState(s.membershipStatePath())
	if err != nil {
		return 0, err
	}
	if membership.WriterID == "" {
		return 0, errors.New("promoted membership is unavailable")
	}
	revision := expected
	commit := func(domain, key string, value any) error {
		return s.withQuorumDatabase(func(t *taskTransaction) error {
			if err := checkQuorumAuthorityRevision(t.tx, revision); err != nil {
				return err
			}
			if s.quorumNode.CurrentTerm() != s.quorumAuthorityTerm.Load() {
				return errors.New("quorum leadership changed while materializing authority")
			}
			if err := s.stageQuorumAuthorityState(t, domain, key, value); err != nil {
				return err
			}
			revision += uint64(t.quorumAuthorityChanges)
			return nil
		})
	}
	if err := commit("auth", "bundle", bundle); err != nil {
		return 0, err
	}
	err = commit("membership", "writer", membershipQuorumAuthorityState(membership))
	return revision, err
}

func checkQuorumAuthorityRevision(tx *sql.Tx, expected uint64) error {
	var current uint64
	if err := tx.QueryRow(`SELECT authority_revision FROM task_quorum_meta WHERE id=1`).Scan(&current); err != nil {
		return err
	}
	if current != expected {
		return errors.New("quorum authority changed during materialization")
	}
	return nil
}

func (s *Server) readQuorumAuthorityImage() (quorumAuthorityImage, error) {
	return s.readQuorumAuthorityImageFor(s.requiredQuorumAuthorities())
}

func (s *Server) readQuorumAuthorityImageFor(domains map[string]string) (quorumAuthorityImage, error) {
	image := quorumAuthorityImage{Values: make(map[string]json.RawMessage, len(domains))}
	var clusterID string
	var baselineReady, authorityReady int
	rows := make(map[string]quorumAuthorityRow, len(domains))
	err := s.withQuorumReadDatabase(func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT cluster_id,applied_index,authority_revision,baseline_ready,authority_ready FROM task_quorum_meta WHERE id=1`).Scan(&clusterID, &image.AppliedIndex, &image.AuthorityRevision, &baselineReady, &authorityReady); err != nil {
			return err
		}
		if clusterID != s.quorumClusterID || baselineReady != 1 || authorityReady != 1 {
			return errors.New("quorum task and authority baseline is not ready")
		}
		for domain, key := range domains {
			name := domain + "/" + key
			row, err := readQuorumAuthorityRow(tx, domain, key)
			if err != nil {
				return fmt.Errorf("read committed authority %s: %w", name, err)
			}
			rows[name] = row
		}
		return nil
	})
	if err != nil {
		return quorumAuthorityImage{}, err
	}
	for name, row := range rows {
		opened, err := s.quorumNode.OpenExternalValue(row.namespace, row.key, uint64(row.revision), row.payload)
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
	return s.materializeAuthorityValues(values, s.promoteQuorumMembership)
}

func (s *Server) materializeInstructionSets(values map[string]json.RawMessage) error {
	instructionSetsProcessMu.Lock()
	defer instructionSetsProcessMu.Unlock()
	s.instructionWriteMu.Lock()
	defer s.instructionWriteMu.Unlock()
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
	return nil
}

// materializeAuthorityValues shares domain validation with the offline role transfer.
// The caller owns the auth generation gate and provides an explicit membership transition.
func (s *Server) materializeAuthorityValues(values map[string]json.RawMessage, promote func(*nodeMembershipState) error) error {
	if err := s.materializeInstructionSets(values); err != nil {
		return err
	}
	registryKey := "registry/current"
	if s.quorumControlOnly() {
		registryKey = "registry/enrollment"
	}
	var registry persistentRegistryState
	if err := json.Unmarshal(values[registryKey], &registry); err != nil {
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
	if err := writeVirtualMCPState(s.virtualMCPStatePath(), virtual); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("materialize virtual MCP state: %w", err)
	}
	s.mu.Unlock()

	if !s.quorumControlOnly() {
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

	}
	var security securitySettings
	if err := json.Unmarshal(values["security/settings"], &security); err != nil {
		return fmt.Errorf("decode security state: %w", err)
	}
	s.mu.Lock()
	if err := saveSecuritySettings(s.securityPath, security, s.securityKey()); err != nil {
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
	if err := saveWebAuthnState(s.webauthnPath, webauthn); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("materialize WebAuthn state: %w", err)
	}
	s.webauthnState = webauthn
	s.mu.Unlock()

	if !s.quorumControlOnly() {
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

	}
	var authBundle authSnapshot
	if err := json.Unmarshal(values["auth/bundle"], &authBundle); err != nil {
		return fmt.Errorf("decode auth bundle: %w", err)
	}
	var membership *nodeMembershipState
	if err := json.Unmarshal(values["membership/writer"], &membership); err != nil {
		return fmt.Errorf("decode membership state: %w", err)
	}
	if s.quorumControlOnly() && s.quorumNode != nil {
		if err := s.pinLegacyMemberControlSource(membership, authBundle.WriterID); err != nil {
			return err
		}
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
	// The caller holds authMu exclusively across reading and applying the image.
	s.mu.Lock()
	if err := s.validateAuthSnapshot(authBundle); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.writeAuthSnapshotLocked(authBundle, false); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("materialize auth state: %w", err)
	}
	s.mu.Unlock()

	if err := promote(membership); err != nil {
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
	if s.quorumControlOnly() {
		local, err := restoreRegistryEnrollments(path, state)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		state = local
	}
	localTokens := make(map[string]string, len(s.localExecutorTokens))
	for id, token := range s.localExecutorTokens {
		if s.quorumControlOnly() && len(s.localExecutors[id]) == 0 && firstString(state.Agents[id].Meta, shellTokenHashMetaKey) != shellTokenHash(token) {
			continue
		}
		localTokens[id] = token
	}
	if state.Agents == nil {
		state.Agents = make(map[string]Agent)
	}
	for id, identity := range s.localExecutors {
		// Pending self-enrollment also uses localExecutors with an empty
		// identity; it must never become an approved pinned executor here.
		if identity["server_id"] == "" || identity["public_key"] == "" || identity["fingerprint"] == "" {
			continue
		}
		if committed, exists := state.Agents[id]; exists {
			if committed.Status == "disabled" {
				continue
			}
			if approved, recorded := committed.Meta["approved"]; recorded && !truthy(approved) {
				continue
			}
			conflicting := false
			for key, value := range identity {
				raw, recorded := committed.Meta[key]
				old, valid := raw.(string)
				if recorded && (!valid || (old != "" && old != value)) {
					conflicting = true
					break
				}
			}
			if conflicting {
				continue
			}
		}
		local := state.Agents[id]
		meta := make(map[string]any, len(local.Meta)+len(identity)+2)
		for key, value := range local.Meta {
			meta[key] = value
		}
		meta["approved"], meta["local_executor"] = true, true
		for key, value := range identity {
			meta[key] = value
		}
		local.AgentID = id
		if local.Name == "" {
			local.Name = "Shell: " + strings.TrimPrefix(id, "shell:")
		}
		local.Kind, local.Transport, local.Status = "virtual_shell", "long_poll", "online"
		local.LastSeen, local.OfflineSince, local.StaleSince = nowFloat(), 0, 0
		if len(local.Capabilities) == 0 {
			local.Capabilities = []string{"shell", "system", "tasks", "logs"}
		}
		local.Meta = meta
		state.Agents[id] = local
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

// A configured historical source is a migration intent, not an authority
// grant. Resolve only a unique, already enrolled voter whose credential hash
// agrees with the committed membership. Local IDs may differ from Raft IDs.
func (s *Server) legacyControlSourcePeer(state *nodeMembershipState, pin string) (quorum.Peer, error) {
	var found quorum.Peer
	if state == nil || pin == "" {
		return found, errors.New("historical control source is not configured")
	}
	for _, peer := range s.quorumPeers {
		member := state.Nodes[peer.ID]
		if member == nil || member.NodeID != peer.ID || (member.NodeID != pin && member.LocalID != pin) || peer.NodeKeyHash == "" || peer.NodeKeyHash != member.KeyHash {
			continue
		}
		if found.ID != "" {
			return quorum.Peer{}, errors.New("historical control source matches multiple voter enrollments")
		}
		found = peer
	}
	if found.ID == "" {
		return found, errors.New("historical control source has no verified voter enrollment")
	}
	return found, nil
}

// A legacy control baseline does not identify its original authority. Only an
// operator-specified historical pin corroborated by the existing committed
// auth and membership images may initialize it. Requiring that same writer
// during this one transition also keeps the two-domain promotion retryable.
func (s *Server) pinLegacyMemberControlSource(state *nodeMembershipState, authWriter string) error {
	pin := s.cfg.LegacyMemberControlSource
	if pin == "" {
		return nil
	}
	if state == nil {
		return errors.New("committed membership is unavailable for control source migration")
	}
	if state.ControlSourceID != "" {
		if state.ControlSourceID != pin {
			return errors.New("legacy member control source conflicts with the committed source")
		}
		return nil
	}
	if s.authLocalID != pin {
		// All voters may carry the same explicit historical intent. Only its
		// verified historical writer may actually commit the logical ID.
		// A different leader may coordinate the native transfer, never pin.
		_, err := s.legacyControlSourcePeer(state, pin)
		if err != nil {
			return err
		}
		previous := state.Nodes[state.WriterID]
		if previous == nil || authWriter == "" || (previous.NodeID != authWriter && previous.LocalID != authWriter) {
			return errors.New("prior committed auth and membership writers disagree")
		}
		return nil
	}
	localID := s.quorumNode.Status().NodeID
	member := state.Nodes[localID]
	if member == nil || member.NodeID != localID || (member.NodeID != pin && member.LocalID != pin) {
		return errors.New("legacy control source differs from the committed voter enrollment")
	}
	for _, peer := range s.quorumPeers {
		if peer.ID != localID || peer.NodeKeyHash == "" || peer.NodeKeyHash != member.KeyHash {
			continue
		}
		if state.WriterID != localID {
			previous := state.Nodes[state.WriterID]
			if previous == nil || previous.NodeID != state.WriterID || authWriter == "" ||
				(previous.NodeID != authWriter && previous.LocalID != authWriter) {
				return errors.New("prior committed auth and membership writers disagree")
			}
			// Use the regular promotion first. Its committed auth and membership
			// will be read back before this process opens admission.
			return nil
		}
		if authWriter != pin {
			return errors.New("committed auth writer does not corroborate the legacy control source")
		}
		state.ControlSourceID = pin
		return nil
	}
	return errors.New("legacy control source is not the enrolled local quorum voter")
}

func (s *Server) promoteQuorumMembership(state *nodeMembershipState) error {
	if state == nil || state.Nodes == nil {
		return errors.New("committed membership state is incomplete")
	}
	if state.Grants == nil { // An empty grant map is omitted from persisted membership.
		state.Grants = make(map[string]*nodeJoinGrant)
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
	if state.Revision == 0 || state.WriterID == "" {
		return errors.New("authoritative membership epoch is invalid")
	}
	// Legacy membership stores predate epochs and start at zero. Give the
	// already-authoritative writer its first epoch before recording a change.
	if state.Epoch == 0 {
		if state.Revision == ^uint64(0) {
			return errors.New("authoritative membership revision overflow")
		}
		state.Epoch = 1
		state.Revision++
	}
	if state.WriterID != writerID {
		if state.Epoch == ^uint64(0) || state.Revision == ^uint64(0) {
			return errors.New("authoritative membership epoch or revision overflow")
		}
		state.WriterID = writerID
		state.Epoch++
		state.Revision++
	}
	if state.Epoch == 0 || state.Revision == 0 {
		return errors.New("authoritative membership epoch is invalid")
	}
	// Existing members persist WriterURL for heartbeats and WebSockets. They
	// must keep dialing the cluster origin when this physical leader is lost.
	state.WriterURL = firstNonEmpty(s.cfg.PublicOrigin, publicURL)
	member.Endpoints = []string{publicURL}
	member.State = nodeStateReady
	member.LastSeen = s.now()
	member.LeaseExpires = member.LastSeen.Add(nodeLeaseTTL)
	member.LocalID = s.authLocalID
	member.Build = GitCommit
	s.membershipMu.Lock()
	// Heartbeat leases are deliberately absent from the replicated authority
	// image. Retain only leases observed by this same writer in this epoch,
	// and only while the enrolled identity and generation are unchanged.
	if current := s.membership; current != nil && current.WriterID == writerID && current.Epoch == state.Epoch {
		for id, incoming := range state.Nodes {
			previous := current.Nodes[id]
			if incoming == nil || previous == nil || (incoming.State != nodeStateReady && incoming.State != nodeStateOffline) ||
				previous.State != nodeStateReady || !previous.LeaseExpires.After(s.now()) ||
				incoming.KeyHash == "" || incoming.KeyHash != previous.KeyHash || incoming.Generation != previous.Generation {
				continue
			}
			incoming.State = previous.State
			incoming.LastSeen = previous.LastSeen
			incoming.LeaseExpires = previous.LeaseExpires
			incoming.Build = previous.Build
			incoming.SnapshotRev = previous.SnapshotRev
		}
	}
	err = s.writeMembershipStateLocked(state)
	if err == nil {
		s.membership = state
	}
	s.membershipMu.Unlock()
	if err != nil {
		return fmt.Errorf("persist promoted membership: %w", err)
	}
	s.rebuildMembershipRoutes()
	return nil
}
