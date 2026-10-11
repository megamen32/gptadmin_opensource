package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gofrs/flock"
)

const authSnapshotDefaultBudget = 256 << 10
const authSnapshotMaxBudget = 10 << 20

// These are the existing stores, not identities, keys, task state or history.
// A null profiles section explicitly declares that optional store absent.
type authSnapshot struct {
	Version    int                   `json:"version"`
	WriterID   string                `json:"writer_id"`
	Generation uint64                `json:"generation"`
	Tokens     *managedMCPTokenState `json:"tokens"`
	Clients    *oauthClientsState    `json:"clients"`
	Profiles   *accessProfilesState  `json:"profiles"`
}

type authSnapshotState struct {
	WriterID        string `json:"writer_id"`
	Generation      uint64 `json:"generation"`
	Slot            int    `json:"slot"`
	Digest          string `json:"digest"`
	Pending         bool   `json:"pending"`
	ReadOnly        bool   `json:"read_only"`
	ProfilesPresent bool   `json:"profiles_present"`
}

func (s *Server) authContinuityEnabled() bool {
	return s.cfg.AuthMode != "" && s.cfg.AuthMode != "legacy"
}
func (s *Server) authBaseDir() string   { return filepath.Join(s.cfg.ConfigDir, "auth-continuity") }
func (s *Server) authStatePath() string { return filepath.Join(s.authBaseDir(), "state.json") }
func (s *Server) authSlotDir(slot int) string {
	return filepath.Join(s.authBaseDir(), fmt.Sprintf("slot-%d", slot))
}
func (s *Server) authStoreDir() string {
	if s.authContinuityEnabled() && s.authInitialized {
		return s.authSlotDir(s.authState.Slot)
	}
	return s.cfg.ConfigDir
}

func regularAuthPath(path string, optional bool) error {
	info, err := os.Lstat(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("auth state path is not a regular file: %s", filepath.Base(path))
	}
	return nil
}
func ensureAuthDir(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("auth state directory must not be a symlink")
	}
	return nil
}

// Must run after the node has pinned its real embedded executor identity.
// Changing reader to writer is an explicit operator handoff, not an election.
func (s *Server) InitializeAuthContinuity() error {
	if !s.authContinuityEnabled() {
		return nil
	}
	s.mu.Lock()
	err := s.initializeAuthContinuityLocked()
	s.mu.Unlock()
	if err == nil && s.cfg.AuthMode == "writer" {
		err = s.reconcileExistingMCPBearers()
	}
	return err
}

