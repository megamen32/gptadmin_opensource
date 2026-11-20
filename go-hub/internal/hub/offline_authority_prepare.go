package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type AuthorityHandoffPrepareOptions struct {
	BundleDir            string
	BundleDigest         string
	Target               string
	ExpectedLocalID      string
	SourcePeerURL        string
	SourceConnectTo      string
	OutputDir            string
	EnvironmentFile      string
	PhysicalIdentityFile string
}

type AuthorityHandoffFile struct {
	Relative    string `json:"relative"`
	Destination string `json:"destination,omitempty"`
	Before      string `json:"before,omitempty"`
	After       string `json:"after,omitempty"`
	Mode        uint32 `json:"mode"`
}

type AuthorityHandoffGuard struct {
	Path   string `json:"path"`
	Digest string `json:"digest,omitempty"`
}

type AuthorityHandoffPlan struct {
	Schema                     string                  `json:"schema"`
	Role                       string                  `json:"role"`
	ConfigDir                  string                  `json:"config_dir"`
	Target                     string                  `json:"target"`
	LocalID                    string                  `json:"local_id"`
	SourceDigest               string                  `json:"source_digest"`
	Files                      []AuthorityHandoffFile  `json:"files"`
	Guards                     []AuthorityHandoffGuard `json:"guards"`
	Environment                map[string]string       `json:"environment"`
	EnvironmentFile            string                  `json:"environment_file,omitempty"`
	IdentityDerivedEnvironment bool                    `json:"identity_derived_environment,omitempty"`
	PhysicalIdentityFile       string                  `json:"physical_identity_file,omitempty"`
	LegacyRecords              []quorumTaskRecordRow   `json:"legacy_records,omitempty"`
	LegacyRequests             []quorumTaskRequestRow  `json:"legacy_requests,omitempty"`
}

// AuthorityReaderTransition is also private. The former primary can lack a
// member identity. Its one newly generated key is carried here, never in a
// public receipt, and is accepted only for the previously keyless self-entry.
type AuthorityReaderTransition struct {
	Schema            string               `json:"schema"`
	SourceTarget      string               `json:"source_target"`
	WriterTarget      string               `json:"writer_target"`
	WriterID          string               `json:"writer_id"`
	PreviousWriterID  string               `json:"previous_writer_id"`
	WriterURL         string               `json:"writer_url"`
	Auth              authSnapshot         `json:"auth"`
	Named             instructionSetsState `json:"named"`
	Membership        *nodeMembershipState `json:"membership"`
	SourceIdentity    nodeIdentity         `json:"source_identity"`
	SignerFingerprint string               `json:"signer_fingerprint"`
}

type AuthorityHandoffPrepared struct {
	Directory        string `json:"directory"`
	PlanDigest       string `json:"plan_digest"`
	TransitionDigest string `json:"transition_digest,omitempty"`
	WriterID         string `json:"writer_id"`
	AuthGeneration   uint64 `json:"auth_generation"`
	MembershipEpoch  uint64 `json:"membership_epoch"`
	Files            int    `json:"files"`
}

func handoffCopyFile(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > authorityHandoffBudget {
		return errors.New("handoff copy requires bounded regular file")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func handoffCopyTree(source, destination string) error {
	if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("handoff input tree contains a symlink")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(destination, rel), 0700)
		}
		if strings.HasSuffix(entry.Name(), ".lock") {
			return nil
		}
		return handoffCopyFile(path, filepath.Join(destination, rel))
	})
}

