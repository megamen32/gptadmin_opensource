package hub

// Offline authority handoff uses the same file formats and validators as the
// Hub. It never starts a Hub, an executor, a task worker, or a listener on the
// source. The local runtime lock is necessary, but the caller must also fence
// the service manager until the handoff is accepted or rolled back.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const authorityHandoffSchema = "gptadmin.authority-handoff/v2"
const authorityHandoffBudget = 64 << 20

// AuthorityHandoffBundle is private. Domains include managed credentials and
// decrypted one-time secret ingress values. Only a digest receipt is public.
type AuthorityHandoffBundle struct {
	Schema            string                          `json:"schema"`
	CreatedAt         time.Time                       `json:"created_at"`
	SourceTarget      string                          `json:"source_target"`
	SourceWriterID    string                          `json:"source_writer_id"`
	SourceIdentity    *nodeIdentity                   `json:"source_identity,omitempty"`
	PublicOrigin      string                          `json:"public_origin"`
	MCPResource       string                          `json:"mcp_resource"`
	SignerFingerprint string                          `json:"signer_fingerprint"`
	PublicAuth        authorityPublicAuthConfig       `json:"public_auth_config"`
	ConfiguredBearers map[string]string               `json:"configured_bearers,omitempty"`
	Domains           map[string]json.RawMessage      `json:"domains"`
	DomainDigests     map[string]string               `json:"domain_digests"`
	ContentDigest     string                          `json:"content_digest,omitempty"`
	LegacyRecords     []quorumTaskRecordRow           `json:"legacy_records,omitempty"`
	LegacyRequests    []quorumTaskRequestRow          `json:"legacy_requests,omitempty"`
	RetainedLegacy    *AuthorityLegacyBaselineReceipt `json:"retained_legacy,omitempty"`
}

type authorityPublicAuthConfig struct {
	KeyID                      string   `json:"key_id"`
	LegacyOrigins              []string `json:"legacy_origins"`
	AllowedRedirects           []string `json:"allowed_redirects"`
	PermissiveRedirects        bool     `json:"permissive_redirects"`
	PermissiveResources        bool     `json:"permissive_resources"`
	RelaxAuthChecks            bool     `json:"relax_auth_checks"`
	InstructionContextRequired bool     `json:"instruction_context_required"`
	InstructionSessionReceipts bool     `json:"instruction_session_receipts"`
	AuthRateLimit              int      `json:"auth_rate_limit"`
	StandaloneAutoTrust        bool     `json:"standalone_auto_trust"`
	DebugLowSecurity           bool     `json:"debug_low_security"`
	AuthLogSecrets             bool     `json:"auth_log_secrets"`
}

func handoffPublicAuthConfig(cfg Config) authorityPublicAuthConfig {
	return authorityPublicAuthConfig{KeyID: cfg.OAuthKeyID, LegacyOrigins: append([]string(nil), cfg.LegacyPublicOrigins...), AllowedRedirects: append([]string(nil), cfg.OAuthAllowedRedirects...), PermissiveRedirects: cfg.OAuthPermissiveRedirects, PermissiveResources: cfg.OAuthPermissiveResources, RelaxAuthChecks: cfg.RelaxAuthChecks, InstructionContextRequired: cfg.InstructionContextRequired, InstructionSessionReceipts: cfg.InstructionSessionReceipts, AuthRateLimit: cfg.AuthRateLimit, StandaloneAutoTrust: cfg.StandaloneAutoTrust, DebugLowSecurity: cfg.DebugLowSecurity, AuthLogSecrets: cfg.AuthLogSecrets}
}

type AuthorityHandoffExportOptions struct {
	SourceTarget         string
	ExpectedWriter       string
	OutputDir            string
	PhysicalIdentityFile string
	LegacyBaselineFile   string
	LegacyBaselineDigest string
}

type AuthorityHandoffReceipt struct {
	Schema         string                          `json:"schema"`
	Directory      string                          `json:"directory"`
	BundleDigest   string                          `json:"bundle_digest"`
	SourceWriter   string                          `json:"source_writer"`
	Domains        int                             `json:"domains"`
	LegacyRecords  int                             `json:"legacy_records"`
	ContentCopied  bool                            `json:"content_copied"`
	RetainedLegacy *AuthorityLegacyBaselineReceipt `json:"retained_legacy,omitempty"`
}

// Baselines contain hashes, not task bodies. They preserve immutable records
// which already lived on the promoted physical node before the forward handoff.
// Reverse transfer must leave every exact baseline record there, available via
// its existing authenticated /admin/api/tasks/{id} operator history path.
type authorityLegacyBaselineRow struct {
	Kind          string `json:"kind"`
	Key           string `json:"key"`
	Revision      int64  `json:"revision"`
	RecordDigest  string `json:"record_sha256"`
	RequestKey    string `json:"request_key,omitempty"`
	RequestDigest string `json:"request_sha256,omitempty"`
}
type authorityLegacyBaseline struct {
	Schema                 string                       `json:"schema"`
	SourceTarget           string                       `json:"source_target"`
	LocalID                string                       `json:"local_id"`
	PhysicalIdentityDigest string                       `json:"physical_identity_sha256"`
	CreatedAt              time.Time                    `json:"created_at"`
	Rows                   []authorityLegacyBaselineRow `json:"rows"`
	retained               int
}
type AuthorityLegacyBaselineReceipt struct {
	File          string `json:"file"`
	Digest        string `json:"sha256"`
	Records       int    `json:"records"`
	PhysicalOwner string `json:"physical_owner"`
	LookupPath    string `json:"lookup_path"`
}