func (s *Server) initializeAuthContinuityLocked() error {
	if s.authInitialized {
		return nil
	}
	if s.cfg.AuthMode != "writer" && s.cfg.AuthMode != "reader" {
		return errors.New("GPTADMIN_AUTH_MODE must be legacy, writer or reader")
	}
	if s.cfg.AuthSnapshotBudget <= 0 || s.cfg.AuthSnapshotBudget > authSnapshotMaxBudget {
		return errors.New("auth snapshot budget must be between 1 and 10 MiB")
	}
	if s.cfg.ConfigDir == "" {
		return errors.New("auth continuity requires a config directory")
	}
	if len(s.localExecutors) != 1 {
		return errors.New("auth continuity requires one pinned local node identity")
	}
	for _, identity := range s.localExecutors {
		s.authLocalID = identity["server_id"]
	}
	if s.authLocalID == "" {
		return errors.New("local node identity is missing")
	}
	if s.cfg.AuthMode == "reader" && s.cfg.AuthSourceID == "" {
		// A joined member learns the writer identity through its node identity;
		// the explicit env source remains authoritative when set. With only a
		// writer URL known, membership pins the writer ID on first heartbeat
		// and control snapshots are then source-checked against it.
		if s.nodeIdentity != nil && s.nodeIdentity.WriterID != "" {
			s.cfg.AuthSourceID = s.nodeIdentity.WriterID
		}
	}
	if s.cfg.AuthMode == "reader" && s.cfg.AuthSourceID == "" && (s.nodeIdentity == nil || s.nodeIdentity.WriterURL == "") {
		return errors.New("reader requires GPTADMIN_AUTH_SOURCE_ID of the enrolled writer")
	}
	if err := ensureAuthDir(s.authBaseDir()); err != nil {
		return err
	}
	lockPath := filepath.Join(s.authBaseDir(), "runtime.lock")
	if err := regularAuthPath(lockPath, true); err != nil {
		return err
	}
	lock := flock.New(lockPath)
	held, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !held {
		return errors.New("auth state already owned by another local process")
	}
	s.authRuntimeLock = lock
	if err := regularAuthPath(s.authStatePath(), true); err != nil {
		return err
	}
	if err := readAccessStateJSON(s.authStatePath(), 4096, &s.authState); err != nil {
		return err
	}
	if s.authState.Slot < 0 || s.authState.Slot > 1 {
		return errors.New("invalid auth generation slot")
	}
	if s.authState.Generation > 0 && !s.authState.Pending {
		bundle, err := s.readAuthSlot(s.authState)
		if err != nil {
			return err
		}
		if s.authState.ReadOnly && authSnapshotDigest(bundle) != s.authState.Digest {
			return errors.New("auth generation digest mismatch")
		}
		s.authInitialized = true
		s.installAuthMapsLocked(bundle)
	} else {
		s.authInitialized = true
	}
	if s.cfg.AuthMode == "writer" {
		if s.authState.Pending {
			return errors.New("incomplete auth apply; recover in reader mode before promotion")
		}
		if s.authState.Generation == 0 {
			bundle := authSnapshot{Version: 1, WriterID: s.authLocalID, Generation: 1,
				Tokens: &managedMCPTokenState{}, Clients: &oauthClientsState{}}
			// New() preserves legacy startup behavior by logging load failures.
			// Enabling replication must instead reject unreadable authority.
			for name, dest := range map[string]any{"mcp_tokens_state.json": bundle.Tokens, oauthClientsStateFilename: bundle.Clients} {
				path := filepath.Join(s.cfg.ConfigDir, name)
				if err := regularAuthPath(path, true); err != nil {
					return err
				}
				if err := readAccessStateJSON(path, authSnapshotMaxBudget, dest); err != nil {
					return err
				}
			}
			if _, err := os.Lstat(filepath.Join(s.cfg.ConfigDir, accessProfilesStateFilename)); err == nil {
				path := filepath.Join(s.cfg.ConfigDir, accessProfilesStateFilename)
				if err := regularAuthPath(path, false); err != nil {
					return err
				}
				bundle.Profiles = &accessProfilesState{}
				if err := readAccessStateJSON(path, accessProfileStateMaxBytes, bundle.Profiles); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			s.excludeSnapshotControlCredentials(&bundle)
			if err := s.commitAuthSnapshotLocked(bundle, false); err != nil {
				return err
			}
		}
		s.authState.WriterID = s.authLocalID
		s.authState.ReadOnly = false
		return writeAccessStateJSON(s.authStatePath(), s.authState, 4096)
	}
	return nil
}

func (s *Server) authWriteErrorLocked() error {
	if !s.authContinuityEnabled() {
		return nil
	}
	if s.cfg.AuthMode != "writer" {
		return errors.New("auth reader cannot mutate authority; use the explicit writer")
	}
	if !s.authInitialized || s.authState.Pending {
		return errors.New("auth authority is not ready")
	}
	return nil
}
func (s *Server) authWriterHTTP(w http.ResponseWriter) bool {
	s.mu.Lock()
	err := s.authWriteErrorLocked()
	s.mu.Unlock()
	if err == nil {
		return true
	}
	writeJSON(w, 503, map[string]any{"error": "temporarily_unavailable", "error_description": err.Error()})
	return false
}

type authAdmissionReleaseKey struct{}

// Authentication attaches claims and a profile value to the request under the
// generation gate. Execution keeps that admission snapshot, but must not hold
// the gate while waiting on an executor or upstream. Authority mutations retain
// the gate through completion. Accepted jobs are not cancelled by later revoke.
func releaseAuthAdmission(r *http.Request) {
	if r != nil {
		if release, ok := r.Context().Value(authAdmissionReleaseKey{}).(func()); ok {
			release()
		}
	}
}

// A one-shot release is shared by nested request contexts and the final defer.
func (s *Server) authSnapshotGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authContinuityEnabled() || strings.HasPrefix(r.URL.Path, "/queue/") || r.URL.Path == "/heartbeat" || r.URL.Path == "/healthz" || r.URL.Path == "/version" || r.URL.Path == "/metrics" || strings.HasPrefix(r.URL.Path, "/mcp-relay/poll/") {
			next.ServeHTTP(w, r)
			return
		}
		// Never hold a generation lock while waiting for client-controlled input.
		// This also covers owner apply/export bodies before their exclusive lock.
		limit := int64(authSnapshotMaxBudget)
		if strings.HasPrefix(r.URL.Path, "/admin/api/auth-snapshot/") {
			limit = int64(s.cfg.AuthSnapshotBudget)
		}
		// Node bearer admission owns its smaller body limit and native read
		// deadline; do not consume that body before its enrolled-node gate.
		if r.Body != nil && r.Body != http.NoBody && r.URL.Path != nodeBearerAdmissionPath {
			if r.ContentLength > limit {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"detail": "request body exceeds configured limit"})
				return
			}
			raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "cannot read request body"})
				return
			}
			if int64(len(raw)) > limit {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"detail": "request body exceeds configured limit"})
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		if strings.HasPrefix(r.URL.Path, "/admin/api/auth-snapshot/") {
			next.ServeHTTP(w, r)
			return
		}
		// Member bootstrap: join/membership/heartbeat/control-snapshot must work
		// before the first control-snapshot apply. They have their own credential
		// gate; auth-snapshot readiness is what we are trying to bootstrap.
		if strings.HasPrefix(r.URL.Path, "/admin/api/nodes") {
			next.ServeHTTP(w, r)
			return
		}
		s.authMu.RLock()
		var once sync.Once
		release := func() { once.Do(s.authMu.RUnlock) }
		defer release()
		r = r.WithContext(context.WithValue(r.Context(), authAdmissionReleaseKey{}, release))
		s.mu.Lock()
		var err error
		if !s.authInitialized || s.authState.Pending || s.authState.Generation == 0 {
			err = errors.New("auth snapshot is not ready")
		} else {
			if s.cfg.AuthMode == "reader" && s.authState.WriterID != s.cfg.AuthSourceID {
				err = errors.New("auth source differs from enrolled writer")
			} else {
				var bundle authSnapshot
				bundle, err = s.readAuthSlot(s.authState)
				if err == nil && s.cfg.AuthMode == "reader" && authSnapshotDigest(bundle) != s.authState.Digest {
					err = errors.New("auth generation was modified outside snapshot apply")
				}
			}
		}
		s.mu.Unlock()
		if err != nil {
			writeJSON(w, 503, map[string]any{"detail": err.Error()})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func authSnapshotDigest(bundle authSnapshot) string {
	data, _ := json.Marshal(bundle)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (s *Server) readAuthSlot(state authSnapshotState) (authSnapshot, error) {
	bundle := authSnapshot{Version: 1, WriterID: state.WriterID, Generation: state.Generation, Tokens: &managedMCPTokenState{}, Clients: &oauthClientsState{}}
	dir := s.authSlotDir(state.Slot)
	info, err := os.Lstat(dir)
	if err != nil {
		return bundle, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return bundle, errors.New("invalid auth slot directory")
	}
	for name, dest := range map[string]any{"mcp_tokens_state.json": bundle.Tokens, oauthClientsStateFilename: bundle.Clients} {
		path := filepath.Join(dir, name)
		if err := regularAuthPath(path, false); err != nil {
			return bundle, err
		}
		if err := readAccessStateJSON(path, authSnapshotMaxBudget, dest); err != nil {
			return bundle, err
		}
	}
	path := filepath.Join(dir, accessProfilesStateFilename)
	if state.ProfilesPresent {
		if err := regularAuthPath(path, false); err != nil {
			return bundle, err
		}
		bundle.Profiles = &accessProfilesState{}
		if err := readAccessStateJSON(path, accessProfileStateMaxBytes, bundle.Profiles); err != nil {
			return bundle, err
		}
	} else if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return bundle, errors.New("unexpected optional auth profile store")
	}
	return bundle, nil
}

func (s *Server) validateAuthSnapshot(bundle authSnapshot) error {
	return validateAuthSnapshotValues(bundle, s.cfg.AuthSnapshotBudget, s.cfg.CtlToken, s.instructionSetExists)
}

func validateAuthSnapshotValues(bundle authSnapshot, budget int, ctlToken string, instructionSetExists func(string) bool) error {
	if bundle.Version != 1 || strings.TrimSpace(bundle.WriterID) == "" || bundle.Generation == 0 || bundle.Tokens == nil || bundle.Clients == nil {
		return errors.New("incomplete auth snapshot manifest")
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	if len(data) > budget {
		return errors.New("auth snapshot exceeds configured budget")
	}
	for _, store := range []struct {
		value any
		limit int
	}{{bundle.Tokens, 8 << 20}, {bundle.Clients, oauthClientsStateMaxBytes}, {bundle.Profiles, accessProfileStateMaxBytes}} {
		data, err := json.MarshalIndent(store.value, "", "  ")
		if err != nil {
			return err
		}
		if len(data)+1 > store.limit {
			return errors.New("auth snapshot exceeds existing store limit")
		}
	}
	for id, record := range bundle.Tokens.Tokens {
		if id == "" || record.ID != id || record.TokenKind == "legacy_ctl" || (ctlToken != "" && (record.TokenValue == ctlToken || record.TokenDigest == configuredMCPBearerDigest(ctlToken))) {
			return errors.New("invalid or control credential in auth snapshot")
		}
		if record.ProfileID != "" && (bundle.Profiles == nil || bundle.Profiles.Profiles[record.ProfileID].ID != record.ProfileID) {
			return errors.New("missing managed-token access profile")
		}
	}
	if len(bundle.Clients.Clients) > oauthClientsMaxItems {
		return errors.New("too many OAuth clients")
	}
	for id, record := range bundle.Clients.Clients {
		if err := validateOAuthClientMetadata(id, record); err != nil {
			return err
		}
		if record.ProfileID != "" && (bundle.Profiles == nil || bundle.Profiles.Profiles[record.ProfileID].ID != record.ProfileID) {
			return errors.New("missing OAuth access profile")
		}
	}
	if bundle.Profiles != nil {
		for id, profile := range bundle.Profiles.Profiles {
			if id != profile.ID {
				return errors.New("invalid access profile identity")
			}
			if err := validateAccessProfile(profile); err != nil {
				return err
			}
			if !instructionSetExists(profile.InstructionSetID) {
				return errors.New("access profile requires locally provisioned instruction set")
			}
		}
	}
	return nil
}

func (s *Server) installAuthMapsLocked(bundle authSnapshot) {
	s.managedMCP = cloneAccessRecords(bundle.Tokens.Tokens)
	s.managedMCPPersisted = cloneAccessRecords(bundle.Tokens.Tokens)
	s.oauthClients = cloneAccessRecords(bundle.Clients.Clients)
	s.oauthClientsPersisted = cloneAccessRecords(bundle.Clients.Clients)
	s.accessProfiles = map[string]AccessProfile{}
	if bundle.Profiles != nil {
		s.accessProfiles = cloneAccessProfiles(bundle.Profiles.Profiles)
	}
}

func (s *Server) excludeSnapshotControlCredentials(bundle *authSnapshot) {
	excludeSnapshotControlCredentials(bundle, s.cfg.CtlToken)
}

func excludeSnapshotControlCredentials(bundle *authSnapshot, ctlToken string) {
	for id, record := range bundle.Tokens.Tokens {
		if record.TokenKind == "legacy_ctl" || (ctlToken != "" && (record.TokenValue == ctlToken || record.TokenDigest == configuredMCPBearerDigest(ctlToken))) {
			delete(bundle.Tokens.Tokens, id)
		}
	}
}

// The pending marker is durable before touching the inactive slot. A crash
// cannot silently fall back to an older, more permissive auth generation.
func (s *Server) commitAuthSnapshotLocked(bundle authSnapshot, reader bool) error {
	if err := s.validateAuthSnapshot(bundle); err != nil {
		return err
	}
	if err := regularAuthPath(s.authStatePath(), true); err != nil {
		return err
	}
	if reader {
		if err := s.preflightReaderAuthSnapshot(bundle); err != nil {
			return err
		}
	}
	s.authState.Pending = true
	if err := writeAccessStateJSON(s.authStatePath(), s.authState, 4096); err != nil {
		return err
	}
	slot := 1 - s.authState.Slot
	dir := s.authSlotDir(slot)
	if err := ensureAuthDir(dir); err != nil {
		return err
	}
	files := map[string]any{"mcp_tokens_state.json": bundle.Tokens, oauthClientsStateFilename: bundle.Clients}
	if bundle.Profiles != nil {
		files[accessProfilesStateFilename] = bundle.Profiles
	}
	for name, value := range files {
		path := filepath.Join(dir, name)
		if err := regularAuthPath(path, true); err != nil {
			return err
		}
		if err := regularAuthPath(path+".lock", true); err != nil {
			return err
		}
		if err := withAccessStateLock(path, func() error { return writeAccessStateJSON(path, value, authSnapshotMaxBudget) }); err != nil {
			return err
		}
	}
	if bundle.Profiles == nil {
		path := filepath.Join(dir, accessProfilesStateFilename)
		if err := regularAuthPath(path, true); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	next := authSnapshotState{WriterID: bundle.WriterID, Generation: bundle.Generation, Slot: slot, Digest: authSnapshotDigest(bundle), ReadOnly: reader, ProfilesPresent: bundle.Profiles != nil}
	if err := writeAccessStateJSON(s.authStatePath(), next, 4096); err != nil {
		return err
	}
	s.authState = next
	s.installAuthMapsLocked(bundle)
	return nil
}

func (s *Server) authSnapshotHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.authContinuityEnabled() {
		writeJSON(w, 404, map[string]any{"detail": "auth continuity is not enabled"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"detail": "POST required"})
		return
	}
	// Bootstrap/recovery uses the existing owner control credential. Ordinary
	// and delegated admin credentials must not export the private token store.
	if s.cfg.CtlToken == "" || strings.TrimSpace(r.Header.Get("Authorization")) != "Bearer "+s.cfg.CtlToken {
		writeJSON(w, 403, map[string]any{"detail": "owner control credential required"})
		return
	}
	s.authMu.Lock()
	s.mu.Lock()
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { s.mu.Unlock(); s.authMu.Unlock() })
	}
	defer release()
	respond := func(status int, value any) {
		// Detach response bytes while state is coherent, then release both
		// locks before any potentially slow network write (including errors).
		body, err := json.Marshal(value)
		if err != nil {
			status = http.StatusInternalServerError
			body = []byte(`{"error":"json encode failed"}`)
		}
		release()
		writeJSON(w, status, json.RawMessage(body))
	}
	if !s.authInitialized {
		respond(503, map[string]any{"detail": "auth continuity is not initialized"})
		return
	}
	switch r.URL.Path {
	case "/admin/api/auth-snapshot/export":
		if err := s.authWriteErrorLocked(); err != nil {
			respond(409, map[string]any{"detail": err.Error()})
			return
		}
		bundle, err := s.readAuthSlot(s.authState)
		if err != nil {
			respond(503, map[string]any{"detail": err.Error()})
			return
		}
		bundle.WriterID = s.authLocalID
		bundle.Generation++
		s.excludeSnapshotControlCredentials(&bundle)
		if err := s.commitAuthSnapshotLocked(bundle, false); err != nil {
			respond(409, map[string]any{"detail": err.Error()})
			return
		}
		respond(200, bundle)
	case "/admin/api/auth-snapshot/apply":
		if s.cfg.AuthMode != "reader" {
			respond(409, map[string]any{"detail": "only an explicit reader can apply snapshots"})
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, int64(s.cfg.AuthSnapshotBudget)+1))
		if err != nil || len(raw) > s.cfg.AuthSnapshotBudget {
			respond(413, map[string]any{"detail": "auth snapshot exceeds configured budget"})
			return
		}
		var manifest map[string]json.RawMessage
		if json.Unmarshal(raw, &manifest) != nil || manifest["profiles"] == nil {
			respond(400, map[string]any{"detail": "explicit profiles manifest required"})
			return
		}
		var bundle authSnapshot
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&bundle); err != nil {
			respond(400, map[string]any{"detail": "invalid auth snapshot"})
			return
		}
		if bundle.WriterID != s.cfg.AuthSourceID || bundle.Generation <= s.authState.Generation {
			respond(409, map[string]any{"detail": "foreign or stale auth snapshot"})
			return
		}
		if err := s.commitAuthSnapshotLocked(bundle, true); err != nil {
			status := 409
			if errors.Is(err, errAuthSnapshotSpace) {
				status = http.StatusInsufficientStorage
			}
			respond(status, map[string]any{"detail": err.Error()})
			return
		}
		respond(200, map[string]any{"ok": true, "writer_id": bundle.WriterID, "generation": bundle.Generation})
	default:
		respond(404, map[string]any{"detail": "unknown snapshot action"})
	}
}