func handoffProjectConfig(cfg Config, root string) Config {
	previous := cfg.ConfigDir
	cfg.ConfigDir = root
	for _, p := range []*string{&cfg.RegistryStateFile, &cfg.StartupInstructionsFile, &cfg.InstructionSetsStateFile, &cfg.VirtualMCPStateFile, &cfg.WebhookConfigFile, &cfg.WebhookStateFile, &cfg.SecurityStateFile, &cfg.SecretStoreDir, &cfg.SecretStoreKeyFile, &cfg.SecretIngressStateFile} {
		rel, _ := filepath.Rel(previous, *p)
		*p = filepath.Join(root, rel)
	}
	cfg.NodeIdentityFile = filepath.Join(root, "node-identity.json")
	cfg.QuorumConfigFile = ""
	cfg.OutputDir = filepath.Join(root, "outputs")
	cfg.NetworkProxyStateFile = filepath.Join(root, "network_proxy_state.json")
	cfg.TelemetryStateFile = filepath.Join(root, "telemetry_state.json")
	cfg.FailoverConfigFile = filepath.Join(root, "failover_config.json")
	cfg.FailoverStateFile = filepath.Join(root, "failover_state.json")
	cfg.NetworkProxyRelayKeyFile = ""
	cfg.NetworkProxyRelayURL = ""
	cfg.NetworkProxyRelayRevokeURL = ""
	cfg.TelemetryOTLPEndpoint = ""
	cfg.ExistingMCPBearers = nil
	cfg.NodePeersJSON = ""
	cfg.MeshCoreSocket = ""
	return cfg
}

func handoffIdentityPath(cfg Config) string {
	if cfg.NodeIdentityFile != "" {
		return cfg.NodeIdentityFile
	}
	return filepath.Join(cfg.ConfigDir, "node-identity.json")
}

func handoffLoadNodeIdentity(cfg Config, optional bool) (*nodeIdentity, error) {
	path := handoffIdentityPath(cfg)
	if _, err := os.Lstat(path); optional && errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var identity nodeIdentity
	if err := handoffReadJSON(path, &identity, false, 16<<10); err != nil {
		return nil, err
	}
	if identity.Schema != nodeIdentityTag || identity.NodeID == "" || identity.NodeKey == "" {
		return nil, errors.New("incomplete existing member identity")
	}
	return &identity, nil
}

func handoffLocalExecutor(cfg Config, target, expectedLocalID string, physicalPaths ...string) (map[string]string, string, error) {
	var state persistentRegistryState
	if err := handoffReadJSON(cfg.RegistryStateFile, &state, false, 16<<20); err != nil {
		return nil, "", err
	}
	agent, ok := state.Agents[target]
	if !ok || truthy(agent.Meta["self_enrolled"]) {
		return nil, "", errors.New("target is not a pinned physical local executor")
	}
	identity := map[string]string{}
	for _, name := range []string{"server_id", "public_key", "fingerprint"} {
		value, ok := agent.Meta[name].(string)
		if !ok || value == "" {
			return nil, "", errors.New("target executor identity is incomplete")
		}
		identity[name] = value
	}
	if expectedLocalID == "" || identity["server_id"] != expectedLocalID {
		return nil, "", errors.New("target physical local identity changed")
	}
	physical := ""
	if len(physicalPaths) > 0 {
		physical = physicalPaths[0]
	}
	if physical != "" {
		// Local registration flags can be replaced by an ordinary heartbeat.
		// Bind to the configured on-disk executor identity and its actual key,
		// not to an incidental runtime metadata flag in the shared registry.
		if !filepath.IsAbs(physical) || filepath.Base(physical) != "shellmcp_identity.json" {
			return nil, "", errors.New("physical identity path is not canonical")
		}
		var disk struct {
			Name        string `json:"name"`
			ServerID    string `json:"server_id"`
			PublicKey   string `json:"public_key"`
			Fingerprint string `json:"fingerprint"`
		}
		if err := handoffReadJSON(physical, &disk, false, 16<<10); err != nil {
			return nil, "", err
		}
		if disk.Name != strings.TrimPrefix(target, "shell:") || disk.ServerID != expectedLocalID || disk.PublicKey != identity["public_key"] || disk.Fingerprint != identity["fingerprint"] {
			return nil, "", errors.New("configured physical identity differs from registry and reviewed target")
		}
		keyPath := filepath.Join(filepath.Dir(physical), "shellmcp_ed25519")
		if err := regularAuthPath(keyPath, false); err != nil {
			return nil, "", err
		}
		raw, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, "", err
		}
		defer zeroBytes(raw)
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, "", errors.New("physical executor private key is invalid")
		}
		defer zeroBytes(block.Bytes)
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, "", errors.New("physical executor key is not PKCS8")
		}
		private, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, "", errors.New("physical executor key is not ed25519")
		}
		defer zeroBytes(private)
		public := private.Public().(ed25519.PublicKey)
		fingerprint := sha256.Sum256(public)
		if base64.RawURLEncoding.EncodeToString(public) != disk.PublicKey || "SHA256:"+base64.RawURLEncoding.EncodeToString(fingerprint[:]) != disk.Fingerprint {
			return nil, "", errors.New("physical executor key does not match its persisted public identity")
		}
	} else {
		// The direct library fixture path can use an explicitly registered
		// in-process identity. The runtime CLI always supplies a disk proof.
		if !truthy(agent.Meta["local_executor"]) {
			return nil, "", errors.New("physical identity proof is required")
		}
		for id, entry := range state.Agents {
			if id != target && truthy(entry.Meta["local_executor"]) && !truthy(entry.Meta["self_enrolled"]) {
				return nil, "", errors.New("handoff requires one unambiguous physical local executor")
			}
		}
	}
	// New/InitializeAuthContinuity needs an in-process registration only in the
	// private stage. This random token is never exported or installed.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, "", err
	}
	token := hex.EncodeToString(key)
	zeroBytes(key)
	return identity, token, nil
}

