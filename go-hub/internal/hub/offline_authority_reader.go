package hub

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type AuthorityReaderPrepareOptions struct {
	TransitionFile               string
	TransitionDigest             string
	Target                       string
	ExpectedLocalID              string
	OutputDir                    string
	EnvironmentFile              string
	IdentityDerivedEntrypoint    string
	EntrypointDigest             string
	IdentityDerivedSeedAlias     string
	IdentityDerivedOptionsFile   string
	IdentityDerivedOptionsDigest string
	ListenHost                   string
	PhysicalIdentityFile         string
}

// The environment file is the caller-reviewed last file consumed by the real
// service launcher. Unknown lines are preserved; duplicate assigned keys are
// replaced together. The caller must separately verify launcher precedence.
func handoffStageEnvironment(plan *AuthorityHandoffPlan, stageRoot, path string) error {
	if path == "" {
		return nil
	}
	for key, value := range plan.Environment {
		if !validSecretEnvName.MatchString(key) || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid persistent environment assignment")
		}
	}
	if !filepath.IsAbs(path) {
		return errors.New("environment file must be absolute")
	}
	if err := regularAuthPath(path, false); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("environment file exceeds budget")
	}
	if strings.HasSuffix(path, ".json") {
		var values map[string]string
		if err := json.Unmarshal(raw, &values); err != nil || values == nil {
			return errors.New("canonical JSON environment must be a string map")
		}
		for key, value := range plan.Environment {
			values[key] = value
		}
		encoded, err := json.MarshalIndent(values, "", "  ")
		if err != nil {
			return err
		}
		encoded = append(encoded, '\n')
		rel := "handoff-environment.json"
		if err := os.WriteFile(filepath.Join(stageRoot, rel), encoded, 0600); err != nil {
			return err
		}
		plan.EnvironmentFile = path
		plan.Files = append(plan.Files, AuthorityHandoffFile{Relative: rel, Destination: path, Before: handoffDigest(raw), After: handoffDigest(encoded), Mode: 0600})
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		trim := strings.TrimSpace(line)
		key := trim
		if index := strings.IndexByte(key, '='); index >= 0 {
			key = strings.TrimSpace(key[:index])
			key = strings.TrimPrefix(key, "export ")
		}
		if _, change := plan.Environment[key]; !change {
			lines = append(lines, line)
		}
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	keys := make([]string, 0, len(plan.Environment))
	for key := range plan.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := plan.Environment[key]
		if strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("environment value contains a newline")
		}
		lines = append(lines, key+"="+strconv.Quote(value))
	}
	encoded := []byte(strings.Join(lines, "\n") + "\n")
	rel := "handoff-environment.env"
	if err := os.WriteFile(filepath.Join(stageRoot, rel), encoded, 0600); err != nil {
		return err
	}
	plan.EnvironmentFile = path
	plan.Files = append(plan.Files, AuthorityHandoffFile{Relative: rel, Destination: path, Before: handoffDigest(raw), After: handoffDigest(encoded), Mode: 0600})
	return nil
}

func handoffReadTransition(path, digest string) (AuthorityReaderTransition, error) {
	var transition AuthorityReaderTransition
	actual, err := handoffFileDigest(path)
	if err != nil {
		return transition, err
	}
	if digest == "" || actual != digest {
		return transition, errors.New("reader transition digest mismatch")
	}
	if err := handoffReadJSON(path, &transition, false, authorityHandoffBudget); err != nil {
		return transition, err
	}
	if transition.Schema != authorityHandoffSchema || transition.WriterID == "" || transition.PreviousWriterID == "" || transition.WriterID == transition.PreviousWriterID || transition.Auth.WriterID != transition.WriterID || transition.Membership == nil || transition.Membership.WriterID != transition.WriterID {
		return transition, errors.New("inconsistent reader transition")
	}
	return transition, nil
}

func handoffMemberView(state *nodeMembershipState) (nodeMembershipView, error) {
	var view nodeMembershipView
	raw, digest := membershipSnapshotJSON(state)
	if err := json.Unmarshal(raw, &view); err != nil {
		return view, err
	}
	if view.Schema != nodeMembershipSchema || len(view.Nodes) == 0 {
		return view, errors.New("empty reader membership view")
	}
	for _, node := range view.Nodes {
		if node == nil || node.NodeID == "" {
			return view, errors.New("invalid reader membership node")
		}
		node.KeyHash = ""
		node.CatalogFresh = false
	}
	view.SHA256 = digest
	view.FetchedAt = time.Now().UTC()
	return view, nil
}