func handoffLegacyRowDigest(row quorumTaskRecordRow) string {
	raw, _ := json.Marshal(row)
	return handoffDigest(raw)
}
func handoffLegacyRequest(tx *sql.Tx, task persistedShellTask) (*quorumTaskRequestRow, error) {
	if task.RequestKey == "" {
		return nil, nil
	}
	var req quorumTaskRequestRow
	if err := tx.QueryRow(`SELECT key,fingerprint,owner,created,job_id,response,status FROM task_requests WHERE key=?`, task.RequestKey).Scan(&req.Key, &req.Fingerprint, &req.Owner, &req.Created, &req.JobID, &req.Response, &req.Status); err != nil {
		return nil, err
	}
	if req.JobID != task.ID {
		return nil, errors.New("legacy idempotency owner mismatch")
	}
	return &req, nil
}
func handoffLegacyBaselineRow(tx *sql.Tx, row quorumTaskRecordRow) (authorityLegacyBaselineRow, error) {
	var task persistedShellTask
	if err := json.Unmarshal(row.Payload, &task); err != nil {
		return authorityLegacyBaselineRow{}, err
	}
	if row.Kind != "shell" || row.Key != task.ID || row.Revision <= 0 || row.Done <= 0 || task.DoneAt != row.Done || len(task.SealedPending) != 0 {
		return authorityLegacyBaselineRow{}, errors.New("baseline requires immutable terminal task")
	}
	if task.Status != "completed" && task.Status != "failed" && task.Status != "cancelled" && task.Status != "expired" {
		return authorityLegacyBaselineRow{}, errors.New("baseline contains nonterminal task")
	}
	x := authorityLegacyBaselineRow{Kind: row.Kind, Key: row.Key, Revision: row.Revision, RecordDigest: handoffLegacyRowDigest(row)}
	req, err := handoffLegacyRequest(tx, task)
	if err != nil {
		return x, err
	}
	if req != nil {
		raw, _ := json.Marshal(req)
		x.RequestKey = req.Key
		x.RequestDigest = handoffDigest(raw)
	}
	return x, nil
}

// CaptureStoppedLegacyBaseline has no database write, prune, or import path.
// It is called under the actual node runtime lock before the forward handoff.
func CaptureStoppedLegacyBaseline(cfg Config, target, localID, physical, output string) (AuthorityLegacyBaselineReceipt, error) {
	var receipt AuthorityLegacyBaselineReceipt
	var err error
	cfg, err = handoffConfig(cfg)
	if err != nil {
		return receipt, err
	}
	lock, err := handoffLock(cfg)
	if err != nil {
		return receipt, err
	}
	defer lock.Close()
	if physical == "" {
		return receipt, errors.New("retained baseline requires actual physical identity")
	}
	identity, _, err := handoffLocalExecutor(cfg, target, localID, physical)
	if err != nil {
		return receipt, err
	}
	identityBytes, err := json.Marshal(identity)
	if err != nil {
		return receipt, err
	}
	identityDigest := handoffDigest(identityBytes)
	if !filepath.IsAbs(output) {
		return receipt, errors.New("baseline output must be absolute")
	}
	image := authorityLegacyBaseline{Schema: "gptadmin.legacy-retained-baseline/v1", SourceTarget: target, LocalID: localID, PhysicalIdentityDigest: identityDigest, CreatedAt: time.Now().UTC()}
	db, err := openTaskReadDatabase(filepath.Join(cfg.ConfigDir, taskDatabaseFilename))
	if err != nil {
		return receipt, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	var pending, controls int
	if err := tx.QueryRow(`SELECT count(*) FROM task_records WHERE kind IN ('shell','relay') AND coalesce(json_extract(payload,'$.status'),'') NOT IN ('completed','failed','cancelled','expired')`).Scan(&pending); err != nil {
		return receipt, err
	}
	if err := tx.QueryRow(`SELECT count(*) FROM task_controls`).Scan(&controls); err != nil {
		return receipt, err
	}
	if pending != 0 || controls != 0 {
		return receipt, errors.New("baseline requires fully drained task ledger")
	}
	rows, err := tx.Query(`SELECT kind,key,updated,done,payload,revision FROM task_records WHERE kind='shell' AND json_extract(payload,'$.server')<>? ORDER BY key`, strings.TrimPrefix(target, "shell:"))
	if err != nil {
		return receipt, err
	}
	for rows.Next() {
		var row quorumTaskRecordRow
		if err := rows.Scan(&row.Kind, &row.Key, &row.Updated, &row.Done, &row.Payload, &row.Revision); err != nil {
			rows.Close()
			return receipt, err
		}
		x, err := handoffLegacyBaselineRow(tx, row)
		if err != nil {
			rows.Close()
			return receipt, err
		}
		image.Rows = append(image.Rows, x)
		if len(image.Rows) > 65536 {
			rows.Close()
			return receipt, errors.New("retained baseline metadata budget exceeded")
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return receipt, err
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		return receipt, err
	}
	if err := handoffWritePrivate(output, image); err != nil {
		return receipt, err
	}
	hash, err := handoffFileDigest(output)
	if err != nil {
		return receipt, err
	}
	return AuthorityLegacyBaselineReceipt{File: output, Digest: hash, Records: len(image.Rows), PhysicalOwner: target, LookupPath: "/admin/api/tasks/{id}"}, nil
}

func handoffDigest(data []byte) string {
	d := sha256.Sum256(data)
	return hex.EncodeToString(d[:])
}

func handoffDomainDigest(raw json.RawMessage) string {
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return ""
	}
	return handoffDigest(compact.Bytes())
}

func handoffFileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func handoffPrivateOutput(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("handoff output must be absolute")
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return err
	}
	return handoffSyncParent(path)
}