func handoffPhysicalGuards(plan *AuthorityHandoffPlan, path string) error {
	if path == "" {
		return nil
	}
	plan.PhysicalIdentityFile = path
	for _, p := range []string{path, filepath.Join(filepath.Dir(path), "shellmcp_ed25519")} {
		hash, err := handoffFileDigest(p)
		if err != nil {
			return err
		}
		plan.Guards = append(plan.Guards, AuthorityHandoffGuard{Path: p, Digest: hash})
	}
	return nil
}

func handoffValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("handoff route must be an explicit HTTPS origin")
	}
	return nil
}

func handoffSourceReader(bundle AuthorityHandoffBundle, state *nodeMembershipState, sourceURL, pin string) (nodeIdentity, error) {
	var identity nodeIdentity
	if err := handoffValidateURL(sourceURL); err != nil {
		return identity, err
	}
	if pin != "" && net.ParseIP(pin) == nil {
		return identity, errors.New("former writer route pin must be an IP address")
	}
	if pin == "" && strings.TrimRight(sourceURL, "/") == strings.TrimRight(bundle.PublicOrigin, "/") {
		return identity, errors.New("former writer route would point at the new public writer without a physical pin")
	}
	if bundle.SourceIdentity != nil {
		identity = *bundle.SourceIdentity
		member := state.Nodes[identity.NodeID]
		if member == nil || member.Name != bundle.SourceTarget || member.LocalID != bundle.SourceWriterID || (member.KeyHash != hashNodeSecret(identity.NodeKey) && !(identity.NodeID == bundle.SourceWriterID && member.KeyHash == "")) {
			return identity, errors.New("former writer member identity does not match retained enrollment")
		}
	} else {
		member := state.Nodes[bundle.SourceWriterID]
		if member == nil || member.Name != bundle.SourceTarget || member.KeyHash != "" {
			return identity, errors.New("missing source identity is allowed only for the keyless writer self-entry")
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return identity, err
		}
		identity = nodeIdentity{Schema: nodeIdentityTag, NodeID: bundle.SourceWriterID, NodeKey: hex.EncodeToString(key), Created: time.Now().UTC()}
		zeroBytes(key)
	}
	member := state.Nodes[identity.NodeID]
	member.KeyHash = hashNodeSecret(identity.NodeKey)
	member.Endpoints = []string{sourceURL}
	member.ConnectTo = pin
	member.LocalID = bundle.SourceWriterID
	member.State = nodeStateOffline
	member.LastSeen = time.Time{}
	member.LeaseExpires = time.Time{}
	if member.Generation == 0 {
		member.Generation = 1
	}
	if identity.NodeID != bundle.SourceWriterID {
		old := state.Nodes[bundle.SourceWriterID]
		if old == nil {
			return identity, errors.New("former writer self-entry missing")
		}
		old.State = nodeStateOffline
		old.LeaseExpires = time.Time{}
	}
	return identity, nil
}