// A reviewed HAOS bootstrap alias may remain unchanged only when the pinned
// identity already selects the canonical writer. Native memberWriterURL uses
// that identity before the bootstrap seed. Default preparation remains strict.
func handoffIdentityDerivedSeed(cfg Config, options AuthorityReaderPrepareOptions, identity *nodeIdentity, writerURL string) (string, *AuthorityHandoffGuard, error) {
	alias := options.IdentityDerivedSeedAlias
	if alias == "" {
		if options.IdentityDerivedOptionsFile != "" || options.IdentityDerivedOptionsDigest != "" || cfg.NodeSeedURL != writerURL {
			return "", nil, errors.New("identity-derived startup requires the canonical seed or an explicitly reviewed alias")
		}
		return writerURL, nil, nil
	}
	if options.IdentityDerivedEntrypoint == "" || !strings.HasPrefix(alias, "https://") || handoffValidateURL(alias) != nil || cfg.NodeSeedURL != alias || identity == nil || identity.WriterURL != writerURL {
		return "", nil, errors.New("identity-derived seed alias or canonical identity does not match the reviewed runtime")
	}
	path := options.IdentityDerivedOptionsFile
	if !filepath.IsAbs(path) || options.IdentityDerivedOptionsDigest == "" {
		return "", nil, errors.New("identity-derived seed alias requires exact persistent options")
	}
	digest, err := handoffFileDigest(path)
	if err != nil {
		return "", nil, err
	}
	if digest != options.IdentityDerivedOptionsDigest {
		return "", nil, errors.New("identity-derived options differ from the reviewed seed alias")
	}
	var settings struct {
		Failover struct {
			PrimaryPublicURL string `json:"primary_public_url"`
		} `json:"failover"`
	}
	if err := handoffReadJSON(path, &settings, false, 1<<20); err != nil {
		return "", nil, err
	}
	if settings.Failover.PrimaryPublicURL != alias {
		return "", nil, errors.New("persistent bootstrap seed differs from the reviewed alias")
	}
	return alias, &AuthorityHandoffGuard{Path: path, Digest: digest}, nil
}