func handoffSyncParent(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func handoffReadJSON(path string, dst any, optional bool, limit int) error {
	info, err := os.Lstat(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > int64(limit) {
		return fmt.Errorf("invalid bounded state file %s", filepath.Base(path))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

func handoffWritePrivate(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > authorityHandoffBudget {
		return errors.New("handoff document exceeds budget")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return handoffSyncParent(path)
}

func handoffConfig(cfg Config) (Config, error) {
	root, err := filepath.Abs(cfg.ConfigDir)
	if err != nil || cfg.ConfigDir == "" {
		return cfg, errors.New("config directory is required")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return cfg, errors.New("config directory must be a real directory")
	}
	cfg.ConfigDir = root
	paths := []*string{&cfg.RegistryStateFile, &cfg.StartupInstructionsFile, &cfg.InstructionSetsStateFile, &cfg.VirtualMCPStateFile, &cfg.WebhookConfigFile, &cfg.WebhookStateFile, &cfg.SecurityStateFile, &cfg.SecretStoreDir, &cfg.SecretStoreKeyFile, &cfg.SecretIngressStateFile}
	defaults := []string{"registry_state.json", "startup_instructions.md", instructionSetsStateFilename, virtualMCPStateFilename, "webhooks.json", "webhook_state.json", securityStateFilename, "secrets", "secret-store.key", "secrets/requests.json"}
	for i, p := range paths {
		if *p == "" {
			*p = filepath.Join(root, defaults[i])
		}
		abs, err := filepath.Abs(*p)
		if err != nil {
			return cfg, err
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return cfg, fmt.Errorf("authority path must be inside config directory: %s", defaults[i])
		}
		*p = abs
	}
	if cfg.AuthSnapshotBudget == 0 {
		cfg.AuthSnapshotBudget = authSnapshotDefaultBudget
	}
	if cfg.AuthSnapshotReserveBytes == 0 {
		cfg.AuthSnapshotReserveBytes = authSnapshotDefaultReserve
	}
	return cfg, nil
}

func handoffLock(cfg Config) (*flock.Flock, error) {
	path := filepath.Join(cfg.ConfigDir, "auth-continuity", "runtime.lock")
	if err := regularAuthPath(path, false); err != nil {
		return nil, err
	}
	lock := flock.New(path)
	held, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !held {
		_ = lock.Close()
		return nil, errors.New("authority runtime is active; stop and fence it before handoff")
	}
	return lock, nil
}

func handoffReadSecrets(cfg Config) (quorumSecretIngressState, error) {
	if err := regularAuthPath(cfg.SecretStoreKeyFile, false); err != nil {
		return quorumSecretIngressState{}, err
	}
	key, err := os.ReadFile(cfg.SecretStoreKeyFile)
	if err != nil {
		return quorumSecretIngressState{}, err
	}
	defer zeroBytes(key)
	if len(key) != secretKeySize {
		return quorumSecretIngressState{}, ErrSecretStoreCorrupt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return quorumSecretIngressState{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return quorumSecretIngressState{}, err
	}
	state := persistedSecretState{Requests: map[string]persistedSecretRequest{}}
	if err := handoffReadJSON(cfg.SecretIngressStateFile, &state, true, 16<<20); err != nil {
		return quorumSecretIngressState{}, err
	}
	if state.Requests == nil {
		state.Requests = map[string]persistedSecretRequest{}
	}
	for ref, request := range state.Requests {
		if ref == "" || strings.ContainsAny(ref, "/\\") || request.Ref != ref || request.TokenHash == "" || request.OwnerFingerprint == "" || request.Label == "" || !validSecretEnvName.MatchString(request.EnvName) || (request.Status != "pending" && request.Status != "ready") {
			return quorumSecretIngressState{}, ErrSecretStoreCorrupt
		}
		if request.Status == "ready" {
			if err := regularAuthPath(filepath.Join(cfg.SecretStoreDir, ref+".json"), false); err != nil {
				return quorumSecretIngressState{}, err
			}
		}
	}
	store := &SecretStore{configDir: cfg.ConfigDir, storeDir: cfg.SecretStoreDir, keyFile: cfg.SecretStoreKeyFile, stateFile: cfg.SecretIngressStateFile, cipher: aead, requests: state.Requests, now: cfg.Now}
	return store.quorumAuthorityState()
}

func handoffReadDomains(cfg Config, expectedWriter, sourceTarget string) (map[string]json.RawMessage, error) {
	values := map[string]json.RawMessage{}
	put := func(name string, v any) error {
		b, err := json.Marshal(v)
		if err == nil {
			values[name] = b
		}
		return err
	}
	s := &Server{cfg: cfg}
	var state authSnapshotState
	if err := handoffReadJSON(s.authStatePath(), &state, false, 4096); err != nil {
		return nil, err
	}
	if state.WriterID != expectedWriter || state.Generation == 0 || state.Pending || state.ReadOnly || state.Slot < 0 || state.Slot > 1 {
		return nil, errors.New("source is not the expected complete writer generation")
	}
	auth, err := s.readAuthSlot(state)
	if err != nil {
		return nil, err
	}
	s.excludeSnapshotControlCredentials(&auth)
	named, err := readInstructionSetsState(cfg.InstructionSetsStateFile)
	if err != nil {
		return nil, err
	}
	if err := validateAuthSnapshotValues(auth, cfg.AuthSnapshotBudget, cfg.CtlToken, func(id string) bool { _, ok := named[id]; return id == "" || id == defaultInstructionSetID || ok }); err != nil {
		return nil, err
	}
	if err := put("auth/bundle", auth); err != nil {
		return nil, err
	}
	if err := put("instruction_sets_named/current", instructionSetsState{Sets: named}); err != nil {
		return nil, err
	}
	content, inline := effectiveInlineStartupInstructions(cfg.StartupInstructions)
	if !inline {
		raw, err := os.ReadFile(cfg.StartupInstructionsFile)
		if err != nil {
			return nil, err
		}
		content = string(raw)
	}
	if err := validateInstructionContent(content); err != nil {
		return nil, err
	}
	if err := put("instruction_sets_default/current", InstructionSet{ID: defaultInstructionSetID, Content: content, Version: instructionSetVersion(content)}); err != nil {
		return nil, err
	}
	member, err := loadNodeMembershipState(s.membershipStatePath())
	if err != nil {
		return nil, err
	}
	if member.WriterID != expectedWriter || member.Revision == 0 || member.Nodes[expectedWriter] == nil || member.Nodes[expectedWriter].Name != sourceTarget {
		return nil, errors.New("source writer membership/physical target mismatch")
	}
	if err := put("membership/writer", member); err != nil {
		return nil, err
	}
	var registry persistentRegistryState
	if err := handoffReadJSON(cfg.RegistryStateFile, &registry, false, 16<<20); err != nil {
		return nil, err
	}
	if registry.Agents[sourceTarget].Meta["server_id"] != expectedWriter {
		return nil, errors.New("source executor identity does not match writer")
	}
	// Persisted self-enrollment bindings are writer authority. The ordinary
	// in-process executor token is ephemeral and is never moved to another host.
	for id := range registry.LocalExecutorTokens {
		agent, ok := registry.Agents[id]
		if !ok {
			return nil, errors.New("credential binding has no enrolled agent")
		}
		if truthy(agent.Meta["self_enrolled"]) {
			continue
		}
		if id == sourceTarget && truthy(agent.Meta["local_executor"]) {
			delete(registry.LocalExecutorTokens, id)
			continue
		}
		return nil, errors.New("credential binding is neither a self-enrollment nor the source physical executor")
	}
	if err := put("registry/current", registry); err != nil {
		return nil, err
	}
	virtual := virtualMCPState{Enabled: map[string]bool{}}
	if err := handoffReadJSON(cfg.VirtualMCPStateFile, &virtual, true, 1<<20); err != nil {
		return nil, err
	}
	for id, enabled := range virtual.Enabled {
		if _, ok := virtualMCPDefinitions[id]; !ok && enabled {
			return nil, fmt.Errorf("invalid virtual MCP %s", id)
		}
	}
	if err := put("virtual_mcp/current", virtual); err != nil {
		return nil, err
	}
	routes, err := loadWebhookRoutes(cfg.WebhookConfigFile)
	if err != nil {
		return nil, err
	}
	routes = append(routes, cfg.WebhookRoutes...)
	if err := validateWebhookRoutes(routes); err != nil {
		return nil, err
	}
	if err := put("webhook_routes/current", webhookRouteMap(routes)); err != nil {
		return nil, err
	}
	webhooks := persistedWebhookState{Jobs: map[string]*webhookJob{}}
	if err := handoffReadJSON(cfg.WebhookStateFile, &webhooks, true, 16<<20); err != nil {
		return nil, err
	}
	if err := put("webhooks/current", webhooks); err != nil {
		return nil, err
	}
	security, err := loadSecuritySettings(cfg.SecurityStateFile, firstNonEmpty(cfg.AdminPassword, cfg.OAuthClientSecret, cfg.CtlToken, "gptadmin-security-state"))
	if err != nil {
		return nil, err
	}
	if err := put("security/settings", security); err != nil {
		return nil, err
	}
	// The ordinary WebAuthn loader chmods its input. Offline export validates
	// the identical persisted shape without changing source permissions.
	webauthn := defaultWebAuthnState()
	if err := handoffReadJSON(filepath.Join(cfg.ConfigDir, webAuthnStateFilename), &webauthn, true, securityStateMaxBytes); err != nil {
		return nil, err
	}
	if len(webauthn.Credentials) > webAuthnMaxCredentials {
		return nil, errors.New("too many WebAuthn credentials")
	}
	for _, credential := range webauthn.Credentials {
		if len(credential.ID) == 0 || len(credential.PublicKey) == 0 {
			return nil, errors.New("invalid WebAuthn credential")
		}
	}
	if err := put("webauthn/credentials", webauthn); err != nil {
		return nil, err
	}
	secrets, err := handoffReadSecrets(cfg)
	if err != nil {
		return nil, err
	}
	if err := put("secret_ingress/current", secrets); err != nil {
		return nil, err
	}
	return values, nil
}

// handoffReadLegacy does not prune, reset, or decrypt pending jobs. Every
// admitted nonterminal record blocks the source export. Legacy terminal rows
// whose execution target differed from the old controller are captured intact
// because their original owner hint cannot locate their physical storage.
func handoffReadLegacy(cfg Config, sourceTarget string, baselines ...*authorityLegacyBaseline) ([]quorumTaskRecordRow, []quorumTaskRequestRow, error) {
	var baseline *authorityLegacyBaseline
	if len(baselines) > 0 {
		baseline = baselines[0]
	}
	baselineRows := map[string]authorityLegacyBaselineRow{}
	seen := map[string]bool{}
	if baseline != nil {
		for _, row := range baseline.Rows {
			if row.Kind != "shell" || row.Key == "" || row.RecordDigest == "" || baselineRows[row.Key].Key != "" {
				return nil, nil, errors.New("invalid retained baseline identity")
			}
			baselineRows[row.Key] = row
		}
	}
	path := filepath.Join(cfg.ConfigDir, taskDatabaseFilename)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if len(baselineRows) > 0 {
			return nil, nil, errors.New("retained baseline task ledger disappeared")
		}
		return nil, nil, nil
	}
	db, err := openTaskReadDatabase(path)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var pending, controls int
	if err := tx.QueryRow(`SELECT count(*) FROM task_records WHERE kind IN ('shell','relay') AND coalesce(json_extract(payload,'$.status'),'') NOT IN ('completed','failed','cancelled','expired')`).Scan(&pending); err != nil {
		return nil, nil, err
	}
	if err := tx.QueryRow(`SELECT count(*) FROM task_controls`).Scan(&controls); err != nil {
		return nil, nil, err
	}
	if pending != 0 || controls != 0 {
		return nil, nil, fmt.Errorf("source has %d nonterminal task records and %d controls; drain without clearing them", pending, controls)
	}
	rows, err := tx.Query(`SELECT kind,key,updated,done,payload,revision FROM task_records WHERE kind='shell' AND json_extract(payload,'$.server')<>? ORDER BY key`, strings.TrimPrefix(sourceTarget, "shell:"))
	if err != nil {
		return nil, nil, err
	}
	var records []quorumTaskRecordRow
	for rows.Next() {
		var row quorumTaskRecordRow
		if err := rows.Scan(&row.Kind, &row.Key, &row.Updated, &row.Done, &row.Payload, &row.Revision); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if old, ok := baselineRows[row.Key]; ok {
			seen[row.Key] = true
			current, err := handoffLegacyBaselineRow(tx, row)
			if err != nil {
				rows.Close()
				return nil, nil, err
			}
			if current == old {
				baseline.retained++
				continue
			}
		}
		records = append(records, row)
		if len(records) > 4096 {
			rows.Close()
			return nil, nil, errors.New("legacy terminal receipt budget exceeded")
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if len(seen) != len(baselineRows) {
		return nil, nil, errors.New("a retained baseline receipt disappeared or changed physical ownership")
	}
	var requests []quorumTaskRequestRow
	for _, row := range records {
		var task persistedShellTask
		if err := json.Unmarshal(row.Payload, &task); err != nil {
			return nil, nil, err
		}
		if task.ID != row.Key || len(task.SealedPending) > 0 || task.DoneAt <= 0 {
			return nil, nil, errors.New("legacy receipt is not immutable terminal state")
		}
		req, err := handoffLegacyRequest(tx, task)
		if err != nil {
			return nil, nil, err
		}
		if req != nil {
			requests = append(requests, *req)
		}
	}
	return records, requests, tx.Commit()
}

// VACUUM INTO creates a consistent SQLite snapshot, including committed WAL
// pages. It runs against a read-only connection and never copies a live .sqlite
// file without its WAL. The destination must not exist.
func handoffSQLiteSnapshot(source, destination string) error {
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("SQLite snapshot destination already exists")
	}
	u, err := sqliteFileURI(source)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(10000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`VACUUM main INTO ?`, destination); err != nil {
		return err
	}
	return os.Chmod(destination, 0600)
}

// Logical equality is used only to retain an existing target content store.
// SQLite page layout/header/WAL differences do not imply different references.
// No rows are merged and no existing SQLite/WAL file is replaced in this case.
func handoffContentDigest(path string, now int64) (string, error) {
	state, err := handoffContentState(path, now)
	return state.Digest, err
}

type authorityHandoffContentState struct {
	Digest  string
	Sources map[string]string
}

// Per-source hashes use the same ordered, typed rows as the complete digest.
// Each row includes its table identity and duplicates remain significant.
// Both views share one read transaction and the existing total row budget.
func handoffContentState(path string, now int64) (authorityHandoffContentState, error) {
	var state authorityHandoffContentState
	db, err := openTaskReadDatabase(path)
	if err != nil {
		return state, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	h := sha256.New()
	encoder := json.NewEncoder(h)
	sources := map[string]hash.Hash{}
	queries := []string{
		`SELECT id,principal,name,origin,format,bytes,chunks,created,expires,indexed,complete FROM content_sources WHERE expires>? ORDER BY id`,
		`SELECT c.source_id,c.principal,c.chunk_id,c.line_start,c.line_end,c.body FROM content_chunks c JOIN content_sources s ON s.id=c.source_id WHERE s.expires>? ORDER BY c.source_id,c.chunk_id`,
		`SELECT f.source_id,f.principal,CAST(f.chunk_id AS INTEGER),f.body FROM content_fts f JOIN content_sources s ON s.id=f.source_id WHERE s.expires>? ORDER BY f.source_id,CAST(f.chunk_id AS INTEGER),f.principal,f.body`,
	}
	rowsRead := 0
	for index, query := range queries {
		if err := encoder.Encode(index); err != nil {
			return state, err
		}
		rows, err := tx.Query(query, now)
		if err != nil {
			return state, err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return state, err
		}
		for rows.Next() {
			rowsRead++
			if rowsRead > 100000 {
				rows.Close()
				return state, errors.New("content comparison exceeds bounded row budget")
			}
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return state, err
			}
			if err := encoder.Encode(values); err != nil {
				rows.Close()
				return state, err
			}
			id, ok := values[0].(string)
			if !ok || id == "" {
				rows.Close()
				return state, errors.New("content source identity is not nonempty text")
			}
			if index == 0 {
				if sources[id] != nil {
					rows.Close()
					return state, errors.New("duplicate content source identity")
				}
				sources[id] = sha256.New()
			}
			if sources[id] == nil {
				rows.Close()
				return state, errors.New("content row has no active source identity")
			}
			sourceEncoder := json.NewEncoder(sources[id])
			if err := sourceEncoder.Encode(index); err != nil {
				rows.Close()
				return state, err
			}
			if err := sourceEncoder.Encode(values); err != nil {
				rows.Close()
				return state, err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return state, err
		}
		rows.Close()
	}
	if err := tx.Commit(); err != nil {
		return state, err
	}
	state.Digest = hex.EncodeToString(h.Sum(nil))
	state.Sources = make(map[string]string, len(sources))
	for id, digest := range sources {
		state.Sources[id] = hex.EncodeToString(digest.Sum(nil))
	}
	return state, nil
}

// ExportStoppedAuthority is a supported read-only source operation. Its output
// directory is new and private. The source runtime lock remains held through
// all eleven domains, receipt inspection and content snapshot creation.
func ExportStoppedAuthority(cfg Config, options AuthorityHandoffExportOptions) (AuthorityHandoffReceipt, error) {
	var receipt AuthorityHandoffReceipt
	var err error
	cfg, err = handoffConfig(cfg)
	if err != nil {
		return receipt, err
	}
	if cfg.AuthMode != "writer" {
		return receipt, errors.New("source service configuration is not writer mode")
	}
	if options.ExpectedWriter == "" || !strings.HasPrefix(options.SourceTarget, "shell:") || cfg.CtlToken == "" || cfg.OAuthClientSecret == "" {
		return receipt, errors.New("explicit source identity, control environment and signer are required")
	}
	output, err := filepath.Abs(options.OutputDir)
	if err != nil {
		return receipt, err
	}
	rel, err := filepath.Rel(cfg.ConfigDir, output)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
		return receipt, errors.New("handoff bundle must be outside source config")
	}
	lock, err := handoffLock(cfg)
	if err != nil {
		return receipt, err
	}
	defer lock.Close()
	localProof, _, err := handoffLocalExecutor(cfg, options.SourceTarget, options.ExpectedWriter, options.PhysicalIdentityFile)
	if err != nil {
		return receipt, err
	}
	values, err := handoffReadDomains(cfg, options.ExpectedWriter, options.SourceTarget)
	if err != nil {
		return receipt, err
	}
	var publicAuth authSnapshot
	if err := json.Unmarshal(values["auth/bundle"], &publicAuth); err != nil {
		return receipt, err
	}
	configured := map[string]string{}
	for name, value := range cfg.ExistingMCPBearers {
		if !validSecretEnvName.MatchString(name) || !strings.HasPrefix(name, "GPTADMIN_") || !strings.HasSuffix(name, "_MCP_BEARER") {
			return receipt, errors.New("configured bearer has an invalid environment binding")
		}
		record, ok := publicAuth.Tokens.Tokens[configuredMCPBearerID(name)]
		if value != cfg.CtlToken && ok && record.TokenKind == configuredMCPBearerTokenKind && record.TokenDigest == configuredMCPBearerDigest(value) {
			configured[name] = value
		}
	}
	identity, err := handoffLoadNodeIdentity(cfg, true)
	if err != nil {
		return receipt, err
	}
	var baseline *authorityLegacyBaseline
	var retained *AuthorityLegacyBaselineReceipt
	if options.LegacyBaselineFile != "" || options.LegacyBaselineDigest != "" {
		hash, err := handoffFileDigest(options.LegacyBaselineFile)
		if err != nil || hash != options.LegacyBaselineDigest {
			return receipt, errors.New("retained baseline digest mismatch")
		}
		baseline = &authorityLegacyBaseline{}
		if err := handoffReadJSON(options.LegacyBaselineFile, baseline, false, 16<<20); err != nil {
			return receipt, err
		}
		if options.PhysicalIdentityFile == "" {
			return receipt, errors.New("retained baseline requires actual physical identity")
		}
		physicalBytes, err := json.Marshal(localProof)
		if err != nil {
			return receipt, err
		}
		physicalHash := handoffDigest(physicalBytes)
		if baseline.Schema != "gptadmin.legacy-retained-baseline/v1" || baseline.SourceTarget != options.SourceTarget || baseline.LocalID != options.ExpectedWriter || baseline.PhysicalIdentityDigest != physicalHash || len(baseline.Rows) > 65536 {
			return receipt, errors.New("retained baseline physical identity mismatch")
		}
	}
	records, requests, err := handoffReadLegacy(cfg, options.SourceTarget, baseline)
	if err != nil {
		return receipt, err
	}
	if baseline != nil {
		retained = &AuthorityLegacyBaselineReceipt{File: options.LegacyBaselineFile, Digest: options.LegacyBaselineDigest, Records: baseline.retained, PhysicalOwner: options.SourceTarget, LookupPath: "/admin/api/tasks/{id}"}
	}
	if err := handoffValidateLegacy(records, requests); err != nil {
		return receipt, err
	}
	if err := handoffPrivateOutput(output); err != nil {
		return receipt, err
	}
	bundle := AuthorityHandoffBundle{Schema: authorityHandoffSchema, CreatedAt: time.Now().UTC(), SourceTarget: options.SourceTarget, SourceWriterID: options.ExpectedWriter, SourceIdentity: identity, PublicOrigin: cfg.PublicOrigin, MCPResource: cfg.MCPResource, SignerFingerprint: handoffDigest([]byte(cfg.OAuthClientSecret)), PublicAuth: handoffPublicAuthConfig(cfg), ConfiguredBearers: configured, Domains: values, DomainDigests: map[string]string{}, LegacyRecords: records, LegacyRequests: requests, RetainedLegacy: retained}
	for name, value := range values {
		bundle.DomainDigests[name] = handoffDomainDigest(value)
	}
	content := filepath.Join(cfg.ConfigDir, "exec-content.sqlite")
	if _, err := os.Stat(content); err == nil {
		if err := handoffSQLiteSnapshot(content, filepath.Join(output, "exec-content.sqlite")); err != nil {
			return receipt, err
		}
		bundle.ContentDigest, err = handoffFileDigest(filepath.Join(output, "exec-content.sqlite"))
		if err != nil {
			return receipt, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return receipt, err
	}
	if err := handoffWritePrivate(filepath.Join(output, "bundle.json"), bundle); err != nil {
		return receipt, err
	}
	digest, err := handoffFileDigest(filepath.Join(output, "bundle.json"))
	if err != nil {
		return receipt, err
	}
	receipt = AuthorityHandoffReceipt{Schema: authorityHandoffSchema, Directory: output, BundleDigest: digest, SourceWriter: options.ExpectedWriter, Domains: len(values), LegacyRecords: len(records), ContentCopied: bundle.ContentDigest != "", RetainedLegacy: retained}
	return receipt, nil
}

func readAuthorityHandoffBundle(directory, expectedDigest string) (AuthorityHandoffBundle, error) {
	var bundle AuthorityHandoffBundle
	path := filepath.Join(directory, "bundle.json")
	digest, err := handoffFileDigest(path)
	if err != nil {
		return bundle, err
	}
	if expectedDigest == "" || digest != expectedDigest {
		return bundle, errors.New("handoff bundle digest differs from reviewed receipt")
	}
	if err := handoffReadJSON(path, &bundle, false, authorityHandoffBudget); err != nil {
		return bundle, err
	}
	if bundle.Schema != authorityHandoffSchema || len(bundle.Domains) != len(quorumRequiredAuthorityRecords) || len(bundle.DomainDigests) != len(bundle.Domains) {
		return bundle, errors.New("incomplete authority handoff bundle")
	}
	for namespace, key := range quorumRequiredAuthorityRecords {
		name := namespace + "/" + key
		raw, ok := bundle.Domains[name]
		if !ok || handoffDomainDigest(raw) != bundle.DomainDigests[name] {
			return bundle, fmt.Errorf("authority domain digest mismatch: %s", name)
		}
	}
	if bundle.ContentDigest != "" {
		digest, err := handoffFileDigest(filepath.Join(directory, "exec-content.sqlite"))
		if err != nil || digest != bundle.ContentDigest {
			return bundle, errors.New("content snapshot digest mismatch")
		}
	}
	return bundle, nil
}

// These helpers deliberately keep equality based on exact bytes for immutable
// receipts. A hash collision or same ID with different owner/caller is not a
// last-write-wins migration decision.
func equalHandoffRecord(a, b quorumTaskRecordRow) bool {
	return a.Kind == b.Kind && a.Key == b.Key && a.Updated == b.Updated && a.Done == b.Done && a.Revision == b.Revision && bytes.Equal(a.Payload, b.Payload)
}
func sortedHandoffDomainNames(values map[string]json.RawMessage) []string {
	out := make([]string, 0, len(values))
	for k := range values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// InspectAuthorityConfiguration is a live read-only compatibility preflight.
// It emits metadata only and does not claim a consistent stopped export. It is
// useful before the maintenance window so unsupported paths/formats fail early.
func InspectAuthorityConfiguration(cfg Config, target, expectedLocalID string, physicalPaths ...string) (map[string]any, error) {
	var err error
	cfg, err = handoffConfig(cfg)
	if err != nil {
		return nil, err
	}
	local, _, err := handoffLocalExecutor(cfg, target, expectedLocalID, physicalPaths...)
	if err != nil {
		return nil, err
	}
	identity, err := handoffLoadNodeIdentity(cfg, cfg.AuthMode == "writer")
	if err != nil {
		return nil, err
	}
	statePath := filepath.Join(cfg.ConfigDir, "auth-continuity", "state.json")
	before, err := handoffFileDigest(statePath)
	if err != nil {
		return nil, err
	}
	var state authSnapshotState
	if err := handoffReadJSON(statePath, &state, false, 4096); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg}
	bundle, err := s.readAuthSlot(state)
	if err != nil {
		return nil, err
	}
	named, err := readInstructionSetsState(cfg.InstructionSetsStateFile)
	if err != nil {
		return nil, err
	}
	if err := validateAuthSnapshotValues(bundle, cfg.AuthSnapshotBudget, cfg.CtlToken, func(id string) bool { _, ok := named[id]; return id == "" || id == defaultInstructionSetID || ok }); err != nil {
		return nil, err
	}
	domains := 0
	if cfg.AuthMode == "writer" {
		values, err := handoffReadDomains(cfg, expectedLocalID, target)
		if err != nil {
			return nil, err
		}
		domains = len(values)
	}
	var pending, controls, legacy int
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, taskDatabaseFilename)); err == nil {
		db, err := openTaskReadDatabase(filepath.Join(cfg.ConfigDir, taskDatabaseFilename))
		if err != nil {
			return nil, err
		}
		defer db.Close()
		// Keep all live ledger counts in one read transaction. The authority
		// controller fences admission; this inspection never edits the ledger.
		tx, err := db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if err := tx.QueryRow(`SELECT count(*) FROM task_records WHERE kind IN ('shell','relay') AND coalesce(json_extract(payload,'$.status'),'') NOT IN ('completed','failed','cancelled','expired')`).Scan(&pending); err != nil {
			return nil, err
		}
		if err := tx.QueryRow(`SELECT count(*) FROM task_controls`).Scan(&controls); err != nil {
			return nil, err
		}
		if err := tx.QueryRow(`SELECT count(*) FROM task_records WHERE kind='shell' AND json_extract(payload,'$.server')<>?`, strings.TrimPrefix(target, "shell:")).Scan(&legacy); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	after, err := handoffFileDigest(statePath)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"schema": authorityHandoffSchema, "read_only": true, "consistent_stopped_export": false, "at": time.Now().UTC(), "target": target, "local_id": local["server_id"], "config_dir": cfg.ConfigDir, "auth_mode": cfg.AuthMode, "auth_source_id": cfg.AuthSourceID, "auth_writer_id": state.WriterID, "auth_generation": state.Generation, "auth_pending": state.Pending, "auth_state_stable_during_read": before == after, "validated_writer_domains": domains, "pending_task_records": pending, "pending_task_controls": controls, "legacy_terminal_candidates": legacy, "offline_shared_signer": cfg.ReaderOfflineSharedSigner, "node_seed_url": cfg.NodeSeedURL, "writer_connect_to": cfg.NodeWriterConnectTo, "node_public_url": cfg.NodePublicURL, "listener": cfg.Addr, "quorum_configured": cfg.QuorumConfigFile != ""}
	result["signer_fingerprint"] = handoffDigest([]byte(cfg.OAuthClientSecret))
	result["oauth_key_id"] = cfg.OAuthKeyID
	result["public_origin"] = cfg.PublicOrigin
	result["mcp_resource"] = cfg.MCPResource
	if identity != nil {
		result["node_id"] = identity.NodeID
		result["identity_writer_id"] = identity.WriterID
		result["identity_writer_url"] = identity.WriterURL
	} else {
		result["node_identity_absent"] = true
	}
	return result, nil
}