func handoffPromoteMembership(s *Server, state *nodeMembershipState, target string, identity nodeIdentity, writerURL string) error {
	if state == nil || state.Nodes == nil || state.WriterID == "" || state.Revision == 0 {
		return errors.New("incomplete authoritative membership")
	}
	if state.Grants == nil {
		state.Grants = map[string]*nodeJoinGrant{}
	}
	if err := handoffValidateURL(writerURL); err != nil {
		return err
	}
	member := state.Nodes[identity.NodeID]
	if member == nil || member.Name != target || member.LocalID != s.authLocalID || member.KeyHash != hashNodeSecret(identity.NodeKey) {
		return errors.New("target membership and physical identity do not match")
	}
	if member.LocalFingerprint != "" && member.LocalFingerprint != s.localExecutors[target]["fingerprint"] {
		return errors.New("target physical fingerprint changed")
	}
	if state.Revision == math.MaxUint64 || state.Epoch == math.MaxUint64 {
		return errors.New("membership version exhausted")
	}
	if previous := state.Nodes[s.authLocalID]; previous != nil && previous.NodeID != identity.NodeID && (previous.Name != target || previous.LocalID != s.authLocalID) {
		return errors.New("writer self identity collides with another physical node")
	}
	self := *member
	if identity.NodeID != s.authLocalID {
		member.State = nodeStateOffline
		member.LeaseExpires = time.Time{}
	}
	self.NodeID = s.authLocalID
	self.LocalID = s.authLocalID
	self.Name = target
	self.State = nodeStateReady
	self.KeyHash = ""
	self.Endpoints = []string{s.cfg.NodePublicURL}
	self.ConnectTo = ""
	self.LastSeen = s.now()
	self.LeaseExpires = s.now().Add(nodeLeaseTTL)
	self.LocalFingerprint = s.localExecutors[target]["fingerprint"]
	state.Nodes[self.NodeID] = &self
	state.WriterID = self.NodeID
	state.WriterURL = writerURL
	state.Epoch++
	state.Revision++
	if state.Epoch == 0 {
		state.Epoch = 1
	}
	s.membershipMu.Lock()
	s.membership = state
	err := s.persistMembershipStateLocked(state)
	s.membershipMu.Unlock()
	if err != nil {
		return err
	}
	s.rebuildMembershipRoutes()
	return nil
}

