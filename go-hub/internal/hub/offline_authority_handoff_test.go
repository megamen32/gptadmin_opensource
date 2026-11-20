package hub

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	webauthnlib "github.com/go-webauthn/webauthn/webauthn"
)

func offlinePhysicalIdentity(t *testing.T, cfg Config, target, localID string) string {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.ConfigDir, "fixture-executor")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shellmcp_ed25519"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(public)
	meta := map[string]string{"name": strings.TrimPrefix(target, "shell:"), "server_id": localID, "public_key": base64.RawURLEncoding.EncodeToString(public), "fingerprint": "SHA256:" + base64.RawURLEncoding.EncodeToString(h[:])}
	path := filepath.Join(dir, "shellmcp_identity.json")
	if err := handoffWritePrivate(path, meta); err != nil {
		t.Fatal(err)
	}
	var registry persistentRegistryState
	if err := handoffReadJSON(cfg.RegistryStateFile, &registry, false, 16<<20); err != nil {
		t.Fatal(err)
	}
	agent := registry.Agents[target]
	delete(agent.Meta, "local_executor")
	for _, key := range []string{"server_id", "public_key", "fingerprint"} {
		agent.Meta[key] = meta[key]
	}
	registry.Agents[target] = agent
	if err := handoffWriteFileJSON(cfg.RegistryStateFile, registry); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOfflineAuthorityIdentityDerivedReviewedSeedAlias(t *testing.T) {
	source := offlineAuthorityFixture(t)
	target := offlineHandoffReader(t, "reader", "reader-identity", "member-44", "node44-key")
	peer := offlineHandoffReader(t, "server88", "server88-identity", "member-88", "node88-key")
	token, _, err := source.issueManagedMCPToken("ordinary", 7, source.cfg.PublicOrigin, source.cfg.MCPResource)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := authContinuityHTTP(source, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
	for _, node := range []*Server{target, peer} {
		if got := authContinuityHTTP(node, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", snapshot.Body.Bytes()); got.Code != 200 {
			t.Fatal("reader fixture snapshot failed")
		}
	}
	sourceCfg, targetCfg, peerCfg := source.cfg, target.cfg, peer.cfg
	for _, node := range []*Server{source, target, peer} {
		if err := node.Close(); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := ExportStoppedAuthority(sourceCfg, AuthorityHandoffExportOptions{SourceTarget: "shell:writer", ExpectedWriter: "writer-identity", OutputDir: filepath.Join(t.TempDir(), "bundle")})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := PrepareStoppedAuthorityWriter(targetCfg, AuthorityHandoffPrepareOptions{BundleDir: bundle.Directory, BundleDigest: bundle.BundleDigest, Target: "shell:reader", ExpectedLocalID: "reader-identity", SourcePeerURL: "https://s100-node.example", OutputDir: filepath.Join(t.TempDir(), "writer")})
	if err != nil {
		t.Fatal(err)
	}
	alias := "https://reviewed-child.example"
	peerCfg.NodeSeedURL = alias
	peerCfg.NodeWriterConnectTo = ""
	peerCfg.NodePeersJSON = ""
	entrypoint := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(entrypoint, []byte("# reviewed identity-derived fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	optionsFile := filepath.Join(t.TempDir(), "options.json")
	optionsRaw := []byte(`{"failover":{"primary_public_url":"https://reviewed-child.example"},"unrelated":"preserved"}`)
	if err := os.WriteFile(optionsFile, optionsRaw, 0600); err != nil {
		t.Fatal(err)
	}
	entryDigest, _ := handoffFileDigest(entrypoint)
	optionsDigest, _ := handoffFileDigest(optionsFile)
	options := AuthorityReaderPrepareOptions{TransitionFile: filepath.Join(writer.Directory, "reader-transition.json"), TransitionDigest: writer.TransitionDigest, Target: "shell:server88", ExpectedLocalID: "server88-identity", IdentityDerivedEntrypoint: entrypoint, EntrypointDigest: entryDigest}
	prepare := func(o AuthorityReaderPrepareOptions) (AuthorityHandoffPrepared, error) {
		o.OutputDir = filepath.Join(t.TempDir(), "reader")
		return PrepareStoppedAuthorityReader(peerCfg, o)
	}
	if _, err := prepare(options); err == nil {
		t.Fatal("default strict seed rule was weakened")
	}
	options.IdentityDerivedSeedAlias = alias
	options.IdentityDerivedOptionsFile = optionsFile
	options.IdentityDerivedOptionsDigest = optionsDigest
	for name, change := range map[string]func(*AuthorityReaderPrepareOptions){"different-alias": func(o *AuthorityReaderPrepareOptions) { o.IdentityDerivedSeedAlias = "https://other.example" }, "different-options": func(o *AuthorityReaderPrepareOptions) { o.IdentityDerivedOptionsDigest = strings.Repeat("0", 64) }, "missing-entrypoint": func(o *AuthorityReaderPrepareOptions) { o.IdentityDerivedEntrypoint = "" }} {
		t.Run(name, func(t *testing.T) {
			bad := options
			change(&bad)
			if _, err := prepare(bad); err == nil {
				t.Fatal("unreviewed seed exception accepted")
			}
		})
	}
	identity, err := handoffLoadNodeIdentity(peerCfg, false)
	if err != nil {
		t.Fatal(err)
	}
	canonical := identity.WriterURL
	identity.WriterURL = alias
	if err := saveNodeIdentityFile(handoffIdentityPath(peerCfg), *identity); err != nil {
		t.Fatal(err)
	}
	if _, err := prepare(options); err == nil {
		t.Fatal("noncanonical pinned identity accepted")
	}
	identity.WriterURL = canonical
	if err := saveNodeIdentityFile(handoffIdentityPath(peerCfg), *identity); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepare(options)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := handoffReadPlan(prepared.Directory, prepared.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IdentityDerivedEnvironment || plan.Environment["GPTADMIN_NODE_SEED"] != alias || plan.Environment["GPTADMIN_AUTH_SOURCE_ID"] != "reader-identity" {
		t.Fatal("effective seed or new authority source was misrepresented")
	}
	guarded := false
	for _, guard := range plan.Guards {
		if guard.Path == optionsFile && guard.Digest == optionsDigest {
			guarded = true
		}
	}
	if !guarded {
		t.Fatal("persistent alias options lack a CAS guard")
	}
	if err := os.WriteFile(optionsFile, []byte(`{"failover":{"primary_public_url":"https://changed.example"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyStoppedAuthority(peerCfg, AuthorityHandoffApplyOptions{PreparedDir: prepared.Directory, PlanDigest: prepared.PlanDigest, BackupDir: filepath.Join(t.TempDir(), "rejected"), RequireEnvironment: true}); err == nil {
		t.Fatal("changed startup options bypassed apply guards")
	}
	if err := os.WriteFile(optionsFile, optionsRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyStoppedAuthority(peerCfg, AuthorityHandoffApplyOptions{PreparedDir: prepared.Directory, PlanDigest: prepared.PlanDigest, BackupDir: filepath.Join(t.TempDir(), "applied"), RequireEnvironment: true}); err != nil {
		t.Fatal(err)
	}
	peerCfg.AuthSourceID = "reader-identity"
	restarted := New(peerCfg)
	defer restarted.Close()
	if err := restarted.RegisterLocalExecutor("server88", map[string]string{"server_id": "server88-identity", "public_key": "server88-public", "fingerprint": "server88-fingerprint"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := restarted.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	if restarted.cfg.NodeSeedURL != alias || restarted.memberWriterURL() != canonical || restarted.nodeIdentity.WriterID != "reader-identity" || restarted.authState.WriterID != "reader-identity" {
		t.Fatal("native startup did not preserve canonical identity precedence and unchanged bootstrap seed")
	}
	if got := authContinuityHTTP(restarted, "GET", "/mcp", token, nil); got.Code != 200 {
		t.Fatal("prior managed bearer failed with reviewed bootstrap alias")
	}
	actual, _ := handoffFileDigest(optionsFile)
	if actual != optionsDigest {
		t.Fatal("bootstrap options were rewritten")
	}
}

func TestOfflineAuthorityConfiguredPhysicalIdentityWithoutRuntimeFlag(t *testing.T) {
	s := offlineAuthorityFixture(t)
	cfg := s.cfg
	s.Close()
	path := offlinePhysicalIdentity(t, cfg, "shell:writer", "writer-identity")
	if _, _, err := handoffLocalExecutor(cfg, "shell:writer", "writer-identity"); err == nil {
		t.Fatal("fixture unexpectedly retained runtime-only local registration")
	}
	if _, _, err := handoffLocalExecutor(cfg, "shell:writer", "writer-identity", path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := handoffLocalExecutor(cfg, "shell:wrong", "writer-identity", path); err == nil {
		t.Fatal("physical identity was rebound to another target")
	}
	if _, err := ExportStoppedAuthority(cfg, AuthorityHandoffExportOptions{SourceTarget: "shell:writer", ExpectedWriter: "writer-identity", OutputDir: filepath.Join(t.TempDir(), "disk-proof-export"), PhysicalIdentityFile: path}); err != nil {
		t.Fatal(err)
	}
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKCS8PrivateKey(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "shellmcp_ed25519"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := handoffLocalExecutor(cfg, "shell:writer", "writer-identity", path); err == nil {
		t.Fatal("mismatched actual executor private key accepted")
	}
}

func TestOfflineAuthorityReverseRetainsExactPhysicalBaseline(t *testing.T) {
	s := offlineAuthorityFixture(t)
	cfg := s.cfg
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	physical := offlinePhysicalIdentity(t, cfg, "shell:writer", "writer-identity")
	base, err := CaptureStoppedLegacyBaseline(cfg, "shell:writer", "writer-identity", physical, filepath.Join(t.TempDir(), "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	if base.Records != 2 || base.LookupPath != "/admin/api/tasks/{id}" {
		t.Fatal("baseline receipt lost physical history provenance")
	}
	export := func() (AuthorityHandoffReceipt, error) {
		return ExportStoppedAuthority(cfg, AuthorityHandoffExportOptions{SourceTarget: "shell:writer", ExpectedWriter: "writer-identity", PhysicalIdentityFile: physical, LegacyBaselineFile: base.File, LegacyBaselineDigest: base.Digest, OutputDir: filepath.Join(t.TempDir(), "reverse-export")})
	}
	first, err := export()
	if err != nil {
		t.Fatal(err)
	}
	if first.LegacyRecords != 0 || first.RetainedLegacy == nil || first.RetainedLegacy.Records != 2 {
		t.Fatal("unchanged physical baseline was copied or forgotten")
	}
	// The actual canonical task writer creates one new immutable record. It
	// remains eligible for the original exact import contract, unlike baseline.
	now := float64(time.Now().Unix())
	job := persistedShellTask{ID: "new-controller-legacy", Server: "reader", ReceiptTarget: "shell:reader", OwnerID: "new-owner", Caller: &taskCaller{ClientID: "new-caller"}, ToolName: "system_inspect", CreatedAt: now - 3, StartedAt: now - 2, DoneAt: now - 1, Status: "completed", Result: map[string]any{"new": true}}
	writer := &Server{cfg: cfg}
	if err := writer.withTaskDatabase(func(tx *taskTransaction) error {
		return tx.save(persistedTaskState{Shell: map[string]persistedShellTask{job.ID: job}})
	}); err != nil {
		t.Fatal(err)
	}
	next, err := export()
	if err != nil {
		t.Fatal(err)
	}
	if next.LegacyRecords != 1 || next.RetainedLegacy.Records != 2 {
		t.Fatal("new legacy receipt silently disappeared")
	}
	// Fixture-only mutations demonstrate that changed or missing baselines
	// cannot be silently exempted. No production code has an UPDATE/DELETE path.
	if err := writer.withTaskDatabase(func(tx *taskTransaction) error {
		_, err := tx.tx.Exec(`UPDATE task_records SET revision=revision+1 WHERE key='legacy-remote-receipt'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := export()
	if err != nil {
		t.Fatal(err)
	}
	if changed.LegacyRecords != 2 || changed.RetainedLegacy.Records != 1 {
		t.Fatal("changed baseline was exempted")
	}
	if err := writer.withTaskDatabase(func(tx *taskTransaction) error {
		_, err := tx.tx.Exec(`DELETE FROM task_records WHERE key='legacy-88-receipt'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := export(); err == nil || !strings.Contains(err.Error(), "disappeared") {
		t.Fatalf("missing baseline did not fail closed: %v", err)
	}
}

func offlineAuthorityFixture(t *testing.T) *Server {
	t.Helper()
	s := authContinuityNode(t, "writer", "")
	s.cfg.LegacyPublicOrigins = []string{"https://legacy-hub.example"}
	s.cfg.OAuthAllowedRedirects = []string{"https://approved.example/callback"}
	s.cfg.ExistingMCPBearers = map[string]string{"GPTADMIN_FIXTURE_MCP_BEARER": "fixture-configured-bearer", "GPTADMIN_CTL_ALIAS_MCP_BEARER": s.cfg.CtlToken}
	if err := s.reconcileExistingMCPBearers(); err != nil {
		t.Fatal(err)
	}
	if _, err := writeInstructionContentAtomic(s.cfg.StartupInstructionsFile, "Authoritative source instructions.\n"); err != nil {
		t.Fatal(err)
	}
	set := InstructionSet{ID: "audit", Content: "Keep the original receipt and caller.\n"}
	set.Version = instructionSetVersion(set.Content)
	if err := writeInstructionSetsStateAtomic(s.cfg.InstructionSetsStateFile, map[string]InstructionSet{set.ID: set}); err != nil {
		t.Fatal(err)
	}
	s.ensureWriterSelfEntry()
	s.membershipMu.Lock()
	s.membership.Nodes["member-44"] = &nodeMember{NodeID: "member-44", KeyHash: hashNodeSecret("node44-key"), Name: "shell:reader", LocalID: "reader-identity", LocalFingerprint: "reader-fingerprint", State: nodeStateReady, Generation: 1, Capabilities: append([]string(nil), nodeCapabilities...), Endpoints: []string{"https://node44.example"}, ConnectTo: "192.0.2.44", LastSeen: time.Now(), LeaseExpires: time.Now().Add(time.Hour)}
	s.membership.Nodes["member-88"] = &nodeMember{NodeID: "member-88", KeyHash: hashNodeSecret("node88-key"), Name: "shell:server88", LocalID: "server88-identity", State: nodeStateReady, Generation: 1, Capabilities: append([]string(nil), nodeCapabilities...), Endpoints: []string{"https://node88.example"}, LastSeen: time.Now(), LeaseExpires: time.Now().Add(time.Hour)}
	if err := s.persistMembershipStateLocked(s.membership); err != nil {
		t.Fatal(err)
	}
	s.membershipMu.Unlock()
	s.mu.Lock()
	s.agents["shell:reader"] = &Agent{AgentID: "shell:reader", Name: "reader", Kind: "virtual_shell", Transport: "long_poll", Status: "online", Meta: map[string]any{"approved": true, "server_id": "reader-identity", "public_key": "reader-public", "fingerprint": "reader-fingerprint"}}
	s.agents["shell:server88"] = &Agent{AgentID: "shell:server88", Name: "server88", Kind: "virtual_shell", Transport: "long_poll", Status: "online", Meta: map[string]any{"approved": true, "server_id": "server88-identity", "public_key": "server88-public", "fingerprint": "server88-fingerprint"}}
	s.agents["shell:remote-enrolled"] = &Agent{AgentID: "shell:remote-enrolled", Name: "remote-enrolled", Kind: "virtual_shell", Transport: "long_poll", Status: "online", Meta: map[string]any{"approved": true, "self_enrolled": true}}
	s.localExecutorTokens["shell:remote-enrolled"] = "fixture-remote-self-enrollment-credential"
	if err := s.saveRegistryStateLocked(); err != nil {
		t.Fatal(err)
	}
	s.virtualMCP["webhooks"] = true
	if err := s.saveVirtualMCPStateLocked(); err != nil {
		t.Fatal(err)
	}
	route := WebhookRoute{ID: "fixture-hook", Token: "fixture-webhook-secret", Action: WebhookAction{Kind: "shell", Target: "shell:writer", Command: "true"}}
	s.webhookRoutes[route.ID] = route
	if err := s.saveWebhookRoutesLocked(); err != nil {
		t.Fatal(err)
	}
	if err := s.saveWebhookStateLocked(); err != nil {
		t.Fatal(err)
	}
	security := defaultSecuritySettings()
	security.TOTPSecret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	security.UpdatedAt = time.Unix(1234, 0).UTC()
	if err := s.persistSecurity(security); err != nil {
		t.Fatal(err)
	}
	credential := webauthnlib.Credential{ID: []byte("fixture-credential"), PublicKey: []byte("fixture-public-key")}
	if err := s.saveWebAuthnState(webAuthnState{Credentials: []webauthnlib.Credential{credential}, EnrolledAt: time.Unix(1234, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	_, requestToken, err := s.secretStore.CreateRequest("fixture-owner-fingerprint", "fixture-secret", "FIXTURE_SECRET", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.secretStore.ConsumeRequest(requestToken, "fixture-sensitive-value"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.issueManagedMCPToken("ordinary", 7, s.cfg.PublicOrigin, s.cfg.MCPResource); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	job := persistedShellTask{ID: "legacy-remote-receipt", Server: "reader", ReceiptTarget: "shell:reader", OwnerID: "original-owner", Caller: &taskCaller{ClientID: "original-client", TokenID: "original-token", Subject: "original-client"}, ToolName: "system_inspect", CreatedAt: now - 3, StartedAt: now - 2, DoneAt: now - 1, Status: "completed", Result: map[string]any{"original": true}}
	peerJob := job
	peerJob.ID = "legacy-88-receipt"
	peerJob.Server = "server88"
	peerJob.ReceiptTarget = "shell:server88"
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		return tx.save(persistedTaskState{Shell: map[string]persistedShellTask{job.ID: job, peerJob.ID: peerJob}})
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOfflineAuthorityPrepareMaterializesActualTarget(t *testing.T) {
	source := offlineAuthorityFixture(t)
	target := authContinuityNode(t, "reader", "writer-identity")
	target.cfg.NodePublicURL = "https://node44.example"
	identity := nodeIdentity{Schema: nodeIdentityTag, NodeID: "member-44", NodeKey: "node44-key", Created: time.Unix(1000, 0).UTC(), WriterID: "writer-identity", WriterURL: "https://hub.example"}
	if err := saveNodeIdentityFile(handoffIdentityPath(target.cfg), identity); err != nil {
		t.Fatal(err)
	}
	token, _, err := source.issueManagedMCPToken("ordinary", 7, source.cfg.PublicOrigin, source.cfg.MCPResource)
	if err != nil {
		t.Fatal(err)
	}
	exported := authContinuityHTTP(source, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
	if exported.Code != 200 {
		t.Fatalf("auth export: %d", exported.Code)
	}
	applied := authContinuityHTTP(target, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", exported.Body.Bytes())
	if applied.Code != 200 {
		t.Fatalf("auth replica seed: %d %s", applied.Code, applied.Body.String())
	}
	target.mu.Lock()
	err = target.saveRegistryStateLocked()
	target.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	sourceCfg, targetCfg := source.cfg, target.cfg
	targetCfg.ExistingMCPBearers = map[string]string{"GPTADMIN_FIXTURE_MCP_BEARER": "stale-target-copy", "GPTADMIN_ONLY_TARGET_MCP_BEARER": "local-only-binding"}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	keyBefore, err := handoffFileDigest(targetCfg.SecretStoreKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	identityBefore, err := handoffFileDigest(handoffIdentityPath(targetCfg))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := ExportStoppedAuthority(sourceCfg, AuthorityHandoffExportOptions{SourceTarget: "shell:writer", ExpectedWriter: "writer-identity", OutputDir: filepath.Join(t.TempDir(), "bundle")})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareStoppedAuthorityWriter(targetCfg, AuthorityHandoffPrepareOptions{BundleDir: receipt.Directory, BundleDigest: receipt.BundleDigest, Target: "shell:reader", ExpectedLocalID: "reader-identity", SourcePeerURL: "https://hub.example", SourceConnectTo: "192.0.2.100", OutputDir: filepath.Join(t.TempDir(), "stage")})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.WriterID != "reader-identity" || prepared.MembershipEpoch == 0 || prepared.Files == 0 {
		t.Fatalf("bad prepare receipt: %+v", prepared)
	}
	writerPlan, err := handoffReadPlan(prepared.Directory, prepared.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if writerPlan.Environment["GPTADMIN_FIXTURE_MCP_BEARER"] != "fixture-configured-bearer" || writerPlan.Environment["GPTADMIN_ONLY_TARGET_MCP_BEARER"] != "" || writerPlan.Environment["GPTADMIN_CTL_ALIAS_MCP_BEARER"] != "" || writerPlan.Environment["GPTADMIN_LEGACY_PUBLIC_ORIGINS"] != "https://legacy-hub.example" {
		t.Fatal("writer public auth environment would replace the transferred identity or lose its aliases")
	}
	stageCfg := handoffProjectConfig(targetCfg, filepath.Join(prepared.Directory, "config"))
	stageCfg.AuthMode = "writer"
	stageCfg.AuthSourceID = ""
	stageCfg.ExistingMCPBearers = nil
	promoted := New(stageCfg)
	defer promoted.Close()
	if err := promoted.RegisterLocalExecutor("reader", map[string]string{"server_id": "reader-identity", "public_key": "reader-public", "fingerprint": "reader-fingerprint"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := promoted.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	if got := authContinuityHTTP(promoted, "GET", "/mcp", token, nil); got.Code == 401 || got.Code == 503 {
		t.Fatalf("prior managed bearer rejected: %d %s", got.Code, got.Body.String())
	}
	if got := authContinuityHTTP(promoted, "GET", "/mcp", "fixture-configured-bearer", nil); got.Code != 200 {
		t.Fatal("configured bearer digest identity did not survive")
	}
	if len(promoted.localExecutors) != 1 || promoted.localExecutors["shell:writer"] != nil || promoted.localExecutors["shell:reader"]["server_id"] != "reader-identity" {
		t.Fatal("physical executor ownership changed")
	}
	if len(promoted.localExecutorTokens) != 2 || promoted.localExecutorTokens["shell:reader"] != strings.Repeat("x", 32) || promoted.localExecutorTokens["shell:remote-enrolled"] != "fixture-remote-self-enrollment-credential" {
		t.Fatal("physical executor or self-enrollment binding changed")
	}
	if len(promoted.agents) < 3 || !promoted.virtualMCP["webhooks"] || promoted.instructionSets["audit"].Content == "" || strings.TrimSpace(promoted.instructionSetSnapshot().Content) != "Authoritative source instructions." {
		t.Fatalf("materialization: agents=%d virtual=%v named=%q default=%q", len(promoted.agents), promoted.virtualMCP["webhooks"], promoted.instructionSets["audit"].Content, promoted.instructionSetSnapshot().Content)
	}
	if promoted.webhookRoutes["fixture-hook"].Token != "fixture-webhook-secret" || len(promoted.webauthnState.Credentials) != 1 || promoted.security.TOTPSecret != "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP" {
		t.Fatal("webhooks, WebAuthn or security state lost")
	}
	secrets, err := promoted.secretStore.quorumAuthorityState()
	if err != nil || len(secrets.Values) != 1 {
		t.Fatal("secret ingress did not survive target re-encryption")
	}
	if promoted.membership.WriterID != "reader-identity" || promoted.membership.Nodes["member-44"].State != nodeStateOffline || !promoted.membership.Nodes["member-44"].LeaseExpires.IsZero() {
		t.Fatal("old enrollment was not retained without an ambiguous active route")
	}
	if promoted.membership.Nodes["member-44"].KeyHash != hashNodeSecret("node44-key") {
		t.Fatal("target enrollment key changed")
	}
	if promoted.membership.Nodes["writer-identity"].KeyHash == "" || promoted.membership.Nodes["writer-identity"].ConnectTo != "192.0.2.100" {
		t.Fatal("former writer reader enrollment/physical route missing")
	}
	keyAfter, _ := handoffFileDigest(targetCfg.SecretStoreKeyFile)
	identityAfter, _ := handoffFileDigest(handoffIdentityPath(targetCfg))
	if keyAfter != keyBefore || identityAfter != identityBefore {
		t.Fatal("prepare modified physical target state")
	}
}

func TestOfflineAuthorityExportActualDomainsAndContent(t *testing.T) {
	s := offlineAuthorityFixture(t)
	options := AuthorityHandoffExportOptions{SourceTarget: "shell:writer", ExpectedWriter: "writer-identity", OutputDir: filepath.Join(t.TempDir(), "bundle")}
	if _, err := ExportStoppedAuthority(s.cfg, options); err == nil || !strings.Contains(err.Error(), "runtime is active") {
		t.Fatalf("active runtime accepted: %v", err)
	}
	store, err := OpenContentStore(filepath.Join(s.cfg.ConfigDir, "exec-content.sqlite"), defaultExecContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, err := store.Index("original-principal", "active-reference", "shell:writer", "durable content fixture\nsecond line")
	if err != nil {
		t.Fatal(err)
	}
	source, err = store.Source("original-principal", source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := handoffFileDigest(filepath.Join(s.cfg.ConfigDir, "registry_state.json"))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := ExportStoppedAuthority(s.cfg, options)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Domains != 11 || receipt.LegacyRecords != 2 || !receipt.ContentCopied {
		t.Fatalf("incomplete export: %+v", receipt)
	}
	bundle, err := readAuthorityHandoffBundle(receipt.Directory, receipt.BundleDigest)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Domains) != 11 || bundle.SourceWriterID != "writer-identity" {
		t.Fatalf("bad bundle metadata")
	}
	var secrets quorumSecretIngressState
	if err := json.Unmarshal(bundle.Domains["secret_ingress/current"], &secrets); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Values) != 1 {
		t.Fatal("ready secret ingress value was lost")
	}
	var registry persistentRegistryState
	if err := json.Unmarshal(bundle.Domains["registry/current"], &registry); err != nil {
		t.Fatal(err)
	}
	if len(registry.LocalExecutorTokens) != 1 || registry.LocalExecutorTokens["shell:remote-enrolled"] != "fixture-remote-self-enrollment-credential" || registry.LocalExecutorTokens["shell:writer"] != "" {
		t.Fatal("self-enrollment binding lost or source physical credential moved")
	}
	copyStore, err := OpenContentStore(filepath.Join(receipt.Directory, "exec-content.sqlite"), defaultExecContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	defer copyStore.Close()
	got, err := copyStore.Source("original-principal", source.ID)
	if err != nil || got.ID != source.ID || got.Origin != source.Origin || !got.ExpiresAt.Equal(source.ExpiresAt) {
		t.Fatalf("content metadata changed: %+v %v", got, err)
	}
	if _, err := copyStore.Source("different-principal", source.ID); err == nil {
		t.Fatal("copied content lost principal isolation")
	}
	hits, err := copyStore.Search("original-principal", source.ID, "durable", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("FTS content snapshot lost its index: %v", err)
	}
	after, err := handoffFileDigest(filepath.Join(s.cfg.ConfigDir, "registry_state.json"))
	if err != nil || after != before {
		t.Fatal("source registry was modified")
	}
	if info, err := os.Stat(filepath.Join(receipt.Directory, "bundle.json")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private bundle permissions are not0600")
	}
}

func TestOfflineAuthorityExportDoesNotClearPending(t *testing.T) {
	s := offlineAuthorityFixture(t)
	now := float64(time.Now().Unix())
	job := persistedShellTask{ID: "unknown-started-operation", Server: "writer", ReceiptTarget: "shell:writer", ToolName: "shell_exec", CreatedAt: now - 2, StartedAt: now - 1, Status: "running"}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		return tx.save(persistedTaskState{Shell: map[string]persistedShellTask{job.ID: job}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := ExportStoppedAuthority(s.cfg, AuthorityHandoffExportOptions{SourceTarget: "shell:writer", ExpectedWriter: "writer-identity", OutputDir: filepath.Join(t.TempDir(), "bundle")})
	if err == nil || !strings.Contains(err.Error(), "nonterminal") {
		t.Fatalf("unknown operation did not block export: %v", err)
	}
	db, err := openTaskReadDatabase(filepath.Join(s.cfg.ConfigDir, taskDatabaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRow(`SELECT json_extract(payload,'$.status') FROM task_records WHERE key=?`, job.ID).Scan(&status); err != nil || status != "running" {
		t.Fatalf("unknown operation was modified: %s %v", status, err)
	}
}

func offlineHandoffReader(t *testing.T, name, localID, nodeID, key string) *Server {
	t.Helper()
	cfg := FromEnv()
	cfg.ConfigDir = t.TempDir()
	cfg.RegistryStateFile = ""
	cfg.StartupInstructionsFile = ""
	cfg.InstructionSetsStateFile = ""
	cfg.VirtualMCPStateFile = ""
	cfg.WebhookConfigFile = ""
	cfg.WebhookStateFile = ""
	cfg.SecurityStateFile = ""
	cfg.SecretStoreDir = ""
	cfg.SecretStoreKeyFile = ""
	cfg.SecretIngressStateFile = ""
	cfg.NodeIdentityFile = ""
	cfg, err := handoffConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg = handoffProjectConfig(cfg, cfg.ConfigDir)
	cfg.AuthMode = "reader"
	cfg.AuthSourceID = "writer-identity"
	cfg.CtlToken = "fixture-owner"
	cfg.OAuthClientSecret = "fixture-signing"
	cfg.PublicOrigin = "https://hub.example"
	cfg.MCPResource = "https://hub.example/mcp"
	cfg.NodePublicURL = "https://" + name + ".example"
	cfg.ExistingMCPBearers = nil
	cfg.OutputDir = filepath.Join(cfg.ConfigDir, "outputs")
	if err := saveNodeIdentityFile(handoffIdentityPath(cfg), nodeIdentity{Schema: nodeIdentityTag, NodeID: nodeID, NodeKey: key, Created: time.Unix(1000, 0).UTC(), WriterID: "writer-identity", WriterURL: "https://hub.example"}); err != nil {
		t.Fatal(err)
	}
	s := New(cfg)
	t.Cleanup(func() { s.Close() })
	if err := s.RegisterLocalExecutor(name, map[string]string{"server_id": localID, "public_key": name + "-public", "fingerprint": name + "-fingerprint"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	return s
}

func offlinePreparedTransitionWire(t *testing.T, name string, prepared AuthorityHandoffPrepared, transitionDigest string, reader bool) {
	t.Helper()
	raw, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(transitionDigest) != 64 || prepared.TransitionDigest != transitionDigest || wire["transition_digest"] != transitionDigest {
		t.Fatalf("%s omitted or changed the verified transition digest in its JSON receipt", name)
	}
	if reader {
		plan, err := handoffReadPlan(prepared.Directory, prepared.PlanDigest)
		if err != nil || plan.Role != "reader" || plan.SourceDigest != transitionDigest {
			t.Fatalf("%s receipt is not bound to its verified reader plan: %v", name, err)
		}
	}
	// These synthetic, directly marshaled outputs can be consumed by the
	// independently implemented ingress validator without constructing fields.
	t.Logf("prepared-transition-wire %s %s", name, raw)
}

func TestOfflineAuthorityApplyAndRetainedReaders(t *testing.T) {
	source := offlineAuthorityFixture(t)
	target := offlineHandoffReader(t, "reader", "reader-identity", "member-44", "node44-key")
	target.cfg.NodePublicURL = "https://node44.example"
	peer := offlineHandoffReader(t, "server88", "server88-identity", "member-88", "node88-key")
	peer.cfg.NodePublicURL = "https://node88.example"
	token, _, err := source.issueManagedMCPToken("ordinary", 7, source.cfg.PublicOrigin, source.cfg.MCPResource)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := authContinuityHTTP(source, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
	content, err := OpenContentStore(filepath.Join(source.cfg.ConfigDir, "exec-content.sqlite"), defaultExecContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	contentRef, err := content.SaveIncompleteFormatted("original-principal", "active-partial", "shell:writer", "stdout", "kept partial bytes")
	if err != nil {
		t.Fatal(err)
	}
	content.Close()
	for _, reader := range []*Server{target, peer} {
		got := authContinuityHTTP(reader, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", snapshot.Body.Bytes())
		if got.Code != 200 {
			t.Fatalf("seed reader: %d %s", got.Code, got.Body.String())
		}
	}
	configs := []Config{source.cfg, target.cfg, peer.cfg}
	for _, node := range []*Server{source, target, peer} {
		if err := node.Close(); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := ExportStoppedAuthority(configs[0], AuthorityHandoffExportOptions{SourceTarget: "shell:writer", ExpectedWriter: "writer-identity", OutputDir: filepath.Join(t.TempDir(), "bundle")})
	if err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(t.TempDir(), "node.env")
	if err := os.WriteFile(envFile, []byte("# preserve unrelated values\nUNRELATED=keep\nGPTADMIN_AUTH_MODE=reader\nGPTADMIN_NODE_WRITER_CONNECT_TO=192.0.2.100\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareStoppedAuthorityWriter(configs[1], AuthorityHandoffPrepareOptions{BundleDir: bundle.Directory, BundleDigest: bundle.BundleDigest, Target: "shell:reader", ExpectedLocalID: "reader-identity", SourcePeerURL: "https://s100-node.example", SourceConnectTo: "192.0.2.100", OutputDir: filepath.Join(t.TempDir(), "writer-stage"), EnvironmentFile: envFile})
	if err != nil {
		t.Fatal(err)
	}
	offlinePreparedTransitionWire(t, "forward_writer", prepared, prepared.TransitionDigest, false)
	originalEnv, _ := os.ReadFile(envFile)
	failedBackup := filepath.Join(t.TempDir(), "interrupted-backup")
	_, crashErr := ApplyStoppedAuthority(configs[1], AuthorityHandoffApplyOptions{PreparedDir: prepared.Directory, PlanDigest: prepared.PlanDigest, BackupDir: failedBackup, RequireEnvironment: true, afterFileWrite: func(path string) error {
		if path == envFile {
			return errors.New("fixture interruption after effective environment")
		}
		return nil
	}})
	if crashErr == nil {
		t.Fatal("crash fixture did not stop before auth commit")
	}
	var pending authSnapshotState
	if err := handoffReadJSON(filepath.Join(configs[1].ConfigDir, "auth-continuity", "state.json"), &pending, false, 4096); err != nil || !pending.Pending {
		t.Fatal("authority admitted between environment and final auth commit")
	}
	if err := restoreStoppedAuthorityBeforeActivation(configs[1], failedBackup, prepared.PlanDigest, func(path string) error {
		if path == envFile {
			return errors.New("fixture interruption during recovery")
		}
		return nil
	}); err == nil {
		t.Fatal("restore crash fixture did not stop")
	}
	if err := handoffReadJSON(filepath.Join(configs[1].ConfigDir, "auth-continuity", "state.json"), &pending, false, 4096); err != nil || !pending.Pending {
		t.Fatal("recovery admitted before restored auth commit")
	}
	if err := RestoreStoppedAuthorityBeforeActivation(configs[1], failedBackup, prepared.PlanDigest); err != nil {
		t.Fatal(err)
	}
	restoredEnv, _ := os.ReadFile(envFile)
	if !bytes.Equal(originalEnv, restoredEnv) {
		t.Fatal("preactivation rollback did not restore effective environment")
	}
	backup := filepath.Join(t.TempDir(), "writer-backup")
	applied, err := ApplyStoppedAuthority(configs[1], AuthorityHandoffApplyOptions{PreparedDir: prepared.Directory, PlanDigest: prepared.PlanDigest, BackupDir: backup, RequireEnvironment: true})
	if err != nil {
		t.Fatal(err)
	}
	if applied.LegacyRecords != 2 || applied.Activated {
		t.Fatal("invalid immutable import receipt")
	}
	envBytes, _ := os.ReadFile(envFile)
	if !bytes.Contains(envBytes, []byte("UNRELATED=keep")) || !bytes.Contains(envBytes, []byte("GPTADMIN_AUTH_MODE=\"writer\"")) {
		t.Fatal("effective environment was not changed/preserved")
	}
	// Activation must share the actual runtime fence with recovery; a stale
	// successful apply receipt alone cannot retire recovery after restoration.
	lock, err := handoffLock(configs[1])
	if err != nil {
		t.Fatal(err)
	}
	markErr := MarkAuthorityActivation(configs[1], backup, prepared.PlanDigest)
	lock.Close()
	if markErr == nil {
		t.Fatal("activation bypassed an occupied runtime lock")
	}
	if err := restoreStoppedAuthorityBeforeActivation(configs[1], backup, prepared.PlanDigest, func(path string) error {
		if path == envFile {
			return errors.New("fixture restore after successful apply")
		}
		return nil
	}); err == nil {
		t.Fatal("second restore fixture did not stop")
	}
	if err := MarkAuthorityActivation(configs[1], backup, prepared.PlanDigest); err == nil {
		t.Fatal("stale applied receipt authorized during restoration")
	}
	if err := RestoreStoppedAuthorityBeforeActivation(configs[1], backup, prepared.PlanDigest); err != nil {
		t.Fatal(err)
	}
	if err := MarkAuthorityActivation(configs[1], backup, prepared.PlanDigest); err == nil {
		t.Fatal("restored receipt authorized stale applied state")
	}
	backup = filepath.Join(t.TempDir(), "committed-writer-backup")
	if _, err := ApplyStoppedAuthority(configs[1], AuthorityHandoffApplyOptions{PreparedDir: prepared.Directory, PlanDigest: prepared.PlanDigest, BackupDir: backup, RequireEnvironment: true}); err != nil {
		t.Fatal(err)
	}
	if err := MarkAuthorityActivation(configs[1], backup, prepared.PlanDigest); err != nil {
		t.Fatal(err)
	}
	if err := RestoreStoppedAuthorityBeforeActivation(configs[1], backup, prepared.PlanDigest); err == nil {
		t.Fatal("stale snapshot rollback allowed after activation authorization")
	}
	writerCfg := configs[1]
	writerCfg.AuthMode = "writer"
	writerCfg.AuthSourceID = ""
	writerCfg.NodeWriterConnectTo = ""
	writerCfg.NodeSeedURL = ""
	writerImage, err := readAuthorityHandoffBundle(bundle.Directory, bundle.BundleDigest)
	if err != nil {
		t.Fatal(err)
	}
	writerCfg.ExistingMCPBearers = writerImage.ConfiguredBearers
	writerCfg.LegacyPublicOrigins = writerImage.PublicAuth.LegacyOrigins
	writerCfg.OAuthAllowedRedirects = writerImage.PublicAuth.AllowedRedirects
	writer := New(writerCfg)
	defer writer.Close()
	if err := writer.RegisterLocalExecutor("reader", map[string]string{"server_id": "reader-identity", "public_key": "reader-public", "fingerprint": "reader-fingerprint"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := writer.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	if got := authContinuityHTTP(writer, "GET", "/mcp", token, nil); got.Code != 200 {
		t.Fatalf("managed bearer continuity: %d", got.Code)
	}
	retainedContent, err := OpenContentStore(filepath.Join(writer.cfg.ConfigDir, "exec-content.sqlite"), defaultExecContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	retainedRef, err := retainedContent.Source("original-principal", contentRef.ID)
	if err != nil || retainedRef.Complete || retainedRef.Origin != "shell:writer" {
		retainedContent.Close()
		t.Fatal("applied partial content reference lost identity/ownership/completeness")
	}
	if _, err := retainedContent.Source("foreign-principal", contentRef.ID); err == nil {
		t.Fatal("applied reference lost principal binding")
	}
	retainedContent.Close()
	request := requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil), map[string]any{"client_id": "original-client", "jti": "original-token", "sub": "original-client"})
	foreign := requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil), map[string]any{"client_id": "other", "jti": "other", "sub": "other"})
	for _, test := range []struct{ id, target string }{{"legacy-remote-receipt", "shell:reader"}, {"legacy-88-receipt", "shell:server88"}} {
		result, rpcErr := writer.mcpTaskGetForRequest(request, test.id, "")
		if rpcErr != nil || firstString(mapValue(result), "resultType") != "complete" {
			t.Fatalf("native old receipt unavailable: %s %v", test.id, rpcErr)
		}
		if _, err := writer.mcpTaskGetForRequest(foreign, test.id, ""); err == nil {
			t.Fatal("immutable import lost original caller ACL")
		}
		w := httptest.NewRecorder()
		if writer.routeNodePeerJob(w, request, map[string]any{"id": test.id, "owner_target": test.target}, []byte(`{"jsonrpc":"2.0","id":1}`)) {
			t.Fatalf("local imported receipt wrongly forwarded/denied: %s %d %s", test.id, w.Code, w.Body.String())
		}
	}
	lastToken := token
	retainedReaderConfigs := make(map[string]Config)
	for index, targetName := range []string{"shell:writer", "shell:server88"} {
		cfg := configs[0]
		localName, localID := "writer", "writer-identity"
		if index == 1 {
			cfg = configs[2]
			localName, localID = "server88", "server88-identity"
		}
		priorIdentity, _ := handoffLoadNodeIdentity(cfg, true)
		readerEnv := filepath.Join(t.TempDir(), "node.env.json")
		if err := os.WriteFile(readerEnv, []byte(`{"GPTADMIN_AUTH_MODE":"reader","GPTADMIN_AUTH_SOURCE_ID":"writer-identity","UNRELATED":"retained-json"}`), 0600); err != nil {
			t.Fatal(err)
		}
		readerStage, err := PrepareStoppedAuthorityReader(cfg, AuthorityReaderPrepareOptions{TransitionFile: filepath.Join(prepared.Directory, "reader-transition.json"), TransitionDigest: prepared.TransitionDigest, Target: targetName, ExpectedLocalID: localID, OutputDir: filepath.Join(t.TempDir(), "reader-stage"), EnvironmentFile: readerEnv})
		if err != nil {
			t.Fatal(err)
		}
		offlinePreparedTransitionWire(t, "forward_"+targetName, readerStage, prepared.TransitionDigest, true)
		readerBackup := filepath.Join(t.TempDir(), "reader-backup")
		if _, err := ApplyStoppedAuthority(cfg, AuthorityHandoffApplyOptions{PreparedDir: readerStage.Directory, PlanDigest: readerStage.PlanDigest, BackupDir: readerBackup, RequireEnvironment: true}); err != nil {
			t.Fatal(err)
		}
		var jsonEnv map[string]string
		if err := handoffReadJSON(readerEnv, &jsonEnv, false, 1<<20); err != nil || jsonEnv["UNRELATED"] != "retained-json" || jsonEnv["GPTADMIN_AUTH_SOURCE_ID"] != "reader-identity" {
			t.Fatal("native JSON environment was not preserved and switched")
		}
		cfg.AuthMode = "reader"
		cfg.AuthSourceID = "reader-identity"
		cfg.NodeWriterConnectTo = ""
		cfg.NodeSeedURL = "https://hub.example"
		cfg.NodePeersJSON = ""
		if index == 0 {
			cfg.NodePublicURL = "https://s100-node.example"
			readerPlan, err := handoffReadPlan(readerStage.Directory, readerStage.PlanDigest)
			if err != nil || readerPlan.Environment["GPTADMIN_NODE_PUBLIC_URL"] != cfg.NodePublicURL {
				t.Fatal("former writer would advertise the new authority URL as its physical endpoint")
			}
		}
		reader := New(cfg)
		if err := reader.RegisterLocalExecutor(localName, map[string]string{"server_id": localID, "public_key": localName + "-public", "fingerprint": localName + "-fingerprint"}, strings.Repeat("x", 32)); err != nil {
			reader.Close()
			t.Fatal(err)
		}
		if err := reader.InitializeAuthContinuity(); err != nil {
			reader.Close()
			t.Fatal(err)
		}
		if priorIdentity != nil && (reader.nodeIdentity.NodeID != priorIdentity.NodeID || reader.nodeIdentity.NodeKey != priorIdentity.NodeKey) {
			t.Fatal("reader immutable identity/key changed")
		}
		if reader.cfg.AuthSourceID != "reader-identity" || reader.nodeIdentity.WriterID != "reader-identity" || reader.authState.WriterID != "reader-identity" || reader.membershipView.WriterID != "reader-identity" {
			t.Fatal("reader writer pin/auth/view was not coherent")
		}
		for _, node := range reader.membershipView.Nodes {
			if node.KeyHash != "" {
				t.Fatal("member view leaked private enrollment hashes")
			}
		}
		if got := authContinuityHTTP(reader, "GET", "/mcp", token, nil); got.Code != 200 {
			t.Fatalf("retained reader bearer failed: %d", got.Code)
		}
		initialSync := httptest.NewServer(http.HandlerFunc(writer.nodeMembershipControlSnapshot))
		reader.membershipApplyControlSnapshot(initialSync.Client(), initialSync.URL)
		initialSync.Close()
		body, _ := json.Marshal(map[string]any{"state": "ready", "storage_ok": true, "snapshot_revision": reader.authState.Generation, "local_id": localID, "local_name": targetName, "local_fingerprint": localName + "-fingerprint", "endpoint": reader.cfg.NodePublicURL})
		heartbeat := httptest.NewRequest(http.MethodPost, "/admin/api/nodes/heartbeat", bytes.NewReader(body))
		heartbeat.Header.Set("Authorization", "Bearer "+reader.nodeIdentity.NodeKey)
		response := httptest.NewRecorder()
		writer.nodeMembershipHeartbeat(response, heartbeat)
		if response.Code != 200 || writer.membership.Nodes[reader.nodeIdentity.NodeID].State != nodeStateReady {
			reader.Close()
			t.Fatalf("retained reader cannot renew readiness: %d %s", response.Code, response.Body.String())
		}
		lastToken, _, err = writer.issueManagedMCPToken("next-generation", 7, writer.cfg.PublicOrigin, writer.cfg.MCPResource)
		if err != nil {
			t.Fatal(err)
		}
		upstream := httptest.NewServer(http.HandlerFunc(writer.nodeMembershipControlSnapshot))
		reader.membershipApplyControlSnapshot(upstream.Client(), upstream.URL)
		upstream.Close()
		if reader.authState.Generation != writer.authState.Generation || reader.authState.Pending {
			reader.Close()
			t.Fatal("reader cannot accept the next ordinary writer generation")
		}
		reader.Close()
		retainedReaderConfigs[targetName] = cfg
	}
	// A post-activation recovery is a fresh reverse transfer, preserving the
	// generations accepted by the new writer instead of loading an old backup.
	previousGeneration := writer.authState.Generation
	writer.Close()
	reverseBundle, err := ExportStoppedAuthority(writerCfg, AuthorityHandoffExportOptions{SourceTarget: "shell:reader", ExpectedWriter: "reader-identity", OutputDir: filepath.Join(t.TempDir(), "reverse-bundle")})
	if err != nil {
		t.Fatal(err)
	}
	formerCfg := configs[0]
	formerCfg.AuthMode = "reader"
	formerCfg.AuthSourceID = "reader-identity"
	formerCfg.NodePublicURL = "https://s100-node.example"
	formerCfg.NodeSeedURL = "https://hub.example"
	reverseStage, err := PrepareStoppedAuthorityWriter(formerCfg, AuthorityHandoffPrepareOptions{BundleDir: reverseBundle.Directory, BundleDigest: reverseBundle.BundleDigest, Target: "shell:writer", ExpectedLocalID: "writer-identity", SourcePeerURL: "https://node44.example", OutputDir: filepath.Join(t.TempDir(), "reverse-stage")})
	if err != nil {
		t.Fatal(err)
	}
	offlinePreparedTransitionWire(t, "reverse_writer", reverseStage, reverseStage.TransitionDigest, false)
	for _, reader := range []struct {
		target, localID string
		cfg             Config
	}{
		{"shell:reader", "reader-identity", writerCfg},
		{"shell:server88", "server88-identity", retainedReaderConfigs["shell:server88"]},
	} {
		reverseEnv := filepath.Join(t.TempDir(), "reverse-reader.env.json")
		if err := handoffWritePrivate(reverseEnv, map[string]string{"GPTADMIN_AUTH_MODE": reader.cfg.AuthMode, "GPTADMIN_AUTH_SOURCE_ID": reader.cfg.AuthSourceID}); err != nil {
			t.Fatal(err)
		}
		options := AuthorityReaderPrepareOptions{TransitionFile: filepath.Join(reverseStage.Directory, "reader-transition.json"), TransitionDigest: reverseStage.TransitionDigest, Target: reader.target, ExpectedLocalID: reader.localID, OutputDir: filepath.Join(t.TempDir(), "reverse-reader-stage"), EnvironmentFile: reverseEnv}
		preparedReader, err := PrepareStoppedAuthorityReader(reader.cfg, options)
		if err != nil {
			t.Fatal(err)
		}
		offlinePreparedTransitionWire(t, "reverse_"+reader.target, preparedReader, reverseStage.TransitionDigest, true)
		options.OutputDir = filepath.Join(t.TempDir(), "wrong-transition-reader")
		options.TransitionDigest = strings.Repeat("0", 64)
		if _, err := PrepareStoppedAuthorityReader(reader.cfg, options); err == nil {
			t.Fatal("reader preparation accepted an unverified transition digest")
		}
	}
	reverseBackup := filepath.Join(t.TempDir(), "reverse-backup")
	if _, err := ApplyStoppedAuthority(formerCfg, AuthorityHandoffApplyOptions{PreparedDir: reverseStage.Directory, PlanDigest: reverseStage.PlanDigest, BackupDir: reverseBackup}); err != nil {
		t.Fatal(err)
	}
	formerCfg.AuthMode = "writer"
	formerCfg.AuthSourceID = ""
	formerCfg.NodeSeedURL = ""
	formerCfg.NodeWriterConnectTo = ""
	formerCfg.ExistingMCPBearers = writerCfg.ExistingMCPBearers
	restoredWriter := New(formerCfg)
	if err := restoredWriter.RegisterLocalExecutor("writer", map[string]string{"server_id": "writer-identity", "public_key": "writer-public", "fingerprint": "writer-fingerprint"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := restoredWriter.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	if restoredWriter.authState.Generation <= previousGeneration {
		t.Fatal("reverse transfer lost post-cutover auth revision")
	}
	for _, bearer := range []string{token, lastToken} {
		if got := authContinuityHTTP(restoredWriter, "GET", "/mcp", bearer, nil); got.Code != 200 {
			t.Fatal("reverse transfer lost an accepted bearer")
		}
	}
	restoredWriter.Close()
	reexport, err := ExportStoppedAuthority(formerCfg, AuthorityHandoffExportOptions{SourceTarget: "shell:writer", ExpectedWriter: "writer-identity", OutputDir: filepath.Join(t.TempDir(), "second-source-bundle")})
	if err != nil {
		t.Fatal(err)
	}
	image, err := readAuthorityHandoffBundle(reexport.Directory, reexport.BundleDigest)
	if err != nil {
		t.Fatal(err)
	}
	var membership *nodeMembershipState
	if err := json.Unmarshal(image.Domains["membership/writer"], &membership); err != nil {
		t.Fatal(err)
	}
	retainedIdentity, err := handoffSourceReader(image, membership, "https://s100-node.example", "")
	if err != nil || image.SourceIdentity == nil || retainedIdentity.NodeKey != image.SourceIdentity.NodeKey {
		t.Fatal("repeated transfer did not retain the former writer's original member key")
	}
}