// PrepareStoppedAuthorityReader changes the pinned writer coherently. It never
// starts a runtime or opens the node's execution ledger. A reader keeps its
// node ID/key, local executor identity, content, tasks and local secrets.
func PrepareStoppedAuthorityReader(cfg Config, options AuthorityReaderPrepareOptions) (AuthorityHandoffPrepared, error) {
	var result AuthorityHandoffPrepared
	var err error
	cfg, err = handoffConfig(cfg)
	if err != nil {
		return result, err
	}
	lock, err := handoffLock(cfg)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	transition, err := handoffReadTransition(options.TransitionFile, options.TransitionDigest)
	if err != nil {
		return result, err
	}
	if cfg.ReaderOfflineSharedSigner && handoffDigest([]byte(cfg.OAuthClientSecret)) != transition.SignerFingerprint {
		return result, errors.New("explicit offline shared signer differs from transferred authority")
	}
	if _, _, err := handoffLocalExecutor(cfg, options.Target, options.ExpectedLocalID, options.PhysicalIdentityFile); err != nil {
		return result, err
	}
	identity, err := handoffLoadNodeIdentity(cfg, options.Target == transition.SourceTarget)
	if err != nil {
		return result, err
	}
	if identity == nil {
		copyIdentity := transition.SourceIdentity
		identity = &copyIdentity
		if identity.NodeID != transition.PreviousWriterID || options.ExpectedLocalID != transition.PreviousWriterID {
			return result, errors.New("new reader identity is not the former keyless writer")
		}
	}
	member := transition.Membership.Nodes[identity.NodeID]
	if member == nil || member.Name != options.Target || member.LocalID != options.ExpectedLocalID || member.KeyHash != hashNodeSecret(identity.NodeKey) || member.State == nodeStateDisabled {
		return result, errors.New("reader identity does not match retained eligible enrollment")
	}
	if identity.WriterID != "" && identity.WriterID != transition.PreviousWriterID && identity.WriterID != transition.WriterID {
		return result, errors.New("reader has another pinned writer")
	}
	var state authSnapshotState
	if err := handoffReadJSON(filepath.Join(cfg.ConfigDir, "auth-continuity", "state.json"), &state, false, 4096); err != nil {
		return result, err
	}
	if state.Pending || state.WriterID != transition.PreviousWriterID || state.Generation >= transition.Auth.Generation {
		return result, errors.New("reader authority moved or has an incomplete/newer generation")
	}
	if cfg.AuthMode == "reader" && cfg.AuthSourceID != "" && cfg.AuthSourceID != transition.PreviousWriterID {
		return result, errors.New("reader environment pins an unexpected writer")
	}
	if cfg.AuthMode != "reader" && options.Target != transition.SourceTarget {
		return result, errors.New("only the former writer may be demoted")
	}
	seed := transition.WriterURL
	var seedGuard *AuthorityHandoffGuard
	if options.IdentityDerivedEntrypoint != "" {
		seed, seedGuard, err = handoffIdentityDerivedSeed(cfg, options, identity, transition.WriterURL)
		if err != nil {
			return result, err
		}
	} else if options.IdentityDerivedSeedAlias != "" || options.IdentityDerivedOptionsFile != "" || options.IdentityDerivedOptionsDigest != "" {
		return result, errors.New("reviewed seed alias is limited to identity-derived startup")
	}
	if err := handoffPrivateOutput(options.OutputDir); err != nil {
		return result, err
	}
	root := filepath.Join(options.OutputDir, "config")
	if err := os.Mkdir(root, 0700); err != nil {
		return result, err
	}
	stageCfg := handoffProjectConfig(cfg, root)
	stageCfg.AuthMode = "reader"
	stageCfg.AuthSourceID = transition.WriterID
	stageCfg.NodeSeedURL = seed
	stageCfg.NodeWriterConnectTo = ""
	stageCfg.NodePeersJSON = ""
	if options.Target == transition.SourceTarget {
		if len(member.Endpoints) != 1 || handoffValidateURL(member.Endpoints[0]) != nil {
			return result, errors.New("former writer physical endpoint is not explicit")
		}
		stageCfg.NodePublicURL = member.Endpoints[0]
	}
	if err := handoffCopyTree(filepath.Join(cfg.ConfigDir, "auth-continuity"), filepath.Join(root, "auth-continuity")); err != nil {
		return result, err
	}
	sets := transition.Named.Sets
	for id, set := range sets {
		if validateNamedInstructionSetID(id) != nil || set.ID != id || validateInstructionContent(set.Content) != nil || set.Version != instructionSetVersion(set.Content) {
			return result, errors.New("invalid transferred named instruction")
		}
	}
	if err := writeInstructionSetsStateAtomic(stageCfg.InstructionSetsStateFile, sets); err != nil {
		return result, err
	}
	s := &Server{cfg: stageCfg, authState: state, instructionSets: cloneInstructionSets(sets)}
	s.authMu.Lock()
	s.mu.Lock()
	err = s.commitAuthSnapshotLocked(transition.Auth, true)
	s.mu.Unlock()
	s.authMu.Unlock()
	if err != nil {
		return result, err
	}
	identity.WriterID = transition.WriterID
	identity.WriterURL = transition.WriterURL
	if err := saveNodeIdentityFile(stageCfg.NodeIdentityFile, *identity); err != nil {
		return result, err
	}
	view, err := handoffMemberView(transition.Membership)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(filepath.Join(root, "membership"), 0700); err != nil {
		return result, err
	}
	if err := writeAccessStateJSON(filepath.Join(root, "membership", "member-view.json"), view, nodeMembershipStateLimit); err != nil {
		return result, err
	}
	plan := AuthorityHandoffPlan{Schema: authorityHandoffSchema, Role: "reader", ConfigDir: cfg.ConfigDir, Target: options.Target, LocalID: options.ExpectedLocalID, SourceDigest: options.TransitionDigest, Environment: map[string]string{"GPTADMIN_AUTH_MODE": "reader", "GPTADMIN_AUTH_SOURCE_ID": transition.WriterID, "GPTADMIN_NODE_SEED": seed, "GPTADMIN_NODE_WRITER_CONNECT_TO": "", "GPTADMIN_NODE_PEERS": "", "GPTADMIN_QUORUM_CONFIG_FILE": ""}}
	if options.ListenHost != "" {
		if net.ParseIP(options.ListenHost) == nil || options.EnvironmentFile == "" {
			return result, errors.New("listener transition requires an explicit IP and persistent environment file")
		}
		if _, port, err := net.SplitHostPort(cfg.Addr); err != nil || port == "" {
			return result, errors.New("current listener port is not explicit")
		}
		plan.Environment["GPTADMIN_HUB_HOST"] = options.ListenHost
	}
	if options.Target == transition.SourceTarget {
		plan.Environment["GPTADMIN_NODE_PUBLIC_URL"] = stageCfg.NodePublicURL
	}
	paths, err := handoffAuthorityPaths(stageCfg)
	if err != nil {
		return result, err
	}
	namedRel, _ := filepath.Rel(root, stageCfg.InstructionSetsStateFile)
	paths = append(paths, filepath.Join("membership", "member-view.json"))
	for _, rel := range paths {
		if rel != namedRel && rel != filepath.Join("membership", "member-view.json") && !strings.HasPrefix(rel, "auth-continuity"+string(os.PathSeparator)) {
			continue
		}
		before, err := handoffOptionalDigest(filepath.Join(cfg.ConfigDir, rel))
		if err != nil {
			return result, err
		}
		after, err := handoffOptionalDigest(filepath.Join(root, rel))
		if err != nil {
			return result, err
		}
		if before != after {
			plan.Files = append(plan.Files, AuthorityHandoffFile{Relative: rel, Before: before, After: after, Mode: 0600})
		}
	}
	identityBefore, err := handoffOptionalDigest(handoffIdentityPath(cfg))
	if err != nil {
		return result, err
	}
	identityAfter, err := handoffFileDigest(stageCfg.NodeIdentityFile)
	if err != nil {
		return result, err
	}
	plan.Files = append(plan.Files, AuthorityHandoffFile{Relative: "node-identity.json", Destination: handoffIdentityPath(cfg), Before: identityBefore, After: identityAfter, Mode: 0600})
	for _, path := range []string{cfg.RegistryStateFile, cfg.SecretStoreKeyFile} {
		digest, err := handoffOptionalDigest(path)
		if err != nil {
			return result, err
		}
		plan.Guards = append(plan.Guards, AuthorityHandoffGuard{Path: path, Digest: digest})
	}
	if err := handoffPhysicalGuards(&plan, options.PhysicalIdentityFile); err != nil {
		return result, err
	}
	if options.IdentityDerivedEntrypoint != "" {
		// HAOS member startup derives AuthSourceID from the identity file. Its
		// immutable image entrypoint is an explicit reviewed dependency, not an
		// invented environment file. Every other changed knob must already be
		// the desired value; otherwise use a real persistent settings adapter.
		if options.EnvironmentFile != "" || cfg.AuthMode != "reader" || cfg.NodeWriterConnectTo != "" || strings.TrimSpace(cfg.NodePeersJSON) != "" || cfg.QuorumConfigFile != "" || options.Target == transition.SourceTarget {
			return result, errors.New("identity-derived startup would require additional persistent environment changes")
		}
		digest, err := handoffOptionalDigest(options.IdentityDerivedEntrypoint)
		if err != nil {
			return result, err
		}
		if digest == "" || options.EntrypointDigest == "" || digest != options.EntrypointDigest {
			return result, errors.New("identity-derived entrypoint differs from the reviewed runtime source")
		}
		plan.IdentityDerivedEnvironment = true
		plan.Guards = append(plan.Guards, AuthorityHandoffGuard{Path: options.IdentityDerivedEntrypoint, Digest: digest})
		if seedGuard != nil {
			plan.Guards = append(plan.Guards, *seedGuard)
		}
	} else if err := handoffStageEnvironment(&plan, root, options.EnvironmentFile); err != nil {
		return result, err
	}
	if err := handoffWritePrivate(filepath.Join(options.OutputDir, "plan.json"), plan); err != nil {
		return result, err
	}
	digest, err := handoffFileDigest(filepath.Join(options.OutputDir, "plan.json"))
	if err != nil {
		return result, err
	}
	return AuthorityHandoffPrepared{Directory: options.OutputDir, PlanDigest: digest, TransitionDigest: plan.SourceDigest, WriterID: transition.WriterID, AuthGeneration: transition.Auth.Generation, MembershipEpoch: transition.Membership.Epoch, Files: len(plan.Files)}, nil
}