func handoffAuthorityPaths(cfg Config) ([]string, error) {
	paths := []string{cfg.RegistryStateFile, cfg.StartupInstructionsFile, cfg.InstructionSetsStateFile, cfg.VirtualMCPStateFile, cfg.WebhookConfigFile, cfg.WebhookStateFile, cfg.SecurityStateFile, filepath.Join(cfg.ConfigDir, webAuthnStateFilename), filepath.Join(cfg.ConfigDir, "membership", "membership.json"), filepath.Join(cfg.ConfigDir, "exec-content.sqlite")}
	for _, root := range []string{filepath.Join(cfg.ConfigDir, "auth-continuity"), cfg.SecretStoreDir} {
		if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("authority state tree contains symlink")
			}
			if !entry.IsDir() && !strings.HasSuffix(entry.Name(), ".lock") {
				paths = append(paths, path)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	var result []string
	for _, path := range paths {
		rel, err := filepath.Rel(cfg.ConfigDir, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, errors.New("authority output escaped config directory")
		}
		if !seen[rel] {
			seen[rel] = true
			result = append(result, rel)
		}
	}
	sort.Strings(result)
	return result, nil
}

func handoffOptionalDigest(path string) (string, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if err := regularAuthPath(path, false); err != nil {
		return "", err
	}
	return handoffFileDigest(path)
}

func handoffMakePlan(original, staged Config, target, localID, sourceDigest, role string, env map[string]string) (AuthorityHandoffPlan, error) {
	plan := AuthorityHandoffPlan{Schema: authorityHandoffSchema, Role: role, ConfigDir: original.ConfigDir, Target: target, LocalID: localID, SourceDigest: sourceDigest, Environment: env}
	before, err := handoffAuthorityPaths(original)
	if err != nil {
		return plan, err
	}
	after, err := handoffAuthorityPaths(staged)
	if err != nil {
		return plan, err
	}
	set := map[string]bool{}
	for _, p := range append(before, after...) {
		set[p] = true
	}
	names := make([]string, 0, len(set))
	for p := range set {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, rel := range names {
		a, err := handoffOptionalDigest(filepath.Join(original.ConfigDir, rel))
		if err != nil {
			return plan, err
		}
		b, err := handoffOptionalDigest(filepath.Join(staged.ConfigDir, rel))
		if err != nil {
			return plan, err
		}
		if a != b {
			plan.Files = append(plan.Files, AuthorityHandoffFile{Relative: rel, Before: a, After: b, Mode: 0600})
		}
	}
	for _, p := range []string{original.SecretStoreKeyFile, handoffIdentityPath(original)} {
		digest, err := handoffOptionalDigest(p)
		if err != nil {
			return plan, err
		}
		plan.Guards = append(plan.Guards, AuthorityHandoffGuard{Path: p, Digest: digest})
	}
	return plan, nil
}

// Keep an equal active store in place. A strict subset can be replaced only by
// the complete verified incoming snapshot, with no target-only or changed row.
// The ordinary file installer does not remove WAL files, so replacement also
// requires an absent or empty target WAL and pins both original files for apply.
func handoffStageWriterContent(target, incoming, staged, incomingDigest string, now int64) (bool, []AuthorityHandoffGuard, error) {
	var guards []AuthorityHandoffGuard
	for _, path := range []string{target, target + "-wal"} {
		digest, err := handoffOptionalDigest(path)
		if err != nil {
			return false, nil, err
		}
		guards = append(guards, AuthorityHandoffGuard{Path: path, Digest: digest})
	}
	retain := guards[0].Digest != ""
	if incomingDigest != "" {
		if retain {
			before, err := handoffContentState(target, now)
			if err != nil {
				return false, nil, err
			}
			after, err := handoffContentState(incoming, now)
			if err != nil {
				return false, nil, err
			}
			retain = before.Digest == after.Digest
			if !retain {
				if len(before.Sources) >= len(after.Sources) {
					return false, nil, errors.New("target has distinct active content references; preserve both stores before handoff")
				}
				for id, digest := range before.Sources {
					if after.Sources[id] != digest {
						return false, nil, errors.New("target has distinct active content references; preserve both stores before handoff")
					}
				}
			}
		}
		if !retain {
			empty := sha256.Sum256(nil)
			if guards[1].Digest != "" && guards[1].Digest != hex.EncodeToString(empty[:]) {
				return false, nil, errors.New("target content WAL is not empty; preserve it before replacing the content snapshot")
			}
			if err := handoffCopyFile(incoming, staged); err != nil {
				return false, nil, err
			}
			if digest, err := handoffFileDigest(staged); err != nil || digest != incomingDigest {
				return false, nil, errors.New("staged content snapshot differs from the verified incoming bundle")
			}
		}
	}
	for _, guard := range guards {
		digest, err := handoffOptionalDigest(guard.Path)
		if err != nil || digest != guard.Digest {
			return false, nil, errors.New("target content changed during preparation")
		}
	}
	return retain, guards, nil
}

// PrepareStoppedAuthorityWriter materializes actual Hub state in a new private
// stage. Only this empty-task stage is passed to New; no source or copied
// production task database is ever opened by a runtime constructor.
func PrepareStoppedAuthorityWriter(cfg Config, options AuthorityHandoffPrepareOptions) (AuthorityHandoffPrepared, error) {
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
	bundle, err := readAuthorityHandoffBundle(options.BundleDir, options.BundleDigest)
	if err != nil {
		return result, err
	}
	if handoffDigest([]byte(cfg.OAuthClientSecret)) != bundle.SignerFingerprint {
		return result, errors.New("target signing key differs from source authority")
	}
	if cfg.AuthMode != "reader" || cfg.AuthSourceID != bundle.SourceWriterID {
		return result, errors.New("target is not a reader of the expected source writer")
	}
	local, token, err := handoffLocalExecutor(cfg, options.Target, options.ExpectedLocalID, options.PhysicalIdentityFile)
	if err != nil {
		return result, err
	}
	identity, err := handoffLoadNodeIdentity(cfg, false)
	if err != nil {
		return result, err
	}
	if local["server_id"] == bundle.SourceWriterID {
		return result, errors.New("source and target physical identities must differ")
	}
	if err := handoffValidateURL(cfg.NodePublicURL); err != nil {
		return result, err
	}
	if err := handoffValidateURL(bundle.PublicOrigin); err != nil {
		return result, err
	}
	var membership *nodeMembershipState
	if err := json.Unmarshal(bundle.Domains["membership/writer"], &membership); err != nil {
		return result, err
	}
	sourceIdentity, err := handoffSourceReader(bundle, membership, options.SourcePeerURL, options.SourceConnectTo)
	if err != nil {
		return result, err
	}
	// Refuse target-only one-time secrets rather than silently removing them.
	localSecrets, err := handoffReadSecrets(cfg)
	if err != nil {
		return result, err
	}
	var sourceSecrets quorumSecretIngressState
	if err := json.Unmarshal(bundle.Domains["secret_ingress/current"], &sourceSecrets); err != nil {
		return result, err
	}
	for ref, request := range localSecrets.Requests {
		other, ok := sourceSecrets.Requests[ref]
		if !ok || request != other || localSecrets.Values[ref] != sourceSecrets.Values[ref] {
			return result, errors.New("target has distinct secret ingress state; explicit preservation required")
		}
	}
	if err := handoffPrivateOutput(options.OutputDir); err != nil {
		return result, err
	}
	root := filepath.Join(options.OutputDir, "config")
	if err := os.Mkdir(root, 0700); err != nil {
		return result, err
	}
	stageCfg := handoffProjectConfig(cfg, root)
	if err := handoffCopyFile(cfg.RegistryStateFile, stageCfg.RegistryStateFile); err != nil {
		return result, err
	}
	if err := handoffCopyTree(filepath.Join(cfg.ConfigDir, "auth-continuity"), filepath.Join(root, "auth-continuity")); err != nil {
		return result, err
	}
	if err := handoffCopyFile(cfg.SecretStoreKeyFile, stageCfg.SecretStoreKeyFile); err != nil {
		return result, err
	}
	if err := handoffCopyTree(cfg.SecretStoreDir, stageCfg.SecretStoreDir); err != nil {
		return result, err
	}
	if err := handoffCopyFile(handoffIdentityPath(cfg), stageCfg.NodeIdentityFile); err != nil {
		return result, err
	}
	stageCfg.AuthMode = "writer"
	stageCfg.AuthSourceID = ""
	stageCfg.PublicOrigin = bundle.PublicOrigin
	stageCfg.MCPResource = bundle.MCPResource
	public := bundle.PublicAuth
	stageCfg.OAuthKeyID = public.KeyID
	stageCfg.LegacyPublicOrigins = public.LegacyOrigins
	stageCfg.OAuthAllowedRedirects = public.AllowedRedirects
	stageCfg.OAuthPermissiveRedirects = public.PermissiveRedirects
	stageCfg.OAuthPermissiveResources = public.PermissiveResources
	stageCfg.RelaxAuthChecks = public.RelaxAuthChecks
	stageCfg.InstructionContextRequired = public.InstructionContextRequired
	stageCfg.InstructionSessionReceipts = public.InstructionSessionReceipts
	stageCfg.AuthRateLimit = public.AuthRateLimit
	stageCfg.StandaloneAutoTrust = public.StandaloneAutoTrust
	stageCfg.DebugLowSecurity = public.DebugLowSecurity
	stageCfg.AuthLogSecrets = public.AuthLogSecrets
	s := New(stageCfg)
	defer s.Close()
	if s.secretStoreErr != nil {
		return result, s.secretStoreErr
	}
	if err := s.RegisterLocalExecutor(strings.TrimPrefix(options.Target, "shell:"), local, token); err != nil {
		return result, err
	}
	if err := s.InitializeAuthContinuity(); err != nil {
		return result, err
	}
	values := make(map[string]json.RawMessage, len(bundle.Domains))
	for k, v := range bundle.Domains {
		values[k] = append(json.RawMessage(nil), v...)
	}
	var registry persistentRegistryState
	if err := json.Unmarshal(values["registry/current"], &registry); err != nil {
		return result, err
	}
	var targetRegistry persistentRegistryState
	if err := handoffReadJSON(cfg.RegistryStateFile, &targetRegistry, false, 16<<20); err != nil {
		return result, err
	}
	for id, existing := range targetRegistry.LocalExecutorTokens {
		if id == options.Target && !truthy(targetRegistry.Agents[id].Meta["self_enrolled"]) {
			continue
		}
		if existing != registry.LocalExecutorTokens[id] {
			return result, errors.New("target has distinct self-enrollment credentials; explicit preservation required")
		}
	}
	s.localExecutorTokens = map[string]string{}
	for id, binding := range registry.LocalExecutorTokens {
		if !truthy(registry.Agents[id].Meta["self_enrolled"]) {
			return result, errors.New("source credential binding is not a self-enrollment")
		}
		s.localExecutorTokens[id] = binding
	}
	for id, agent := range registry.Agents {
		if agent.Meta == nil {
			agent.Meta = map[string]any{}
		}
		if id != options.Target {
			delete(agent.Meta, "local_executor")
		}
		registry.Agents[id] = agent
	}
	values["registry/current"], err = json.Marshal(registry)
	if err != nil {
		return result, err
	}
	values["membership/writer"], err = json.Marshal(membership)
	if err != nil {
		return result, err
	}
	s.authMu.Lock()
	err = s.materializeAuthorityValues(values, func(state *nodeMembershipState) error {
		return handoffPromoteMembership(s, state, options.Target, *identity, bundle.PublicOrigin)
	})
	s.authMu.Unlock()
	if err != nil {
		return result, err
	}
	s.mu.Lock()
	err = s.saveWebhookRoutesLocked()
	s.mu.Unlock()
	if err != nil {
		return result, err
	}
	auth, err := s.readAuthSlot(s.authState)
	if err != nil {
		return result, err
	}
	sourceIdentity.WriterID = s.authLocalID
	sourceIdentity.WriterURL = bundle.PublicOrigin
	transition := AuthorityReaderTransition{Schema: authorityHandoffSchema, SourceTarget: bundle.SourceTarget, WriterTarget: options.Target, WriterID: s.authLocalID, PreviousWriterID: bundle.SourceWriterID, WriterURL: bundle.PublicOrigin, Auth: auth, Named: instructionSetsState{Sets: cloneInstructionSets(s.instructionSets)}, Membership: s.membership, SourceIdentity: sourceIdentity, SignerFingerprint: bundle.SignerFingerprint}
	if err := handoffWritePrivate(filepath.Join(options.OutputDir, "reader-transition.json"), transition); err != nil {
		return result, err
	}
	contentPath := filepath.Join(cfg.ConfigDir, "exec-content.sqlite")
	retainExistingContent, contentGuards, err := handoffStageWriterContent(contentPath, filepath.Join(options.BundleDir, "exec-content.sqlite"), filepath.Join(root, "exec-content.sqlite"), bundle.ContentDigest, time.Now().Unix())
	if err != nil {
		return result, err
	}
	if err := s.Close(); err != nil {
		return result, err
	}
	plan, err := handoffMakePlan(cfg, stageCfg, options.Target, local["server_id"], options.BundleDigest, "writer", map[string]string{"GPTADMIN_AUTH_MODE": "writer", "GPTADMIN_AUTH_SOURCE_ID": "", "GPTADMIN_NODE_SEED": "", "GPTADMIN_NODE_WRITER_CONNECT_TO": "", "GPTADMIN_NODE_PEERS": "", "GPTADMIN_QUORUM_CONFIG_FILE": "", "PUBLIC_ORIGIN": bundle.PublicOrigin, "MCP_RESOURCE": bundle.MCPResource})
	if err != nil {
		return result, err
	}
	plan.LegacyRecords = bundle.LegacyRecords
	plan.LegacyRequests = bundle.LegacyRequests
	if err := handoffPhysicalGuards(&plan, options.PhysicalIdentityFile); err != nil {
		return result, err
	}
	for key, value := range map[string]string{"GPTADMIN_JWT_KEY_ID": public.KeyID, "GPTADMIN_LEGACY_PUBLIC_ORIGINS": strings.Join(public.LegacyOrigins, ","), "GPTADMIN_OAUTH_ALLOWED_REDIRECTS": strings.Join(public.AllowedRedirects, ","), "OAUTH_PERMISSIVE_REDIRECTS": strconv.FormatBool(public.PermissiveRedirects), "OAUTH_PERMISSIVE_RESOURCES": strconv.FormatBool(public.PermissiveResources), "GPTADMIN_RELAX_AUTH_CHECKS": strconv.FormatBool(public.RelaxAuthChecks), "GPTADMIN_INSTRUCTION_CONTEXT_REQUIRED": strconv.FormatBool(public.InstructionContextRequired), "GPTADMIN_INSTRUCTION_SESSION_RECEIPTS": strconv.FormatBool(public.InstructionSessionReceipts), "GPTADMIN_AUTH_RATE_LIMIT": strconv.Itoa(public.AuthRateLimit)} {
		plan.Environment[key] = value
	}
	plan.Environment["GPTADMIN_STANDALONE"] = strconv.FormatBool(public.StandaloneAutoTrust)
	plan.Environment["DEBUG_VERIFY_WORK_LOW_SECURITY_MODE"] = strconv.FormatBool(public.DebugLowSecurity)
	plan.Environment["AUTH_LOG_SECRETS"] = strconv.FormatBool(public.AuthLogSecrets)
	for name := range cfg.ExistingMCPBearers {
		plan.Environment[name] = ""
	}
	for name, value := range bundle.ConfiguredBearers {
		if !validSecretEnvName.MatchString(name) || !strings.HasPrefix(name, "GPTADMIN_") || !strings.HasSuffix(name, "_MCP_BEARER") {
			return result, errors.New("invalid source configured bearer environment binding")
		}
		plan.Environment[name] = value
	}
	if retainExistingContent {
		kept := plan.Files[:0]
		for _, file := range plan.Files {
			if file.Relative != "exec-content.sqlite" {
				kept = append(kept, file)
			}
		}
		plan.Files = kept
	} else if bundle.ContentDigest != "" {
		// A replaced file is guarded by Files.Before/After, which also permits
		// preactivation restoration. An old-digest immutable main-file guard
		// would reject our own installed snapshot during that restoration.
		bound := false
		for _, file := range plan.Files {
			if file.Relative == "exec-content.sqlite" {
				bound = file.Before == contentGuards[0].Digest && file.After == bundle.ContentDigest
			}
		}
		if !bound {
			return result, errors.New("content replacement no longer matches the inspected original and incoming snapshots")
		}
		contentGuards = contentGuards[1:]
	}
	plan.Guards = append(plan.Guards, contentGuards...)
	if err := handoffStageEnvironment(&plan, root, options.EnvironmentFile); err != nil {
		return result, err
	}
	if err := handoffWritePrivate(filepath.Join(options.OutputDir, "plan.json"), plan); err != nil {
		return result, err
	}
	planDigest, err := handoffFileDigest(filepath.Join(options.OutputDir, "plan.json"))
	if err != nil {
		return result, err
	}
	transitionDigest, err := handoffFileDigest(filepath.Join(options.OutputDir, "reader-transition.json"))
	if err != nil {
		return result, err
	}
	result = AuthorityHandoffPrepared{Directory: options.OutputDir, PlanDigest: planDigest, TransitionDigest: transitionDigest, WriterID: auth.WriterID, AuthGeneration: auth.Generation, MembershipEpoch: s.membership.Epoch, Files: len(plan.Files)}
	return result, nil
}
